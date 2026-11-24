// Package netmgr lets setup see and change the device's network: is it
// online, which Wi-Fi networks are around, join one. On a Pi it drives
// NetworkManager (Raspberry Pi OS Bookworm's default) through nmcli; the sim
// fakes a few networks so the setup screens can be built on a laptop.
package netmgr

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

type Status struct {
	Supported   bool   `json:"supported"` // Wi-Fi can be managed from setup
	Online      bool   `json:"online"`
	Ethernet    bool   `json:"ethernet"`
	WiFiSSID    string `json:"wifi_ssid,omitempty"`
	WiFiSignal  int    `json:"wifi_signal,omitempty"`
	ClockSynced bool   `json:"clock_synced"`
}

type Network struct {
	SSID   string `json:"ssid"`
	Signal int    `json:"signal"` // 0–100
	Secure bool   `json:"secure"`
	InUse  bool   `json:"in_use"`
}

type Manager interface {
	Status(ctx context.Context) Status
	Scan(ctx context.Context) ([]Network, error)
	Connect(ctx context.Context, ssid, password string) error
}

var ErrWrongPassword = errors.New("wrong Wi-Fi password")

// Sim pretends there is no cable and a handful of Wi-Fi networks. The joined
// network survives restarts (in the state dir) like a real saved connection.
type Sim struct {
	mu   sync.Mutex
	file string
	ssid string
}

func NewSim(stateDir string) *Sim {
	s := &Sim{file: filepath.Join(stateDir, "sim-network.json")}
	if b, err := os.ReadFile(s.file); err == nil {
		json.Unmarshal(b, &s.ssid)
	}
	return s
}

var simNetworks = []Network{
	{SSID: "Studio", Signal: 82, Secure: true},
	{SSID: "Studio 5G", Signal: 64, Secure: true},
	{SSID: "Guest", Signal: 58, Secure: false},
	{SSID: "Bakery downstairs", Signal: 31, Secure: true},
}

func (s *Sim) Status(context.Context) Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{Supported: true, ClockSynced: true, Online: s.ssid != "", WiFiSSID: s.ssid}
	if s.ssid != "" {
		for _, n := range simNetworks {
			if n.SSID == s.ssid {
				st.WiFiSignal = n.Signal
			}
		}
	}
	return st
}

func (s *Sim) Scan(ctx context.Context) ([]Network, error) {
	select {
	case <-time.After(700 * time.Millisecond): // scanning takes a moment on real hardware too
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := slices.Clone(simNetworks)
	for i := range out {
		out[i].InUse = out[i].SSID == s.ssid
	}
	return out, nil
}

// Connect accepts any password except "wrong", to exercise the error path.
func (s *Sim) Connect(ctx context.Context, ssid, password string) error {
	select {
	case <-time.After(1200 * time.Millisecond):
	case <-ctx.Done():
		return ctx.Err()
	}
	idx := slices.IndexFunc(simNetworks, func(n Network) bool { return n.SSID == ssid })
	if idx < 0 {
		return errors.New("network not found")
	}
	if simNetworks[idx].Secure && (password == "wrong" || len(password) < 8) {
		return ErrWrongPassword
	}
	s.mu.Lock()
	s.ssid = ssid
	s.mu.Unlock()
	b, _ := json.Marshal(ssid)
	os.MkdirAll(filepath.Dir(s.file), 0o700)
	return os.WriteFile(s.file, b, 0o600)
}
