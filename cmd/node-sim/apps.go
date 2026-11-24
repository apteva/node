package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/apteva/node/internal/proto"
)

// Fake app backends for the widgets the sim places on the device. The
// surface documents are real apteva-native-surface/v1 files: task-overview
// is copied verbatim from apps/mcp/tasks, store-today exercises every other
// widget block.

//go:embed surfaces/*.json
var surfaces embed.FS

func widgets() []proto.Widget {
	read := func(name string) json.RawMessage {
		b, err := surfaces.ReadFile("surfaces/" + name)
		if err != nil {
			panic(err)
		}
		return b
	}
	return []proto.Widget{
		{
			ID: "w-store", App: "store", Component: "store-today", Size: "full",
			RefreshTopics: []string{"store.changed"},
			Surface:       read("store-today.json"),
		},
		{
			ID: "w-tasks", App: "tasks", Component: "task-overview", Size: "half",
			// Settings as the server would merge them from the manifest's settings_schema defaults.
			Settings:      map[string]any{"show_active": true, "show_upcoming": true, "show_recent": true, "recent_limit": 4},
			RefreshTopics: []string{"task.created", "task.updated", "task.state_changed"},
			Surface:       read("task-overview.json"),
		},
	}
}

type fakeApps struct {
	mu     sync.Mutex
	open   bool
	orders int
	sales  float64
	notes  []map[string]any
	steps  int
}

func newFakeApps() *fakeApps {
	return &fakeApps{open: true, orders: 18, sales: 742.5, notes: []map[string]any{
		{"id": "n1", "text": "Window display swapped to autumn", "tag": "done", "created_at": time.Now().Add(-95 * time.Minute).Format(time.RFC3339)},
	}}
}

func ok(v any) proto.AppResponse {
	b, _ := json.Marshal(v)
	return proto.AppResponse{Status: 200, Body: b}
}

func (a *fakeApps) handle(req proto.AppRequest) proto.AppResponse {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch req.App + " " + req.Method + " " + req.Path {
	case "tasks GET /mobile/summary":
		return ok(a.taskSummary())
	case "store GET /device/today":
		status := "open"
		if !a.open {
			status = "paused"
		}
		headline := fmt.Sprintf("Busiest hour so far: 11:00 with %d orders.", 4+a.orders/6)
		return ok(map[string]any{
			"store_status": status, "sales": a.sales, "orders": a.orders, "visitors": 120 + a.orders*7,
			"goal_progress": min(a.sales/1200, 1), "headline": headline, "notes": reverse(a.notes),
		})
	case "store POST /device/store/toggle":
		a.open = !a.open
		return ok(map[string]any{"open": a.open})
	case "store POST /device/notes":
		body, _ := req.Body.(map[string]any)
		text, _ := body["text"].(string)
		if strings.TrimSpace(text) == "" {
			return proto.AppResponse{Status: 422, Error: "text required"}
		}
		tag := "note"
		if u, _ := body["urgent"].(bool); u {
			tag = "urgent"
		}
		a.notes = append(a.notes, map[string]any{"id": fmt.Sprintf("n%d", len(a.notes)+1), "text": text, "tag": tag, "created_at": time.Now().Format(time.RFC3339)})
		return ok(map[string]any{"ok": true})
	}
	return proto.AppResponse{Status: 404, Error: "no route " + req.Method + " " + req.Path + " in " + req.App}
}

// tick moves the fake data along and reports which app changed.
func (a *fakeApps) tick() (app, topic string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if rand.IntN(2) == 0 && a.open {
		a.orders++
		a.sales += 18 + rand.Float64()*40
		return "store", "store.changed"
	}
	a.steps++
	return "tasks", "task.updated"
}

func (a *fakeApps) taskSummary() map[string]any {
	now := time.Now()
	steps := []string{"Drafting the reply", "Checking stock levels", "Waiting on supplier", "Summarising feedback"}
	return map[string]any{
		"counts": map[string]int{"active": 2, "scheduled": 3, "blocked": 1},
		"active": []map[string]any{
			{"id": 41, "title": "Reorder oat milk", "current_step": steps[a.steps%len(steps)], "state": "running"},
			{"id": 42, "title": "Answer review on Maps", "current_step": "", "state": "blocked"},
		},
		"upcoming": []map[string]any{
			{"id": 7, "title": "Morning sales digest", "next_run_at": now.Add(14 * time.Hour).Format(time.RFC3339)},
			{"id": 8, "title": "Weekly supplier check", "next_run_at": now.Add(50 * time.Hour).Format(time.RFC3339)},
		},
		"recent": []map[string]any{
			{"id": 39, "title": "Post autumn menu", "state": "completed"},
			{"id": 38, "title": "Update opening hours", "state": "failed"},
		},
	}
}

func reverse(in []map[string]any) []map[string]any {
	out := make([]map[string]any, len(in))
	for i, v := range in {
		out[len(in)-1-i] = v
	}
	return out
}

func (s *session) appTicker(apps *fakeApps, every time.Duration) {
	for range time.Tick(every) {
		app, topic := apps.tick()
		if err := s.send(proto.TypeAppEvent, "", proto.AppEvent{App: app, Topic: topic}); err == nil {
			log.Printf("app_event %s %s", app, topic)
		}
	}
}
