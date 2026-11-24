package node

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/apteva/node/internal/config"
	"github.com/apteva/node/internal/netmgr"
	"github.com/apteva/node/internal/pairing"
	"github.com/apteva/node/internal/state"
)

// Modes. Setup runs until the person finishes it; after that the device is
// either paired with an Apteva instance or deliberately standalone.
const (
	ModeSetup      = "setup"
	ModeStandalone = "standalone"
	ModeConnected  = "connected"
)

const (
	unlockFor       = 5 * time.Minute
	pinMaxFails     = 5
	pinLockout      = time.Minute
	netPollEvery    = 10 * time.Second
	pairingFallback = 10 * time.Minute
)

type setupState struct {
	mu        sync.Mutex
	pairing   PairingView
	cancel    context.CancelFunc
	notice    string
	net       netmgr.Status
	unlocks   map[string]time.Time
	fails     int
	lockedTil time.Time
}

func newSetupState() *setupState { return &setupState{unlocks: map[string]time.Time{}} }

type PairingView struct {
	Status    string    `json:"status"` // idle | waiting | approved | denied | expired | error
	Code      string    `json:"code,omitempty"`
	VerifyURL string    `json:"verify_url,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitzero"`
	Error     string    `json:"error,omitempty"`
	Agent     string    `json:"agent,omitempty"`
}

type SetupView struct {
	Mode      string        `json:"mode"`
	ServerURL string        `json:"server_url,omitempty"`
	Agent     string        `json:"agent,omitempty"`
	PinSet    bool          `json:"pin_set"`
	Locked    bool          `json:"locked"` // settings need the PIN
	Network   netmgr.Status `json:"network"`
	Pairing   PairingView   `json:"pairing"`
	Notice    string        `json:"notice,omitempty"`
	Provision string        `json:"provisioned_by,omitempty"` // "node.yaml" when the server came from config
}

// connection resolves which server and token to use. Setup results win;
// a node.yaml with server.url and a token counts as pre-provisioned.
func (n *Node) connection() (url, token string) {
	s := n.store.Get()
	if s.SetupComplete || s.Token != "" {
		return s.ServerURL, s.Token
	}
	if t, _ := n.cfg.ServerToken(); n.cfg.Server.URL != "" && t != "" {
		return n.cfg.Server.URL, t
	}
	return "", ""
}

func (n *Node) provisioned() bool {
	s := n.store.Get()
	t, _ := n.cfg.ServerToken()
	return !s.SetupComplete && s.Token == "" && n.cfg.Server.URL != "" && t != ""
}

func (n *Node) Mode() string {
	s := n.store.Get()
	switch {
	case n.provisioned():
		return ModeConnected
	case !s.SetupComplete:
		return ModeSetup
	case s.Token != "":
		return ModeConnected
	default:
		return ModeStandalone
	}
}

func (n *Node) setupView() SetupView {
	s := n.store.Get()
	n.setup.mu.Lock()
	defer n.setup.mu.Unlock()
	v := SetupView{
		Mode: n.Mode(), ServerURL: s.ServerURL, Agent: s.Agent, PinSet: s.PinHash != "",
		Network: n.setup.net, Pairing: n.setup.pairing, Notice: n.setup.notice,
	}
	if v.Pairing.Status == "" {
		v.Pairing.Status = "idle"
	}
	if n.provisioned() {
		v.ServerURL, v.Provision = n.cfg.Server.URL, "node.yaml"
	}
	v.Locked = v.Mode != ModeSetup && v.PinSet
	return v
}

// --- network ---

func (n *Node) pollNetwork(ctx context.Context) {
	n.RefreshNetwork(ctx)
	t := time.NewTicker(netPollEvery)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			n.RefreshNetwork(ctx)
		case <-ctx.Done():
			return
		}
	}
}

func (n *Node) RefreshNetwork(ctx context.Context) netmgr.Status {
	st := n.net.Status(ctx)
	n.setup.mu.Lock()
	changed := st != n.setup.net
	n.setup.net = st
	n.setup.mu.Unlock()
	if changed {
		n.changed()
	}
	return st
}

func (n *Node) ScanWiFi(ctx context.Context) ([]netmgr.Network, error) { return n.net.Scan(ctx) }

func (n *Node) ConnectWiFi(ctx context.Context, ssid, password string) error {
	err := n.net.Connect(ctx, ssid, password)
	n.RefreshNetwork(ctx)
	return err
}

// --- server and pairing ---

