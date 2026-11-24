// Package localui serves the on-device UI and its live state socket. It
// listens on loopback only and refuses any Host it doesn't recognise, so a
// web page in some other browser can't reach the Node's controls through
// DNS rebinding.
package localui

import (
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/apteva/node/internal/node"
	"github.com/apteva/node/internal/proto"
)

type Server struct {
	Node   *node.Node
	Assets fs.FS
	Log    *slog.Logger
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws", s.ws)
	mux.HandleFunc("GET /photo/{cap}", s.photo)
	mux.HandleFunc("GET /api/snapshot", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(s.Node.Snapshot())
	})
	mux.HandleFunc("POST /api/app-request", s.appRequest)
	s.setupRoutes(mux)
	mux.Handle("GET /", http.FileServerFS(s.Assets))
	return loopbackOnly(mux)
}

func loopbackOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if host != "localhost" && host != "127.0.0.1" && host != "[::1]" && host != "::1" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		o := r.Header.Get("Origin")
		return o == "" || strings.HasPrefix(o, "http://localhost:") || strings.HasPrefix(o, "http://127.0.0.1:")
	},
}

// uiCommand is what the screen can ask for.
type uiCommand struct {
	Type       string `json:"type"` // message | approve | toggle | press
	Text       string `json:"text,omitempty"`
	ID         string `json:"id,omitempty"`
	Allow      bool   `json:"allow,omitempty"`
	Held       bool   `json:"held,omitempty"` // Allow was pressed and held
	Capability string `json:"capability,omitempty"`
	Down       bool   `json:"down,omitempty"`
}

func (s *Server) ws(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	updates, unsubscribe := s.Node.Subscribe()
	defer unsubscribe()

	go func() {
		for {
			var cmd uiCommand
			if err := conn.ReadJSON(&cmd); err != nil {
				conn.Close()
				return
			}
			if err := s.handle(cmd); err != nil {
				s.Log.Warn("ui command failed", "type", cmd.Type, "err", err)
			}
		}
	}()

	push := func() error {
		conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		return conn.WriteJSON(map[string]any{"type": "snapshot", "data": s.Node.Snapshot()})
	}
	if push() != nil {
		return
	}
	// Coalesce bursts and refresh periodically for the clock and system stats.
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case _, ok := <-updates:
			if !ok {
				return
			}
			time.Sleep(40 * time.Millisecond)
		case <-tick.C:
		case <-r.Context().Done():
			return
		}
		if push() != nil {
			return
		}
	}
}

func (s *Server) handle(c uiCommand) error {
	switch c.Type {
	case "message":
		s.Node.SendMessage(strings.TrimSpace(c.Text))
	case "approve":
		s.Node.Resolve(c.ID, c.Allow, c.Held)
	case "toggle":
		return s.Node.Toggle(c.Capability)
	case "press":
		return s.Node.Press(c.Capability, c.Down)
	case "select_project":
		return s.Node.SelectProject(c.ID)
	case "select_agent":
		return s.Node.SelectAgent(c.ID)
	}
	return nil
}

func (s *Server) photo(w http.ResponseWriter, r *http.Request) {
	jpg, ok := s.Node.Photo(r.PathValue("cap"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Write(jpg)
}

// appRequest lets a widget on screen reach its app through the daemon.
func (s *Server) appRequest(w http.ResponseWriter, r *http.Request) {
	if o := r.Header.Get("Origin"); o != "" && !upgrader.CheckOrigin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var req proto.AppRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	res, err := s.Node.AppRequest(r.Context(), req)
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(res)
}
