// Package node is the daemon's core: it binds declared capabilities to
// hardware, answers the agent's tool calls through the guard, forwards
// events and conversation over the link, and keeps the snapshot the
// on-device UI renders.
package node

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/apteva/node/internal/config"
	"github.com/apteva/node/internal/guard"
	"github.com/apteva/node/internal/hw"
	"github.com/apteva/node/internal/link"
	"github.com/apteva/node/internal/netmgr"
	"github.com/apteva/node/internal/proto"
	"github.com/apteva/node/internal/state"
)

const (
	conversationLimit = 60
	actionLimit       = 30
)

type Node struct {
	cfg     *config.Config
	version string
	hw      hw.Backend
	guard   *guard.Guard
	log     *slog.Logger
	store   *state.Store
	events  *state.EventOutbox
	net     netmgr.Manager
	setup   *setupState

	// The link exists only while the device is paired; it is replaced when
	// the device pairs with another server and dropped in standalone mode.
	linkMu        sync.Mutex
	link          *link.Client
	linkCancel    context.CancelFunc
	invokeCtx     context.Context
	invokeCancel  context.CancelFunc
	invokeCalls   map[string]context.CancelFunc
	runCtx        context.Context
	everConnected bool // since this link started; safe state needs a lost link, not a missing one

	camMu   sync.Mutex // one capture (or camera check) at a time
	caps    []*capability
	tools   map[string]*tool
	widgets *widgetStore

	mu           sync.Mutex
	agent        AgentView
	projects     []proto.ProjectInfo
	agents       []proto.AgentInfo // the project's agents, last list from the server
	conversation []Line
	actions      []Action
	lastSent     *SentPhoto
	safeTimer    *time.Timer
	safeApplied  bool

	subMu sync.Mutex
	subs  map[chan struct{}]struct{}
	stop  chan struct{}
}

type AgentView struct {
	ProjectID string    `json:"project_id,omitempty"`
	ID        string    `json:"id,omitempty"`
	Name      string    `json:"name,omitempty"`
	Icon      string    `json:"icon,omitempty"`
	State     string    `json:"state"`
	Detail    string    `json:"detail,omitempty"`
	Since     time.Time `json:"since"`
}

