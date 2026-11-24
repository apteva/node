package node

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/apteva/node/internal/config"
	"github.com/apteva/node/internal/hw"
	"github.com/apteva/node/internal/link"
	"github.com/apteva/node/internal/netmgr"
	"github.com/apteva/node/internal/proto"
)

const testYAML = `
name: desk
safety:
  safe_state_after: 50ms
  confirm_timeout: 2s
capabilities:
  - id: lamp
    kind: relay
    pin: 17
    safe_state: off
  - id: door
    kind: relay
    pin: 27
    confirm: hold
  - id: call
    kind: button
    pin: 23
  - id: eye
    kind: camera
    width: 64
    height: 48
    confirm: screen
  - id: climate
    kind: i2c_sensor
    driver: bme280
    poll: 1h
    events:
      - field: temperature_c
        above: -100
`

// fakeServer is the server end of the link.
type fakeServer struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	conn   *websocket.Conn
	hello  chan proto.Hello
	frames chan proto.Frame
	held   []proto.Frame // read past while looking for another type
}

func newFakeServer(t *testing.T) *fakeServer {
	fs := &fakeServer{t: t, hello: make(chan proto.Hello, 4), frames: make(chan proto.Frame, 64)}
	up := websocket.Upgrader{}
	fs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != link.Path || r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		fs.mu.Lock()
		fs.conn = c
		fs.mu.Unlock()
		for {
			var f proto.Frame
			if err := c.ReadJSON(&f); err != nil {
				return
			}
			if f.Type == proto.TypeHello {
				h, _ := link.Decode[proto.Hello](f)
				fs.hello <- h
				continue
			}
			fs.frames <- f
		}
	}))
	t.Cleanup(fs.srv.Close)
	return fs
}

func (fs *fakeServer) send(typ, id string, data any) {
	f, _ := proto.New(typ, id, data)
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if err := fs.conn.WriteJSON(f); err != nil {
		fs.t.Fatal(err)
	}
}

func (fs *fakeServer) next(typ string) proto.Frame {
	fs.t.Helper()
	for i, f := range fs.held {
		if f.Type == typ {
			fs.held = append(fs.held[:i], fs.held[i+1:]...)
			return f
		}
	}
	timeout := time.After(3 * time.Second)
	for {
		select {
		case f := <-fs.frames:
			if f.Type == typ {
				return f
			}
			fs.held = append(fs.held, f)
		case <-timeout:
			fs.t.Fatalf("no %s frame", typ)
		}
	}
}

func (fs *fakeServer) result(id string) proto.Result {
	fs.t.Helper()
	for {
		f := fs.next(proto.TypeResult)
		if f.ID == id {
			r, _ := link.Decode[proto.Result](f)
			return r
		}
	}
}

func start(t *testing.T) (*Node, *fakeServer, *hw.Sim) {
	t.Helper()
	sim := hw.NewSim()
	n, fs := startWith(t, testYAML, sim)
	return n, fs, sim
}

