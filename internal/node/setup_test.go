package node

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apteva/node/internal/config"
	"github.com/apteva/node/internal/hw"
	"github.com/apteva/node/internal/link"
	"github.com/apteva/node/internal/netmgr"
)

// startUnpaired boots a node with no server in node.yaml: first-boot setup.
func startUnpaired(t *testing.T) (*Node, *hw.Sim) {
	t.Helper()
	cfg, err := config.Parse([]byte(testYAML))
	if err != nil {
		t.Fatal(err)
	}
	cfg.StateDir = t.TempDir()
	sim := hw.NewSim()
	n, err := New(cfg, sim, netmgr.NewSim(cfg.StateDir), "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go n.Run(ctx)
	return n, sim
}

func TestStandaloneKeepsHardwareAndQueuesNothing(t *testing.T) {
	n, _ := startUnpaired(t)
	if n.Mode() != ModeSetup {
		t.Fatalf("mode %s, want setup", n.Mode())
	}
	if err := n.UseStandalone(); err != nil {
		t.Fatal(err)
	}
	n.FinishSetup()
	if n.Mode() != ModeStandalone {
		t.Fatalf("mode %s, want standalone", n.Mode())
	}
	if s := n.Snapshot(); s.Link.State != link.StateStandalone {
		t.Fatalf("link %s", s.Link.State)
	}
	n.SendMessage("hello?")
	if q := n.Snapshot().Queued; q != 0 {
		t.Fatalf("queued %d frames with no server", q)
	}
	n.Toggle("lamp")
	waitFor(t, "lamp on", func() bool { return lampOn(n) })
	// No server ever: safe state must not kick in and turn the lamp off.
	time.Sleep(200 * time.Millisecond)
	if !lampOn(n) {
		t.Fatal("safe state applied without ever having a server")
	}
}

// pairServer answers /health and the device-code flow, and hands out "tok",
// which the fake link server accepts.
func pairServer(t *testing.T, fs *fakeServer, approveAfter int32, revoked *atomic.Bool) *httptest.Server {
	var polls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"status":"ok"}`)) })
	mux.HandleFunc("POST /api/devices/pair/start", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"pairing_id": "p1", "user_code": "KQ4-7MZ", "verify_url": "http://x/pair?code=KQ4-7MZ", "expires_in": 60, "interval": 0})
	})
	mux.HandleFunc("POST /api/devices/pair/poll", func(w http.ResponseWriter, r *http.Request) {
		if polls.Add(1) < approveAfter {
			w.Write([]byte(`{"status":"pending"}`))
			return
		}
		w.Write([]byte(`{"status":"approved","token":"tok","agent":"Atlas"}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if revoked.Load() {
			http.Error(w, "revoked", http.StatusUnauthorized)
			return
		}
		fs.srv.Config.Handler.ServeHTTP(w, r)
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func TestPairingConnectsAndRevokeFallsBackToStandalone(t *testing.T) {
	fs := newFakeServer(t)
	var revoked atomic.Bool
	srv := pairServer(t, fs, 2, &revoked) // one "pending", then approved (polls are 2 s apart)
	n, _ := startUnpaired(t)

	if _, err := n.SetServer(context.Background(), srv.URL); err != nil {
		t.Fatal(err)
	}
	if err := n.StartPairing(); err != nil {
		t.Fatal(err)
	}
	if p := n.setupView().Pairing; p.Status != "waiting" || p.Code != "KQ4-7MZ" {
		t.Fatalf("pairing %+v", p)
	}
	select {
	case <-fs.hello:
	case <-time.After(8 * time.Second):
		t.Fatal("never linked after approval")
	}
	n.FinishSetup()
	if n.Mode() != ModeConnected || n.setupView().Agent != "Atlas" {
		t.Fatalf("mode %s view %+v", n.Mode(), n.setupView())
	}

	// The device is removed in the dashboard: its token stops working.
	revoked.Store(true)
	fs.mu.Lock()
	fs.conn.Close()
	fs.mu.Unlock()
	waitFor(t, "standalone after revoke", func() bool { return n.Mode() == ModeStandalone })
	if n.setupView().Notice == "" {
		t.Fatal("no notice explaining the revoke")
	}
}

func TestPinUnlockAndLockout(t *testing.T) {
	n, _ := startUnpaired(t)
	if !n.Authorized("") {
		t.Fatal("setup must not need a PIN")
	}
	n.SetPIN("2468")
	n.UseStandalone()
	n.FinishSetup()
	if n.Authorized("") {
		t.Fatal("settings open without the PIN")
	}
	tok, _, err := n.Unlock("2468")
	if err != nil || !n.Authorized(tok) {
		t.Fatalf("unlock: %v", err)
	}
	for i := 0; i < pinMaxFails; i++ {
		n.Unlock("0000")
	}
	if _, _, err := n.Unlock("2468"); err == nil {
		t.Fatal("right PIN accepted during lockout")
	}
}