type Line struct {
	ID   string    `json:"id"`
	From string    `json:"from"` // you | agent | system
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

type Action struct {
	At      time.Time `json:"at"`
	By      string    `json:"by"` // agent | local | safety
	Tool    string    `json:"tool"`
	OK      bool      `json:"ok"`
	Summary string    `json:"summary"`
}

func New(cfg *config.Config, backend hw.Backend, nm netmgr.Manager, version string, log *slog.Logger) (*Node, error) {
	store, err := state.Open(cfg.StateDir)
	if err != nil {
		return nil, fmt.Errorf("state: %w", err)
	}
	events, err := state.OpenEventOutbox(cfg.StateDir)
	if err != nil {
		return nil, fmt.Errorf("event outbox: %w", err)
	}
	n := &Node{
		cfg: cfg, version: version, hw: backend, log: log, store: store, events: events, net: nm,
		setup:   newSetupState(),
		tools:   map[string]*tool{},
		widgets: newWidgetStore(),
		agent:   AgentView{State: "idle", Since: time.Now()},
		subs:    map[chan struct{}]struct{}{},
		stop:    make(chan struct{}),
	}
	n.guard = guard.New(cfg.Safety.ConfirmTimeout.Duration, n.changed)

	for _, cc := range cfg.Capabilities {
		c := &capability{cfg: cc, armed: map[int]bool{}}
		var err error
		switch cc.Kind {
		case config.KindRelay, config.KindLED:
			c.out, err = backend.Output(cc.Pin, cc.ActiveLow)
		case config.KindButton:
			c.in, err = backend.Input(cc.Pin, cc.ActiveLow, func(down bool) { n.onButton(c, down) })
		case config.KindSensor:
			c.sensor, err = backend.Sensor(cc.Driver, cfg.Hardware.I2CBus, cc.Address)
		}
		if err != nil {
			return nil, fmt.Errorf("capability %q: %w", cc.ID, err)
		}
		n.caps = append(n.caps, c)
		for _, t := range n.toolsFor(c) {
			t.spec.ReadOnly = !t.mutates
			if t.mutates {
				t.spec.Confirm = cc.Confirm
			}
			n.tools[t.spec.Name] = t
		}
	}
	st := n.systemTool()
	st.spec.ReadOnly = true
	n.tools[st.spec.Name] = st

	if _, err := cfg.ServerToken(); err != nil {
		return nil, fmt.Errorf("server token: %w", err)
	}
	return n, nil
}

func (n *Node) Run(ctx context.Context) {
	go n.runEventOutbox(ctx)
	go n.detectCameras()
	for _, c := range n.caps {
		if c.sensor != nil {
			go n.pollSensor(c, n.stop)
		}
	}
	n.linkMu.Lock()
	n.runCtx = ctx
	n.linkMu.Unlock()
	go n.pollNetwork(ctx)
	if url, token := n.connection(); token != "" {
		n.startLink(url, token)
	}
	<-ctx.Done()
	n.stopLink()
	n.CancelPairing()
	close(n.stop)
}

// startLink (re)connects to an Apteva instance with a device token.
func (n *Node) startLink(url, token string) {
	n.linkMu.Lock()
	defer n.linkMu.Unlock()
	if n.invokeCancel != nil {
		n.invokeCancel()
		n.invokeCtx, n.invokeCancel = nil, nil
	}
	if n.linkCancel != nil {
		n.linkCancel()
	}
	if n.runCtx == nil {
		return // not running yet; Run starts it
	}
	c := &link.Client{ServerURL: url, Token: token, Hello: n.hello, OnFrame: n.onFrame}
	c.OnStatus = func(s link.Status) { n.onLinkStatus(c, s) }
	ctx, cancel := context.WithCancel(n.runCtx)
	n.link, n.linkCancel, n.everConnected = c, cancel, false
	go c.Run(ctx)
}

func (n *Node) stopLink() {
	n.linkMu.Lock()
	if n.linkCancel != nil {
		n.linkCancel()
	}
	if n.invokeCancel != nil {
		n.invokeCancel()
		n.invokeCancel, n.invokeCtx = nil, nil
	}
	n.link, n.linkCancel, n.everConnected = nil, nil, false
	n.linkMu.Unlock()
	n.mu.Lock()
	if n.safeTimer != nil {
		n.safeTimer.Stop()
		n.safeTimer = nil
	}
	n.safeApplied = false
	n.mu.Unlock()
	n.changed()
}

func (n *Node) currentLink() *link.Client {
	n.linkMu.Lock()
	defer n.linkMu.Unlock()
	return n.link
}

// linkStatus is the link's status, or what the device is doing instead of
// linking: still in setup, or standalone by choice.
func (n *Node) linkStatus() link.Status {
	if c := n.currentLink(); c != nil {
		return c.Status()
	}
	if n.Mode() == ModeSetup {
		return link.Status{State: link.StateUnpaired}
	}
	return link.Status{State: link.StateStandalone}
}

func (n *Node) hello() proto.Hello {
	h := proto.Hello{Protocol: proto.Version, Name: n.cfg.Name, Version: n.version,
		Renders: []string{proto.NativeSurfaceSchema + "#widget"}}
	for _, c := range n.caps {
		for _, t := range n.toolsFor(c) {
			h.Tools = append(h.Tools, n.tools[t.spec.Name].spec)
		}
		switch c.cfg.Kind {
		case config.KindButton:
			h.Events = append(h.Events, proto.EventSpec{Capability: c.cfg.ID, Kind: "pressed", Description: describe(c.cfg, "button") + " was pressed"})
		case config.KindSensor:
			for _, th := range c.cfg.Events {
				kind, limit := "above", th.Above
				if th.Below != nil {
					kind, limit = "below", th.Below
				}
				h.Events = append(h.Events, proto.EventSpec{Capability: c.cfg.ID, Kind: th.Field + "_" + kind,
					Description: fmt.Sprintf("%s went %s %g", th.Field, kind, *limit)})
			}
		}
	}
	h.Tools = append(h.Tools, n.tools["system_info"].spec)
	return h
}

// --- link inbound ---

func (n *Node) onFrame(f proto.Frame) {
	switch f.Type {
	case "ack":
		var ack struct {
			Accepted bool `json:"accepted"`
		}
		if json.Unmarshal(f.Data, &ack) == nil && ack.Accepted {
			n.ackEvent(f.ID)
		}
	case proto.TypeWelcome:
		w, _ := link.Decode[proto.Welcome](f)
		n.mu.Lock()
		switched := n.agent.ID != "" && w.AgentID != "" && n.agent.ID != w.AgentID
		n.agent.ID, n.agent.Name, n.agent.Icon = w.AgentID, w.Agent, w.Icon
		n.agent.ProjectID = w.ProjectID
		n.mu.Unlock()
		if switched {
			n.addLine("system", "Now talking to "+w.Agent)
		}
		n.changed()
	case "projects":
		var projects proto.Projects
		if json.Unmarshal(f.Data, &projects) != nil {
			return
		}
		n.mu.Lock()
		n.projects = projects.Projects
		n.mu.Unlock()
		n.changed()
	case proto.TypeAgents:
		a, err := link.Decode[proto.Agents](f)
		if err != nil {
			n.log.Warn("bad agents frame", "err", err)
			return
		}
		n.mu.Lock()
		n.agents = a.Agents
		n.mu.Unlock()
		n.changed()
	case proto.TypeInvoke:
		inv, err := link.Decode[proto.Invoke](f)
		if err != nil {
			n.reply(f.ID, proto.Result{Error: "bad invoke: " + err.Error()})
			return
		}
		n.linkMu.Lock()
		ctx := n.invokeCtx
		n.linkMu.Unlock()
		if ctx == nil {
			return
		}
		callCtx, cancel := context.WithCancel(ctx)
		n.linkMu.Lock()
		if n.invokeCalls == nil {
			n.invokeCalls = map[string]context.CancelFunc{}
		}
		if _, exists := n.invokeCalls[f.ID]; exists {
			n.linkMu.Unlock()
			cancel()
			return
		}
		n.invokeCalls[f.ID] = cancel
		n.linkMu.Unlock()
		go func() {
			defer cancel()
			defer func() { n.linkMu.Lock(); delete(n.invokeCalls, f.ID); n.linkMu.Unlock() }()
			n.invokeContext(callCtx, f.ID, inv)
		}()
	case "cancel_invoke":
		n.linkMu.Lock()
		cancel := n.invokeCalls[f.ID]
		n.linkMu.Unlock()
		if cancel != nil {
			cancel()
		}
	case proto.TypeSay:
		s, _ := link.Decode[proto.Say](f)
		if s.Text != "" {
			n.addLine("agent", s.Text)
		}
	case "error":
		var problem struct {
			Error     string
			Permanent bool
		}
		if json.Unmarshal(f.Data, &problem) == nil && problem.Error != "" {
			n.addLine("system", problem.Error)
			if problem.Permanent {
				n.ackEvent(f.ID)
			}
		}
	case proto.TypeStatus:
		s, _ := link.Decode[proto.Status](f)
		n.mu.Lock()
		if n.agent.State != s.State {
			n.agent.Since = time.Now()
		}
		n.agent.State, n.agent.Detail = s.State, s.Detail
		n.mu.Unlock()
		n.changed()
	case proto.TypeWidgets:
		ws, err := link.Decode[proto.Widgets](f)
		if err != nil {
			n.log.Warn("bad widgets frame", "err", err)
			return
		}
		n.setWidgets(ws.Widgets)
	case proto.TypeAppResponse:
		r, _ := link.Decode[proto.AppResponse](f)
		n.onAppResponse(f.ID, r)
	case proto.TypeAppEvent:
		ev, _ := link.Decode[proto.AppEvent](f)
		n.onAppEvent(ev)
	default:
		n.log.Debug("ignoring frame", "type", f.Type)
	}
}

func (n *Node) invokeContext(ctx context.Context, id string, inv proto.Invoke) {
	if ctx.Err() != nil {
		return
	}
	t := n.tools[inv.Tool]
	if t == nil {
		n.reply(id, proto.Result{Error: "unknown tool " + inv.Tool})
		return
	}
	args := inv.Args
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	if t.mutates {
		out := n.guard.Request(ctx, t.cap.cfg.ID, t.spec.Name, summarize(t, args), t.cap.cfg.Confirm)
		if !out.Allowed {
			if ctx.Err() != nil {
				return
			}
			n.record(Action{By: "agent", Tool: t.spec.Name, Summary: "denied (" + out.Via + ")"})
			n.reply(id, proto.Result{Denied: true, Error: "the person at the device did not approve (" + out.Via + ")"})
			return
		}
	}
	if ctx.Err() != nil {
		return
	}
	data, err := t.run(args)
	a := Action{By: "agent", Tool: t.spec.Name, OK: err == nil, Summary: summarize(t, args)}
	if err != nil {
		a.Summary += ": " + err.Error()
	}
	n.record(a)
	if err != nil {
		n.reply(id, proto.Result{Error: err.Error()})
		return
	}
	n.reply(id, proto.Result{OK: true, Data: data})
}

// summarize turns a tool call into a sentence a person can approve.
func summarize(t *tool, args json.RawMessage) string {
	if t.cap == nil {
		return t.spec.Name
	}
	name := t.cap.cfg.ID
	if t.cap.cfg.Description != "" {
		name = t.cap.cfg.Description
	}
	switch t.cap.cfg.Kind {
	case config.KindRelay, config.KindLED:
		if len(t.spec.Name) > 4 && t.spec.Name[len(t.spec.Name)-4:] == "_set" {
			var a struct{ On bool }
			json.Unmarshal(args, &a)
			if a.On {
				return "Switch on " + name
			}
			return "Switch off " + name
		}
		return "Check " + name
	case config.KindCamera:
		var a struct{ Width, Height int }
		json.Unmarshal(args, &a)
		if a.Width > 0 || a.Height > 0 {
			w, h := photoSize(t.cap.cfg, a.Width, a.Height)
			return fmt.Sprintf("Take a %d×%d photo with %s", w, h, name)
		}
		return "Take a photo with " + name
	case config.KindSensor:
		return "Read " + name
	}
	return t.spec.Name
}

func (n *Node) reply(id string, r proto.Result) { n.sendFrame(proto.TypeResult, id, r) }

func (n *Node) sendFrame(typ, id string, data any) {
	f, err := proto.New(typ, id, data)
	if err != nil {
		n.log.Error("encode frame", "type", typ, "err", err)
		return
	}
	if c := n.currentLink(); c != nil {
		c.Send(f)
	}
	// No link (setup or standalone): nothing to deliver to, nothing to queue.
}

func (n *Node) emit(capID, kind string, data any) {
	url, token := n.connection()
	if token == "" {
		n.record(Action{By: "local", Tool: capID, OK: true, Summary: "event " + kind + " (standalone)"})
		return
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		n.log.Error("event ID failed", "err", err)
		return
	}
	id := hex.EncodeToString(idBytes)
	now := time.Now()
	f, err := proto.New(proto.TypeEvent, id, proto.Event{ID: id, Capability: capID, Kind: kind, Data: data, At: now})
	if err == nil {
		err = n.events.Add(state.EventScope(url, token), f, now)
	}
	summary := "event " + kind + " queued"
	if err != nil {
		summary = "event could not be saved: " + err.Error()
		n.log.Error(summary)
	}
	n.record(Action{By: "local", Tool: capID, OK: err == nil, Summary: summary})
}

func (n *Node) ackEvent(id string) {
	url, token := n.connection()
	if err := n.events.Remove(state.EventScope(url, token), id); err != nil {
		n.record(Action{By: "local", Tool: "events", Summary: "event acknowledgement could not be saved: " + err.Error()})
	}
	n.changed()
}
func (n *Node) runEventOutbox(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		url, token := n.connection()
		scope := state.EventScope(url, token)
		if scope == "" {
			continue
		} // A temporarily unreadable provisioned token must not erase the queue.
		dropped, err := n.events.Prune(scope, time.Now())
		if err != nil {
			n.log.Error("event queue cleanup failed", "err", err)
			continue
		}
		if dropped > 0 {
			n.record(Action{By: "local", Tool: "events", Summary: fmt.Sprintf("%d queued events expired or belonged to a previous pairing", dropped)})
		}
		if c := n.currentLink(); c != nil && state.EventScope(c.ServerURL, c.Token) == scope {
			for _, e := range n.events.Pending(scope) {
				if !c.SendLive(e.Frame) {
					break
				}
			}
		}
	}
}