func startWith(t *testing.T, yaml string, backend hw.Backend) (*Node, *fakeServer) {
	t.Helper()
	cfg, err := config.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	fs := newFakeServer(t)
	cfg.Server.URL, cfg.Server.Token = fs.srv.URL, "tok"
	cfg.StateDir = t.TempDir()
	n, err := New(cfg, backend, netmgr.NewSim(cfg.StateDir), "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go n.Run(ctx)
	return n, fs
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestHelloAnnouncesDeclaredToolsOnly(t *testing.T) {
	_, fs, _ := start(t)
	h := <-fs.hello
	got := map[string]string{}
	for _, tl := range h.Tools {
		got[tl.Name] = tl.Confirm
	}
	for _, want := range []string{"lamp_set", "lamp_get", "door_set", "call_get", "eye_capture", "climate_read", "system_info"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing tool %s", want)
		}
	}
	if got["door_set"] != "hold" || got["eye_capture"] != "screen" || got["lamp_set"] != "none" {
		t.Errorf("confirm modes not announced: %v", got)
	}
}

func TestInvokeWithoutConfirm(t *testing.T) {
	n, fs, _ := start(t)
	<-fs.hello
	fs.send(proto.TypeInvoke, "1", proto.Invoke{Tool: "lamp_set", Args: json.RawMessage(`{"on":true}`)})
	if r := fs.result("1"); !r.OK {
		t.Fatalf("lamp_set: %+v", r)
	}
	st, _ := link.Decode[proto.State](fs.next(proto.TypeState))
	if st.Capability != "lamp" {
		t.Errorf("state frame for %q", st.Capability)
	}
	for _, c := range n.Snapshot().Capabilities {
		if c.ID == "lamp" && (c.On == nil || !*c.On) {
			t.Error("lamp not on in snapshot")
		}
	}
}

func TestHoldConfirmNeedsAHold(t *testing.T) {
	n, fs, _ := start(t)
	<-fs.hello
	fs.send(proto.TypeInvoke, "2", proto.Invoke{Tool: "door_set", Args: json.RawMessage(`{"on":true}`)})
	waitFor(t, "approval", func() bool { return len(n.Snapshot().Approvals) == 1 })
	a := n.Snapshot().Approvals[0]
	if a.Summary != "Switch on door" {
		t.Errorf("summary %q", a.Summary)
	}
	if n.Resolve(a.ID, true, false) {
		t.Fatal("a tap must not approve a hold-mode action")
	}
	if !n.Resolve(a.ID, true, true) {
		t.Fatal("hold not accepted")
	}
	if r := fs.result("2"); !r.OK {
		t.Fatalf("door_set: %+v", r)
	}
}

func TestScreenDenyReachesAgent(t *testing.T) {
	n, fs, _ := start(t)
	<-fs.hello
	fs.send(proto.TypeInvoke, "3", proto.Invoke{Tool: "eye_capture"})
	waitFor(t, "approval", func() bool { return len(n.Snapshot().Approvals) == 1 })
	n.Resolve(n.Snapshot().Approvals[0].ID, false, false)
	if r := fs.result("3"); r.OK || !r.Denied {
		t.Fatalf("expected denied, got %+v", r)
	}
	if _, ok := n.Photo("eye"); ok {
		t.Error("photo taken despite denial")
	}
}

func TestButtonEventAndMessage(t *testing.T) {
	n, fs, _ := start(t)
	<-fs.hello
	n.Press("call", true)
	for {
		e, _ := link.Decode[proto.Event](fs.next(proto.TypeEvent))
		if e.Capability == "climate" {
			continue // startup threshold crossing
		}
		if e.Capability != "call" || e.Kind != "pressed" {
			t.Errorf("event %+v", e)
		}
		break
	}
	n.SendMessage("hello")
	m, _ := link.Decode[proto.Message](fs.next(proto.TypeMessage))
	if m.Text != "hello" {
		t.Errorf("message %q", m.Text)
	}
	fs.send(proto.TypeSay, "", proto.Say{Text: "hi there"})
	waitFor(t, "agent line", func() bool {
		c := n.Snapshot().Conversation
		return len(c) > 0 && c[len(c)-1].From == "agent"
	})
}

func TestSafeStateAfterLinkLoss(t *testing.T) {
	n, fs, _ := start(t)
	<-fs.hello
	n.Toggle("lamp") // on, locally
	waitFor(t, "lamp on", func() bool { return lampOn(n) })
	fs.mu.Lock()
	fs.conn.Close()
	fs.mu.Unlock()
	waitFor(t, "safe state", func() bool {
		for _, a := range n.Snapshot().Actions {
			if a.By == "safety" {
				return !lampOn(n)
			}
		}
		return false
	})
	// The node reconnects on its own.
	select {
	case <-fs.hello:
	case <-time.After(5 * time.Second):
		t.Fatal("no reconnect")
	}
}

func TestThresholdFiresOnceUntilRearmed(t *testing.T) {
	n, fs, _ := start(t)
	<-fs.hello
	// Poll at start already crossed "above -100".
	e, _ := link.Decode[proto.Event](fs.next(proto.TypeEvent))
	if e.Kind != "temperature_c_above" {
		t.Fatalf("event %+v", e)
	}
	var c *capability
	for _, cc := range n.caps {
		if cc.cfg.ID == "climate" {
			c = cc
		}
	}
	n.readSensor(c)
	select {
	case f := <-fs.frames:
		if f.Type == proto.TypeEvent {
			t.Fatal("threshold fired twice without re-arming")
		}
	case <-time.After(50 * time.Millisecond):
	}
}

func lampOn(n *Node) bool {
	for _, c := range n.Snapshot().Capabilities {
		if c.ID == "lamp" {
			return c.On != nil && *c.On
		}
	}
	return false
}

const testSurface = `{"schema":"apteva-native-surface/v1","id":"task-overview","version":"1.0.0","title":"Tasks","icon":"list-checks","context":{"scope":"project"},"presentation":"widget","data_sources":{"summary":{"request":{"method":"GET","path":"/mobile/summary"},"response":{"item":"$"}}},"blocks":[{"id":"counts","type":"metric","source":"summary","value":"$.counts.active"}]}`

func TestWidgetsRequestsAndRefresh(t *testing.T) {
	n, fs, _ := start(t)
	<-fs.hello
	fs.send(proto.TypeWidgets, "", proto.Widgets{Widgets: []proto.Widget{
		{ID: "w1", App: "tasks", Component: "task-overview", RefreshTopics: []string{"task.updated"}, Surface: json.RawMessage(testSurface)},
		{ID: "bad", App: "x", Surface: json.RawMessage(`{"schema":"other/v9","presentation":"widget"}`)},
	}})
	waitFor(t, "widgets", func() bool { return len(n.Snapshot().Widgets) == 1 })
	if w := n.Snapshot().Widgets[0]; w.Size != "half" || w.Rev != 0 {
		t.Fatalf("widget %+v", w)
	}

	if _, err := n.AppRequest(context.Background(), proto.AppRequest{App: "billing", Path: "/x"}); err == nil {
		t.Fatal("request to an app without a widget must be refused")
	}
	if _, err := n.AppRequest(context.Background(), proto.AppRequest{App: "tasks", Path: "/../admin"}); err == nil {
		t.Fatal("path traversal must be refused")
	}

	got := make(chan AppResult, 1)
	go func() {
		r, err := n.AppRequest(context.Background(), proto.AppRequest{App: "tasks", Method: "get", Path: "/mobile/summary", Query: map[string]any{"recent_limit": 4}})
		if err != nil {
			t.Error(err)
		}
		got <- r
	}()
	f := fs.next(proto.TypeAppRequest)
	req, _ := link.Decode[proto.AppRequest](f)
	if req.App != "tasks" || req.Method != "GET" || req.Path != "/mobile/summary" {
		t.Fatalf("relayed %+v", req)
	}
	fs.send(proto.TypeAppResponse, f.ID, proto.AppResponse{Status: 200, Body: json.RawMessage(`{"counts":{"active":3}}`)})
	if r := <-got; r.Status != 200 || r.Stale || string(r.Body) != `{"counts":{"active":3}}` {
		t.Fatalf("result %+v", r)
	}

	fs.send(proto.TypeAppEvent, "", proto.AppEvent{App: "tasks", Topic: "task.updated"})
	waitFor(t, "rev bump", func() bool { return n.Snapshot().Widgets[0].Rev == 1 })
	fs.send(proto.TypeAppEvent, "", proto.AppEvent{App: "tasks", Topic: "unrelated"})

	// Offline: the last good read is served, marked stale.
	fs.mu.Lock()
	fs.conn.Close()
	fs.mu.Unlock()
	waitFor(t, "offline", func() bool { return n.Snapshot().Link.State != link.StateConnected })
	r, err := n.AppRequest(context.Background(), proto.AppRequest{App: "tasks", Path: "/mobile/summary", Query: map[string]any{"recent_limit": 4}})
	if err != nil || !r.Stale || r.Status != 200 {
		t.Fatalf("offline read: %+v %v", r, err)
	}
	if _, err := n.AppRequest(context.Background(), proto.AppRequest{App: "tasks", Method: "POST", Path: "/tasks"}); err == nil {
		t.Fatal("writes must not be served from cache")
	}
	if n.Snapshot().Widgets[0].Rev != 1 {
		t.Fatal("unrelated topic bumped the widget")
	}
}

func TestAgentsListAndSwitch(t *testing.T) {
	n, fs, _ := start(t)
	<-fs.hello
	if err := n.SelectAgent("juno"); err == nil {
		t.Fatal("selected an agent the server never listed")
	}
	fs.send(proto.TypeWelcome, "", proto.Welcome{AgentID: "atlas", Agent: "Atlas", Icon: "assistant"})
	fs.send(proto.TypeAgents, "", proto.Agents{Agents: []proto.AgentInfo{
		{ID: "atlas", Name: "Atlas", Icon: "assistant", State: "idle"},
		{ID: "juno", Name: "Juno", Icon: "research", State: "thinking", Detail: "Reading"},
	}})
	waitFor(t, "agents", func() bool { return len(n.Snapshot().Agents) == 2 })
	if err := n.SelectAgent("juno"); err != nil {
		t.Fatal(err)
	}
	sel, _ := link.Decode[proto.Select](fs.next(proto.TypeSelect))
	if sel.AgentID != "juno" {
		t.Fatalf("selected %q", sel.AgentID)
	}
	fs.send(proto.TypeWelcome, "", proto.Welcome{AgentID: "juno", Agent: "Juno", Icon: "research"})
	waitFor(t, "switched", func() bool { return n.Snapshot().Agent.ID == "juno" })
	lines := n.Snapshot().Conversation
	if len(lines) == 0 || lines[len(lines)-1].Text != "Now talking to Juno" {
		t.Fatalf("no switch notice: %+v", lines)
	}
}

func TestRevocationCloseCancelsLocalConfirmationImmediately(t *testing.T) {
	n, fs, _ := start(t)
	<-fs.hello
	fs.send(proto.TypeInvoke, "revoked-call", proto.Invoke{Tool: "door_set", Args: json.RawMessage(`{"on":true}`)})
	waitFor(t, "pending local approval", func() bool { return len(n.Snapshot().Approvals) == 1 })
	approvalID := n.Snapshot().Approvals[0].ID
	fs.mu.Lock()
	err := fs.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(4003, "device revoked"), time.Now().Add(time.Second))
	fs.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "standalone after immediate revoke", func() bool { return n.Mode() == ModeStandalone })
	waitFor(t, "cancelled hardware approval", func() bool { return len(n.Snapshot().Approvals) == 0 })
	if n.Resolve(approvalID, true, true) {
		t.Fatal("revoked approval remained actionable")
	}
	for _, cap := range n.Snapshot().Capabilities {
		if cap.ID == "door" && cap.On != nil && *cap.On {
			t.Fatal("hardware changed after revocation")
		}
	}
}