// SetServer checks there's an Apteva instance at the address and remembers it.
func (n *Node) SetServer(ctx context.Context, raw string) (string, error) {
	u, err := pairing.Normalize(raw)
	if err != nil {
		return "", err
	}
	n.setup.mu.Lock()
	clock := n.setup.net.ClockSynced
	n.setup.mu.Unlock()
	if err := pairing.Probe(ctx, u, clock); err != nil {
		return "", err
	}
	if err := n.store.Update(func(s *state.State) { s.ServerURL = u }); err != nil {
		return "", err
	}
	n.changed()
	return u, nil
}

// StartPairing asks the server for a code and waits in the background for a
// person to approve it in the dashboard.
func (n *Node) StartPairing() error {
	base := n.store.Get().ServerURL
	if base == "" {
		return errors.New("choose a server first")
	}
	n.CancelPairing()

	req := pairing.StartRequest{Name: n.cfg.Name, Version: n.version}
	for _, c := range n.cfg.Capabilities {
		req.Hardware = append(req.Hardware, pairing.Hardware{ID: c.ID, Kind: c.Kind, Description: c.Description})
	}
	ctx, cancel := context.WithCancel(context.Background())
	started, err := pairing.Start(ctx, base, req)
	if err != nil {
		cancel()
		n.setPairing(PairingView{Status: "error", Error: err.Error()})
		return err
	}
	expires := time.Now().Add(time.Duration(started.ExpiresIn) * time.Second)
	if started.ExpiresIn <= 0 {
		expires = time.Now().Add(pairingFallback)
	}
	n.setup.mu.Lock()
	n.setup.cancel = cancel
	n.setup.notice = ""
	n.setup.mu.Unlock()
	n.setPairing(PairingView{Status: "waiting", Code: started.UserCode, VerifyURL: started.VerifyURL, ExpiresAt: expires})
	go n.waitForApproval(ctx, base, started, expires)
	return nil
}

func (n *Node) waitForApproval(ctx context.Context, base string, started pairing.Started, expires time.Time) {
	interval := time.Duration(started.Interval) * time.Second
	fails := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
		if time.Now().After(expires) {
			n.updatePairing(func(p *PairingView) { p.Status = "expired" })
			return
		}
		r, err := pairing.Poll(ctx, base, started.PairingID)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// Keep waiting through blips; give up only if the server stays away.
			if fails++; fails >= 5 {
				n.updatePairing(func(p *PairingView) { p.Status, p.Error = "error", err.Error() })
				return
			}
			continue
		}
		fails = 0
		switch r.Status {
		case "pending":
			continue
		case "approved":
			if r.Token == "" {
				n.updatePairing(func(p *PairingView) { p.Status, p.Error = "error", "the server approved but sent no token" })
				return
			}
			err := n.store.Update(func(s *state.State) {
				s.ServerURL, s.Token, s.Agent, s.PairedAt = base, r.Token, r.Agent, time.Now()
			})
			if err != nil {
				n.updatePairing(func(p *PairingView) { p.Status, p.Error = "error", "couldn't save the pairing: "+err.Error() })
				return
			}
			n.updatePairing(func(p *PairingView) { p.Status, p.Agent = "approved", r.Agent })
			n.mu.Lock()
			n.agent.Name = r.Agent
			n.mu.Unlock()
			n.startLink(base, r.Token)
			return
		case "denied", "expired":
			n.updatePairing(func(p *PairingView) { p.Status = r.Status })
			return
		default:
			n.updatePairing(func(p *PairingView) { p.Status, p.Error = "error", "unexpected answer: "+r.Status })
			return
		}
	}
}

func (n *Node) CancelPairing() {
	n.setup.mu.Lock()
	if n.setup.cancel != nil {
		n.setup.cancel()
		n.setup.cancel = nil
	}
	if n.setup.pairing.Status == "waiting" {
		n.setup.pairing = PairingView{Status: "idle"}
	}
	n.setup.mu.Unlock()
	n.changed()
}

func (n *Node) setPairing(p PairingView) { n.updatePairing(func(old *PairingView) { *old = p }) }

func (n *Node) updatePairing(fn func(*PairingView)) {
	n.setup.mu.Lock()
	fn(&n.setup.pairing)
	n.setup.mu.Unlock()
	n.changed()
}

// UseStandalone disconnects from Apteva (if paired) and keeps running on
// local hardware only. The server address is kept for an easy reconnect.
func (n *Node) UseStandalone() error {
	n.CancelPairing()
	n.stopLink()
	err := n.store.Update(func(s *state.State) { s.Token, s.Agent = "", ""; s.SetupComplete = true })
	if _, e := n.events.Prune("", time.Now()); err == nil {
		err = e
	}
	n.setup.mu.Lock()
	n.setup.pairing = PairingView{Status: "idle"}
	n.setup.mu.Unlock()
	n.mu.Lock()
	n.agent = AgentView{State: "idle", Since: time.Now()}
	n.agents = nil
	n.projects = nil
	n.mu.Unlock()
	n.setWidgets(nil)
	n.changed()
	return err
}