// --- link status and safe state ---

func (n *Node) onLinkStatus(c *link.Client, s link.Status) {
	n.linkMu.Lock()
	if c != n.link {
		n.linkMu.Unlock()
		return // a link we already replaced or stopped
	}
	if s.State == link.StateConnected {
		n.everConnected = true
		if n.invokeCancel != nil {
			n.invokeCancel()
		}
		n.invokeCtx, n.invokeCancel = context.WithCancel(n.runCtx)
	} else if n.invokeCancel != nil {
		n.invokeCancel()
		n.invokeCtx, n.invokeCancel = nil, nil
	}
	lost := n.everConnected && s.State != link.StateConnected
	n.linkMu.Unlock()

	if s.State == link.StateRejected {
		go n.onRevoked()
		return
	}

	n.mu.Lock()
	switch {
	case s.State == link.StateConnected:
		if n.safeTimer != nil {
			n.safeTimer.Stop()
			n.safeTimer = nil
		}
		n.safeApplied = false
	case lost && n.safeTimer == nil && !n.safeApplied:
		// Only a link that worked and then dropped arms the safe state. A
		// device that never reached its server boots with outputs off anyway,
		// and must not switch off what someone just turned on by hand.
		n.safeTimer = time.AfterFunc(n.cfg.Safety.SafeStateAfter.Duration, n.applySafeState)
	}
	n.mu.Unlock()
	n.changed()
}

