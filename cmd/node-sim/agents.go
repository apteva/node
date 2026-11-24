package main

import (
	"math/rand/v2"
	"sync"
	"time"

	"github.com/apteva/node/internal/proto"
)

// The project's agents, as the device's Agents page lists them. Icons are
// the dashboard's AgentMark ids. A ticker moves the others between states
// so the grid looks like a live project.
type roster struct {
	mu     sync.Mutex
	agents []proto.AgentInfo
}

func newRoster() *roster {
	return &roster{agents: []proto.AgentInfo{
		{ID: "atlas", Name: "Atlas", Icon: "assistant", Role: "Front of house, talks to visitors", State: "idle"},
		{ID: "juno", Name: "Juno", Icon: "research", Role: "Research and briefings", State: "thinking", Detail: "Reading supplier reports"},
		{ID: "desk", Name: "Front desk", Icon: "support", Role: "Customer support inbox", State: "acting", Detail: "Replying to 3 tickets"},
		{ID: "ledger", Name: "Ledger", Icon: "finance", Role: "Invoices and payments", State: "idle"},
		{ID: "quill", Name: "Quill", Icon: "writer", Role: "Newsletter and blog", State: "idle"},
		{ID: "pixel", Name: "Pixel", Icon: "design", Role: "Social visuals", State: "paused"},
		{ID: "scout", Name: "Scout", Icon: "sales", Role: "Leads and follow-ups", State: "thinking", Detail: "Scoring new leads"},
		{ID: "forge", Name: "Forge", Icon: "code", Role: "Site and automations", State: "idle"},
		{ID: "sentry", Name: "Sentry", Icon: "security", Role: "Watches logins and alerts", State: "idle"},
	}}
}

func (r *roster) list() []proto.AgentInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]proto.AgentInfo{}, r.agents...)
}

func (r *roster) get(id string) (proto.AgentInfo, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, a := range r.agents {
		if a.ID == id {
			return a, true
		}
	}
	return proto.AgentInfo{}, false
}

func (r *roster) byName(name string) (proto.AgentInfo, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, a := range r.agents {
		if a.Name == name {
			return a, true
		}
	}
	return r.agents[0], false
}

func (r *roster) set(id, state, detail string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.agents {
		if r.agents[i].ID == id {
			r.agents[i].State, r.agents[i].Detail = state, detail
		}
	}
}

var busyWork = []string{"Drafting a reply", "Checking the calendar", "Summarising yesterday", "Updating the CRM", "Reading new mail", "Preparing a report"}

// churn moves one agent (never the one at the device, never a paused one)
// to a new state now and then.
func (s *session) churn(every time.Duration) {
	for range time.Tick(every) {
		list := s.roster.list()
		a := list[rand.IntN(len(list))]
		if a.ID == s.activeID() || a.State == "paused" {
			continue
		}
		switch rand.IntN(3) {
		case 0:
			s.roster.set(a.ID, "idle", "")
		case 1:
			s.roster.set(a.ID, "thinking", busyWork[rand.IntN(len(busyWork))])
		default:
			s.roster.set(a.ID, "acting", busyWork[rand.IntN(len(busyWork))])
		}
		s.send(proto.TypeAgents, "", proto.Agents{Agents: s.roster.list()})
	}
}

func (s *session) activeID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// status reports what the agent at the device is doing, both to the
// device's presence screen and to its entry on the Agents page.
func (s *session) status(state, detail string) {
	id := s.activeID()
	s.roster.set(id, state, detail)
	s.send(proto.TypeStatus, "", proto.Status{State: state, Detail: detail})
	s.send(proto.TypeAgents, "", proto.Agents{Agents: s.roster.list()})
}

// bind makes id the agent at the device and tells the device.
func (s *session) bind(id string) bool {
	a, ok := s.roster.get(id)
	if !ok {
		return false
	}
	s.mu.Lock()
	s.active = a.ID
	s.mu.Unlock()
	s.send(proto.TypeWelcome, "", proto.Welcome{AgentID: a.ID, Agent: a.Name, Icon: a.Icon})
	state := a.State
	if state == "paused" {
		state = "idle"
	}
	s.status(state, a.Detail)
	return true
}