func (n *Node) FinishSetup() error {
	err := n.store.Update(func(s *state.State) { s.SetupComplete = true })
	n.changed()
	return err
}

// onRevoked: the server refused this device's token (removed from the
// dashboard, or the pairing was reset). Drop to standalone and say why.
func (n *Node) onRevoked() {
	n.setup.mu.Lock()
	n.setup.notice = "This device was removed from Apteva, or its pairing is no longer valid. It keeps working on its own; pair it again from Settings."
	n.setup.mu.Unlock()
	n.store.Update(func(s *state.State) { s.SetupComplete = true })
	n.UseStandalone()
	n.record(Action{By: "local", Tool: "link", Summary: "server refused this device — now standalone"})
}

// FactoryReset forgets pairing, PIN and setup. node.yaml and saved Wi-Fi
// networks are kept.
func (n *Node) FactoryReset() error {
	n.CancelPairing()
	n.stopLink()
	err := n.store.Reset()
	if _, e := n.events.Prune("", time.Now()); err == nil {
		err = e
	}
	n.setup.mu.Lock()
	n.setup.pairing = PairingView{Status: "idle"}
	n.setup.notice = ""
	n.setup.unlocks = map[string]time.Time{}
	n.setup.fails = 0
	n.setup.mu.Unlock()
	n.mu.Lock()
	n.agent = AgentView{State: "idle", Since: time.Now()}
	n.agents = nil
	n.projects = nil
	n.conversation = nil
	n.mu.Unlock()
	n.setWidgets(nil)
	n.record(Action{By: "local", Tool: "reset", OK: err == nil, Summary: "device reset to first-boot setup"})
	return err
}

// --- PIN and unlock ---

// Authorized reports whether a settings change may go ahead: always during
// setup or without a PIN, otherwise only with a live unlock token.
func (n *Node) Authorized(token string) bool {
	if n.Mode() == ModeSetup || !n.store.HasPIN() {
		return true
	}
	n.setup.mu.Lock()
	defer n.setup.mu.Unlock()
	exp, ok := n.setup.unlocks[token]
	if !ok || time.Now().After(exp) {
		delete(n.setup.unlocks, token)
		return false
	}
	return true
}

var ErrPinLocked = errors.New("too many wrong PINs")

// Unlock trades the PIN for a short-lived token for settings changes.
func (n *Node) Unlock(pin string) (string, time.Time, error) {
	n.setup.mu.Lock()
	if time.Now().Before(n.setup.lockedTil) {
		wait := time.Until(n.setup.lockedTil).Round(time.Second)
		n.setup.mu.Unlock()
		return "", time.Time{}, fmt.Errorf("%w — try again in %s", ErrPinLocked, wait)
	}
	n.setup.mu.Unlock()

	if !n.store.CheckPIN(pin) {
		n.setup.mu.Lock()
		n.setup.fails++
		if n.setup.fails >= pinMaxFails {
			n.setup.fails = 0
			n.setup.lockedTil = time.Now().Add(pinLockout)
		}
		n.setup.mu.Unlock()
		return "", time.Time{}, errors.New("wrong PIN")
	}
	b := make([]byte, 16)
	rand.Read(b)
	tok := hex.EncodeToString(b)
	exp := time.Now().Add(unlockFor)
	n.setup.mu.Lock()
	n.setup.fails = 0
	n.setup.unlocks[tok] = exp
	n.setup.mu.Unlock()
	return tok, exp, nil
}

func (n *Node) SetPIN(pin string) error {
	if pin != "" && !state.ValidPIN(pin) {
		return errors.New("use 4 to 8 digits")
	}
	err := n.store.SetPIN(pin)
	n.changed()
	return err
}

// LocalCapture takes a photo for the person at the device (hardware check);
// it never leaves the device.
func (n *Node) LocalCapture(capID string) error {
	for _, c := range n.caps {
		if c.cfg.ID == capID && c.cfg.Kind == config.KindCamera {
			jpg, err := n.capture(c, c.cfg.Width, c.cfg.Height)
			if err == nil {
				c.mu.Lock()
				c.lastPhoto, c.photoAt = jpg, time.Now()
				c.mu.Unlock()
			}
			n.record(Action{By: "local", Tool: capID + "_capture", OK: err == nil, Summary: "test photo from " + capID})
			return err
		}
	}
	return fmt.Errorf("no camera %q", capID)
}

func (n *Node) DismissNotice() {
	n.setup.mu.Lock()
	n.setup.notice = ""
	n.setup.mu.Unlock()
	n.changed()
}
