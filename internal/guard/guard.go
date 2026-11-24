// Package guard holds agent actions that need a human on the device to say
// yes. Approval happens here, on the Node, so a compromised or confused
// server can't skip it.
package guard

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sort"
	"sync"
	"time"

	"github.com/apteva/node/internal/config"
)

type Approval struct {
	ID         string    `json:"id"`
	Capability string    `json:"capability"`
	Tool       string    `json:"tool"`
	Summary    string    `json:"summary"`
	Mode       string    `json:"mode"` // screen | hold
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at"`

	done chan decision
}

type decision struct {
	allow bool
	via   string
}

// Outcome of a Request.
type Outcome struct {
	Allowed bool
	Via     string // screen | timeout | cancelled
}

type Guard struct {
	timeout  time.Duration
	onChange func()

	mu      sync.Mutex
	pending map[string]*Approval
}

func New(timeout time.Duration, onChange func()) *Guard {
	if onChange == nil {
		onChange = func() {}
	}
	return &Guard{timeout: timeout, onChange: onChange, pending: map[string]*Approval{}}
}

// Request blocks until the person decides, the timeout passes or ctx ends.
// mode "none" is allowed immediately.
func (g *Guard) Request(ctx context.Context, capability, tool, summary, mode string) Outcome {
	if mode == config.ConfirmNone || mode == "" {
		return Outcome{Allowed: true, Via: "policy"}
	}
	now := time.Now()
	a := &Approval{
		ID: newID(), Capability: capability, Tool: tool, Summary: summary, Mode: mode,
		CreatedAt: now, ExpiresAt: now.Add(g.timeout),
		done: make(chan decision, 1),
	}
	g.mu.Lock()
	g.pending[a.ID] = a
	g.mu.Unlock()
	g.onChange()

	defer func() {
		g.mu.Lock()
		delete(g.pending, a.ID)
		g.mu.Unlock()
		g.onChange()
	}()

	timer := time.NewTimer(g.timeout)
	defer timer.Stop()
	select {
	case d := <-a.done:
		return Outcome{Allowed: d.allow, Via: d.via}
	case <-timer.C:
		return Outcome{Via: "timeout"}
	case <-ctx.Done():
		return Outcome{Via: "cancelled"}
	}
}

// Resolve answers from the touchscreen. A hold-mode approval can be denied
// with a tap but only allowed by a completed press-and-hold, so a stray
// touch never opens a door.
func (g *Guard) Resolve(id string, allow, held bool) bool {
	g.mu.Lock()
	a := g.pending[id]
	g.mu.Unlock()
	if a == nil {
		return false
	}
	if allow && a.Mode == config.ConfirmHold && !held {
		return false
	}
	return send(a, decision{allow: allow, via: "screen"})
}

func (g *Guard) Pending() []Approval {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]Approval, 0, len(g.pending))
	for _, a := range g.pending {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func send(a *Approval, d decision) bool {
	select {
	case a.done <- d:
		return true
	default:
		return false // already decided
	}
}

func newID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}
