// node-sim stands in for the Apteva server's /api/devices/link endpoint so
// a Node can be developed before the server side exists. It accepts one
// Node, prints what the Node sends, plays a scripted "agent" that answers
// messages and reacts to events, and takes commands on stdin:
//
//	tools                     list the tools the node announced
//	call <tool> [json-args]   invoke a tool as the agent would
//	say <text>                agent says something on the device
//	status <state> [detail]   idle | thinking | acting | speaking
//	tick                      advance the fake apps and publish a refresh topic
//	kick                      drop the connection (tests reconnect + safe state)
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/apteva/node/internal/link"
	"github.com/apteva/node/internal/proto"
)

type session struct {
	mu    sync.Mutex
	conn  *websocket.Conn
	hello proto.Hello
	seq   int
	wait  map[string]chan proto.Result
	apps  *fakeApps
	pairs *pairings

	roster *roster
	active string // agent id the device is bound to
}

func (s *session) send(typ, id string, data any) error {
	f, err := proto.New(typ, id, data)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return fmt.Errorf("no node connected")
	}
	return s.conn.WriteJSON(f)
}

func (s *session) call(tool string, args json.RawMessage) (proto.Result, error) {
	s.mu.Lock()
	s.seq++
	id := fmt.Sprintf("c%d", s.seq)
	ch := make(chan proto.Result, 1)
	s.wait[id] = ch
	s.mu.Unlock()
	if err := s.send(proto.TypeInvoke, id, proto.Invoke{Tool: tool, Args: args}); err != nil {
		return proto.Result{}, err
	}
	select {
	case r := <-ch:
		return r, nil
	case <-time.After(2 * time.Minute):
		return proto.Result{}, fmt.Errorf("timed out")
	}
}

func main() {
	addr := flag.String("addr", "127.0.0.1:7490", "listen address")
	token := flag.String("token", "dev-node-token", "device token to accept")
	agentName := flag.String("agent", "Atlas", "agent name to announce")
	tokensFile := flag.String("tokens", ".node-sim-tokens.json", "where paired device tokens are kept")
	flag.Parse()

	s := &session{wait: map[string]chan proto.Result{}, apps: newFakeApps(), roster: newRoster()}
	// The -agent flag picks the default on the pairing page.
	names := []string{*agentName}
	for _, a := range s.roster.list() {
		if a.Name != *agentName {
			names = append(names, a.Name)
		}
	}
	s.pairs = newPairings(*tokensFile, *token, names)
	s.pairs.routes(http.DefaultServeMux)
	up := websocket.Upgrader{}

	http.HandleFunc(link.Path, func(w http.ResponseWriter, r *http.Request) {
		if !s.pairs.valid(r.Header.Get("Authorization")) {
			log.Printf("rejected node: bad token")
			http.Error(w, "unknown device", http.StatusUnauthorized)
			return
		}
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		var hello proto.Frame
		if err := conn.ReadJSON(&hello); err != nil || hello.Type != proto.TypeHello {
			conn.Close()
			return
		}
		h, _ := link.Decode[proto.Hello](hello)
		s.mu.Lock()
		if s.conn != nil {
			s.conn.Close()
		}
		s.conn, s.hello = conn, h
		s.mu.Unlock()
		log.Printf("node %q connected (v%s, %d tools, %d events)", h.Name, h.Version, len(h.Tools), len(h.Events))
		a, _ := s.roster.byName(s.pairs.agentFor(r.Header.Get("Authorization"), *agentName))
		s.bind(a.ID)
		s.send(proto.TypeWidgets, "", proto.Widgets{Widgets: widgets()})

		for {
			var f proto.Frame
			if err := conn.ReadJSON(&f); err != nil {
				log.Printf("node disconnected: %v", err)
				s.mu.Lock()
				if s.conn == conn {
					s.conn = nil
				}
				s.mu.Unlock()
				return
			}
			s.handle(f)
		}
	})

	// Same commands over HTTP, for scripted checks: curl -d 'kick' .../sim
	http.HandleFunc("POST /sim", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		s.command(string(b))
	})
	go s.appTicker(s.apps, 20*time.Second)
	go s.churn(6 * time.Second)
	go repl(s)
	log.Printf("node-sim listening on http://%s%s (token %q); pair devices at http://%s/pair", *addr, link.Path, *token, *addr)
	log.Fatal(http.ListenAndServe(*addr, nil))
}

func (s *session) handle(f proto.Frame) {
	switch f.Type {
	case proto.TypeResult:
		r, _ := link.Decode[proto.Result](f)
		s.mu.Lock()
		ch := s.wait[f.ID]
		delete(s.wait, f.ID)
		s.mu.Unlock()
		if ch != nil {
			ch <- r
		}
	case proto.TypeMessage:
		m, _ := link.Decode[proto.Message](f)
		log.Printf("person: %s", m.Text)
		go s.respond(m.Text)
	case proto.TypeEvent:
		e, _ := link.Decode[proto.Event](f)
		b, _ := json.Marshal(e.Data)
		log.Printf("event: %s %s %s", e.Capability, e.Kind, b)
		go s.react(e)
	case proto.TypeAppRequest:
		req, _ := link.Decode[proto.AppRequest](f)
		resp := s.apps.handle(req)
		log.Printf("app_request %s %s %s → %d", req.App, req.Method, req.Path, resp.Status)
		s.send(proto.TypeAppResponse, f.ID, resp)
	case proto.TypeSelect:
		sel, _ := link.Decode[proto.Select](f)
		if s.bind(sel.AgentID) {
			log.Printf("device switched to agent %s", sel.AgentID)
		}
	case proto.TypeState:
		st, _ := link.Decode[proto.State](f)
		b, _ := json.Marshal(st.Value)
		log.Printf("state: %s %s", st.Capability, b)
	default:
		log.Printf("frame %s", f.Type)
	}
}