func TestDisconnectedLinkCancelsPendingHardwareConfirmation(t *testing.T) {
	n, fs, _ := start(t)
	<-fs.hello
	fs.send(proto.TypeInvoke, "lost-call", proto.Invoke{Tool: "door_set", Args: json.RawMessage(`{"on":true}`)})
	waitFor(t, "pending local approval", func() bool { return len(n.Snapshot().Approvals) == 1 })
	fs.mu.Lock()
	fs.conn.Close()
	fs.mu.Unlock()
	waitFor(t, "cancelled approval on disconnect", func() bool { return len(n.Snapshot().Approvals) == 0 })
}

func TestCancelledInvocationRemovesOnlyItsLocalApproval(t *testing.T) {
	n, fs, _ := start(t)
	<-fs.hello
	fs.send(proto.TypeInvoke, "cancelled-call", proto.Invoke{Tool: "door_set", Args: json.RawMessage(`{"on":true}`)})
	waitFor(t, "pending confirmation", func() bool { return len(n.Snapshot().Approvals) == 1 })
	fs.send("cancel_invoke", "cancelled-call", map[string]string{"reason": "request expired"})
	waitFor(t, "cancelled confirmation", func() bool { return len(n.Snapshot().Approvals) == 0 })
	if n.linkStatus().State != link.StateConnected {
		t.Fatal("cancellation disconnected the device")
	}
}

func TestAccountProjectSelectionWaitsForServerConfirmation(t *testing.T) {
	n, fs, _ := start(t)
	<-fs.hello
	fs.send("projects", "", proto.Projects{Projects: []proto.ProjectInfo{{ID: "home", Name: "Home"}, {ID: "work", Name: "Work"}}})
	waitFor(t, "account projects", func() bool { return len(n.Snapshot().Projects) == 2 })
	if err := n.SelectProject("foreign"); err == nil {
		t.Fatal("unknown project selected")
	}
	if err := n.SelectProject("work"); err != nil {
		t.Fatal(err)
	}
	selected := fs.next("select_project")
	var payload struct {
		ProjectID string `json:"project_id"`
	}
	if err := json.Unmarshal(selected.Data, &payload); err != nil || payload.ProjectID != "work" {
		t.Fatal("wrong project request")
	}
	if n.Snapshot().Agent.ProjectID == "work" {
		t.Fatal("context changed before server confirmation")
	}
	fs.send(proto.TypeWelcome, "", proto.Welcome{ProjectID: "work"})
	waitFor(t, "confirmed project", func() bool { return n.Snapshot().Agent.ProjectID == "work" })
}
