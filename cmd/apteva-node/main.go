// apteva-node is the daemon that runs on a Node: it drives the hardware
// declared in node.yaml, serves the on-device UI, and links to an Apteva
// instance so the bound agent can see, hear and act through the device.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/apteva/node/internal/config"
	"github.com/apteva/node/internal/hw"
	"github.com/apteva/node/internal/localui"
	"github.com/apteva/node/internal/netmgr"
	"github.com/apteva/node/internal/node"
	"github.com/apteva/node/ui"
)

var version = "dev"

func main() {
	cfgPath := flag.String("config", "/etc/apteva-node/node.yaml", "path to node.yaml")
	sim := flag.Bool("sim", false, "simulate hardware (default on non-Linux)")
	debug := flag.Bool("debug", false, "verbose logging")
	uiDir := flag.String("ui-dir", "", "serve the UI from this directory instead of the embedded build (development)")
	stateDir := flag.String("state-dir", "", "where pairing and PIN are kept (overrides node.yaml state_dir)")
	reset := flag.Bool("reset", false, "forget pairing, PIN and setup, then exit")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}
	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	if err := run(*cfgPath, *sim, *uiDir, *stateDir, *reset, log); err != nil {
		log.Error("apteva-node stopped", "err", err)
		os.Exit(1)
	}
}

func run(cfgPath string, sim bool, uiDir, stateDir string, reset bool, log *slog.Logger) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("config %s: %w", cfgPath, err)
	}
	if stateDir != "" {
		cfg.StateDir = stateDir
	}

	var backend hw.Backend
	var nm netmgr.Manager
	if sim || cfg.Hardware.Sim || runtime.GOOS != "linux" {
		backend, nm = hw.NewSim(), netmgr.NewSim(cfg.StateDir)
	} else if backend, err = hw.NewLinux(cfg.Hardware.GPIOChip); err != nil {
		return err
	} else {
		nm = netmgr.NewNMCLI()
	}
	defer backend.Close()

	n, err := node.New(cfg, backend, nm, version, log)
	if err != nil {
		return err
	}
	if reset {
		if err := n.FactoryReset(); err != nil {
			return err
		}
		log.Info("reset: the device will start setup on next boot")
		return nil
	}
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	assets := ui.Assets()
	if uiDir != "" {
		assets = os.DirFS(uiDir)
	}
	srv := &http.Server{
		Addr:              cfg.UI.Listen,
		Handler:           (&localui.Server{Node: n, Assets: assets, Log: log}).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("ui server", "err", err)
			stop()
		}
	}()

	log.Info("apteva-node up", "version", version, "name", cfg.Name, "hardware", backend.Name(),
		"ui", "http://"+cfg.UI.Listen, "mode", n.Mode(), "state", cfg.StateDir)
	n.Run(ctx)

	shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}
