package localui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/apteva/node/internal/netmgr"
)

// Setup and settings. During first-boot setup anyone at the screen may make
// changes; afterwards, if a PIN is set, they need an unlock token (from
// POST /api/unlock) in the X-Unlock header.

type setupBody struct {
	PIN        string `json:"pin"`
	SSID       string `json:"ssid"`
	Password   string `json:"password"`
	URL        string `json:"url"`
	Capability string `json:"capability"`
}

func (s *Server) setupRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/unlock", s.api(false, func(ctx context.Context, b setupBody) (any, error) {
		tok, exp, err := s.Node.Unlock(b.PIN)
		return map[string]any{"token": tok, "expires_at": exp}, err
	}))
	mux.HandleFunc("POST /api/setup/wifi/scan", s.api(true, func(ctx context.Context, b setupBody) (any, error) {
		nets, err := s.Node.ScanWiFi(ctx)
		if nets == nil {
			nets = []netmgr.Network{}
		}
		return map[string]any{"networks": nets}, err
	}))
	mux.HandleFunc("POST /api/setup/wifi", s.api(true, func(ctx context.Context, b setupBody) (any, error) {
		err := s.Node.ConnectWiFi(ctx, b.SSID, b.Password)
		if errors.Is(err, netmgr.ErrWrongPassword) {
			err = errors.New("wrong password for " + b.SSID)
		}
		return nil, err
	}))
	mux.HandleFunc("POST /api/setup/server", s.api(true, func(ctx context.Context, b setupBody) (any, error) {
		u, err := s.Node.SetServer(ctx, b.URL)
		return map[string]string{"url": u}, err
	}))
	mux.HandleFunc("POST /api/setup/pair", s.api(true, func(ctx context.Context, b setupBody) (any, error) {
		return nil, s.Node.StartPairing()
	}))
	mux.HandleFunc("POST /api/setup/pair/cancel", s.api(true, func(ctx context.Context, b setupBody) (any, error) {
		s.Node.CancelPairing()
		return nil, nil
	}))
	mux.HandleFunc("POST /api/setup/standalone", s.api(true, func(ctx context.Context, b setupBody) (any, error) {
		return nil, s.Node.UseStandalone()
	}))
	mux.HandleFunc("POST /api/setup/pin", s.api(true, func(ctx context.Context, b setupBody) (any, error) {
		return nil, s.Node.SetPIN(b.PIN)
	}))
	mux.HandleFunc("POST /api/setup/finish", s.api(true, func(ctx context.Context, b setupBody) (any, error) {
		return nil, s.Node.FinishSetup()
	}))
	// Not behind the PIN: a forgotten PIN must not brick the device, and a
	// reset only unpairs it — getting it back into a project still needs an
	// approval in the dashboard.
	mux.HandleFunc("POST /api/setup/reset", s.api(false, func(ctx context.Context, b setupBody) (any, error) {
		return nil, s.Node.FactoryReset()
	}))
	mux.HandleFunc("POST /api/setup/test-photo", s.api(true, func(ctx context.Context, b setupBody) (any, error) {
		return nil, s.Node.LocalCapture(b.Capability)
	}))
	mux.HandleFunc("POST /api/setup/notice/dismiss", s.api(false, func(ctx context.Context, b setupBody) (any, error) {
		s.Node.DismissNotice()
		return nil, nil
	}))
}

func (s *Server) api(guarded bool, fn func(context.Context, setupBody) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Only the device's own page may call these, never a cross-site form.
		if !upgrader.CheckOrigin(r) || r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
			return
		}
		if guarded && !s.Node.Authorized(r.Header.Get("X-Unlock")) {
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "locked"})
			return
		}
		var b setupBody
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&b); err != nil && !errors.Is(err, io.EOF) {
			http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		out, err := fn(ctx, b)
		if err != nil {
			w.WriteHeader(http.StatusUnprocessableEntity)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		if out == nil {
			out = map[string]bool{"ok": true}
		}
		json.NewEncoder(w).Encode(out)
	}
}