// applySafeState drives outputs that declare a safe_state once the server
// has been unreachable for safety.safe_state_after.
func (n *Node) applySafeState() {
	n.mu.Lock()
	n.safeTimer = nil
	n.safeApplied = true
	n.mu.Unlock()
	for _, c := range n.caps {
		if c.cfg.SafeState == "" || c.out == nil {
			continue
		}
		want := c.cfg.SafeState == "on"
		if c.out.Get() == want {
			continue
		}
		_, err := n.setOutput(c, want, "safety")
		n.record(Action{By: "safety", Tool: c.cfg.ID + "_set", OK: err == nil, Summary: "link lost: " + c.cfg.ID + " → " + c.cfg.SafeState})
	}
}

// --- local inputs ---

func (n *Node) onButton(c *capability, down bool) {
	c.mu.Lock()
	c.pressed = down
	c.mu.Unlock()
	n.changed()
	if !down {
		return
	}
	n.emit(c.cfg.ID, "pressed", nil)
}

// SelectProject changes context only after the server confirms it in welcome.
func (n *Node) SelectProject(id string) error {
	if n.linkStatus().State != link.StateConnected {
		return errors.New("not connected to Apteva")
	}
	n.mu.Lock()
	known := id == ""
	for _, project := range n.projects {
		known = known || project.ID == id
	}
	n.mu.Unlock()
	if !known {
		return errors.New("unknown project")
	}
	n.sendFrame("select_project", "", map[string]string{"project_id": id})
	return nil
}