// respond is a tiny scripted agent: enough to see every screen move.
func (s *session) respond(text string) {
	s.status("thinking", "")
	time.Sleep(900 * time.Millisecond)
	lower := strings.ToLower(text)
	tool := func(suffix string) string {
		for _, t := range s.hello.Tools {
			if strings.HasSuffix(t.Name, suffix) {
				return t.Name
			}
		}
		return ""
	}
	var reply string
	switch {
	case strings.Contains(lower, "photo") || strings.Contains(lower, "picture") || strings.Contains(lower, "look"):
		if t := tool("_capture"); t != "" {
			s.status("acting", "Taking a photo")
			r, err := s.call(t, nil)
			switch {
			case err != nil:
				reply = "I couldn't reach the camera: " + err.Error()
			case r.Denied:
				reply = "Okay, I won't take the photo."
			case !r.OK:
				reply = "The camera failed: " + r.Error
			default:
				reply = "Got it — photo taken."
			}
		}
	case strings.Contains(lower, "temp") || strings.Contains(lower, "climate") || strings.Contains(lower, "hot"):
		if t := tool("_read"); t != "" {
			s.status("acting", "Reading the sensor")
			r, err := s.call(t, nil)
			if err == nil && r.OK {
				m, _ := r.Data.(map[string]any)
				reply = fmt.Sprintf("It's %.1f °C with %.0f%% humidity.", m["temperature_c"], m["humidity_pct"])
			} else {
				reply = "I couldn't read the sensor."
			}
		}
	case hasWord(lower, "on") || hasWord(lower, "off"):
		if t := tool("_set"); t != "" {
			on := !hasWord(lower, "off")
			s.status("acting", "Switching "+strings.TrimSuffix(t, "_set"))
			r, err := s.call(t, json.RawMessage(fmt.Sprintf(`{"on":%t}`, on)))
			switch {
			case err != nil:
				reply = err.Error()
			case r.Denied:
				reply = "Understood, leaving it as it is."
			case r.OK:
				reply = "Done."
			default:
				reply = r.Error
			}
		}
	}
	if reply == "" {
		a, _ := s.roster.get(s.activeID())
		reply = "I'm " + a.Name + ", a simulated agent (" + strings.ToLower(a.Role) + "). Try “take a photo”, “what's the temperature?” or “turn the lamp on”."
	}
	s.status("speaking", "")
	s.send(proto.TypeSay, "", proto.Say{Text: reply})
	time.Sleep(600 * time.Millisecond)
	s.status("idle", "")
}

func (s *session) react(e proto.Event) {
	s.status("thinking", "Noticed "+e.Capability)
	time.Sleep(700 * time.Millisecond)
	text := fmt.Sprintf("I noticed %s: %s.", e.Capability, strings.ReplaceAll(e.Kind, "_", " "))
	if e.Kind == "pressed" {
		text = "You pressed " + e.Capability + ". What can I do for you?"
	}
	s.send(proto.TypeSay, "", proto.Say{Text: text})
	s.status("idle", "")
}

func hasWord(s, w string) bool {
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return !('a' <= r && r <= 'z') }) {
		if f == w {
			return true
		}
	}
	return false
}

func repl(s *session) {
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		s.command(sc.Text())
	}
}

func (s *session) command(line string) {
	cmd, rest, _ := strings.Cut(strings.TrimSpace(line), " ")
	switch cmd {
	case "":
	case "tools":
		for _, t := range s.hello.Tools {
			c := ""
			if t.Confirm != "" && t.Confirm != "none" {
				c = " [confirm: " + t.Confirm + "]"
			}
			fmt.Printf("  %s%s — %s\n", t.Name, c, t.Description)
		}
	case "call":
		name, args, _ := strings.Cut(rest, " ")
		var raw json.RawMessage
		if args != "" {
			raw = json.RawMessage(args)
		}
		go func() {
			r, err := s.call(name, raw)
			if err != nil {
				fmt.Println("  error:", err)
				return
			}
			b, _ := json.Marshal(r)
			if len(b) > 300 {
				b = append(b[:300], "…"...)
			}
			fmt.Println(" ", string(b))
		}()
	case "say":
		s.send(proto.TypeSay, "", proto.Say{Text: rest})
	case "status":
		st, detail, _ := strings.Cut(rest, " ")
		s.status(st, detail)
	case "approve", "deny":
		if !s.pairs.decide(strings.ToUpper(rest), cmd == "approve", "") {
			fmt.Println("  no pending pairing")
		}
	case "revoke":
		// What removing the device in the dashboard does.
		s.pairs.revokeAll()
		s.mu.Lock()
		if s.conn != nil {
			s.conn.Close()
		}
		s.mu.Unlock()
	case "kick":
		s.mu.Lock()
		if s.conn != nil {
			s.conn.Close()
		}
		s.mu.Unlock()
	default:
		fmt.Println("commands: tools | call <tool> [json] | say <text> | status <state> [detail] | tick | kick | approve [code] | deny [code] | revoke")
	}
}