// SelectAgent asks the server to bind this device to another agent.
func (n *Node) SelectAgent(id string) error {
	if n.linkStatus().State != link.StateConnected {
		return errors.New("not connected to Apteva")
	}
	n.mu.Lock()
	known := false
	for _, a := range n.agents {
		known = known || a.ID == id
	}
	n.mu.Unlock()
	if !known {
		return fmt.Errorf("unknown agent %q", id)
	}
	n.sendFrame(proto.TypeSelect, "", proto.Select{AgentID: id})
	return nil
}

// SendMessage is the person at the device talking to the agent.
func (n *Node) SendMessage(text string) {
	if text == "" {
		return
	}
	n.addLine("you", text)
	if n.currentLink() == nil {
		n.addLine("system", "This device isn't connected to Apteva, so there's no agent to answer.")
		return
	}
	n.sendFrame(proto.TypeMessage, "", proto.Message{Text: text, At: time.Now()})
	if st := n.linkStatus().State; st != link.StateConnected {
		n.addLine("system", "Not connected — your message will be sent when the link is back.")
	}
}

func (n *Node) Resolve(id string, allow, held bool) bool { return n.guard.Resolve(id, allow, held) }

// Toggle is a local, hands-on switch from the Hardware screen. The person
// standing at the device doesn't need the agent's confirm policy.
func (n *Node) Toggle(capID string) error {
	for _, c := range n.caps {
		if c.cfg.ID == capID && c.out != nil {
			on := !c.out.Get()
			_, err := n.setOutput(c, on, "local")
			n.record(Action{By: "local", Tool: capID + "_set", OK: err == nil, Summary: fmt.Sprintf("%s %s from screen", capID, onOff(on))})
			return err
		}
	}
	return fmt.Errorf("no output %q", capID)
}

// Press drives a simulated button; only works on the sim backend.
func (n *Node) Press(capID string, down bool) error {
	sim, ok := n.hw.(hw.Simulator)
	if !ok {
		return fmt.Errorf("not running on the simulator")
	}
	for _, c := range n.caps {
		if c.cfg.ID == capID && c.cfg.Kind == config.KindButton {
			sim.Press(c.cfg.Pin, down)
			return nil
		}
	}
	return fmt.Errorf("no button %q", capID)
}

func (n *Node) Photo(capID string) ([]byte, bool) {
	for _, c := range n.caps {
		if c.cfg.ID == capID {
			c.mu.Lock()
			defer c.mu.Unlock()
			return c.lastPhoto, c.lastPhoto != nil
		}
	}
	return nil, false
}

// --- bookkeeping ---

func (n *Node) addLine(from, text string) {
	n.mu.Lock()
	n.conversation = append(n.conversation, Line{ID: shortID(), From: from, Text: text, At: time.Now()})
	if len(n.conversation) > conversationLimit {
		n.conversation = n.conversation[len(n.conversation)-conversationLimit:]
	}
	n.mu.Unlock()
	n.changed()
}

func (n *Node) record(a Action) {
	a.At = time.Now()
	n.mu.Lock()
	n.actions = append(n.actions, a)
	if len(n.actions) > actionLimit {
		n.actions = n.actions[len(n.actions)-actionLimit:]
	}
	n.mu.Unlock()
	n.changed()
}

type Snapshot struct {
	Node struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		Backend string `json:"backend"`
		Sim     bool   `json:"sim"`
	} `json:"node"`
	Link         link.Status         `json:"link"`
	Setup        SetupView           `json:"setup"`
	Queued       int                 `json:"queued"`
	Agent        AgentView           `json:"agent"`
	Conversation []Line              `json:"conversation"`
	Approvals    []guard.Approval    `json:"approvals"`
	Capabilities []capView           `json:"capabilities"`
	Actions      []Action            `json:"actions"`
	Widgets      []WidgetView        `json:"widgets"`
	Projects     []proto.ProjectInfo `json:"projects"`
	Agents       []proto.AgentInfo   `json:"agents"`
	SentPhoto    *SentPhoto          `json:"sent_photo,omitempty"`
	System       hw.SystemInfo       `json:"system"`
}

func (n *Node) Snapshot() Snapshot {
	var s Snapshot
	s.Node.Name, s.Node.Version, s.Node.Backend = n.cfg.Name, n.version, n.hw.Name()
	_, s.Node.Sim = n.hw.(hw.Simulator)
	s.Link = n.linkStatus()
	if c := n.currentLink(); c != nil {
		s.Queued = c.Queued()
	}
	url, token := n.connection()
	s.Queued += len(n.events.Pending(state.EventScope(url, token)))
	s.Setup = n.setupView()
	s.Approvals = n.guard.Pending()
	s.System = n.hw.System()
	s.Widgets = n.widgetViews()
	for _, c := range n.caps {
		s.Capabilities = append(s.Capabilities, c.view())
	}
	n.mu.Lock()
	s.Agent = n.agent
	s.Agents = append([]proto.AgentInfo{}, n.agents...)
	s.Projects = append([]proto.ProjectInfo{}, n.projects...)
	s.Conversation = append([]Line{}, n.conversation...)
	s.Actions = append([]Action{}, n.actions...)
	s.SentPhoto = n.lastSent
	n.mu.Unlock()
	return s
}

// Subscribe returns a channel that ticks (coalesced) whenever the snapshot
// may have changed.
func (n *Node) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	n.subMu.Lock()
	n.subs[ch] = struct{}{}
	n.subMu.Unlock()
	return ch, func() {
		n.subMu.Lock()
		delete(n.subs, ch)
		n.subMu.Unlock()
	}
}

func (n *Node) changed() {
	n.subMu.Lock()
	defer n.subMu.Unlock()
	for ch := range n.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

func shortID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}
