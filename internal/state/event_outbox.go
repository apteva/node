package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/apteva/node/internal/proto"
)

// EventOutbox survives a daemon restart. Entries belong to one pairing only.
// Capacity errors are reported to the operator; existing events are never evicted.
type EventOutbox struct {
	mu      sync.Mutex
	dir     string
	entries []QueuedEvent
}
type QueuedEvent struct {
	Scope string      `json:"scope"`
	Frame proto.Frame `json:"frame"`
	At    time.Time   `json:"at"`
}

const EventQueueLimit = 512

func EventScope(server, token string) string {
	if token == "" {
		return ""
	}
	h := sha256.Sum256([]byte(server + "\x00" + token))
	return hex.EncodeToString(h[:])
}
func OpenEventOutbox(dir string) (*EventOutbox, error) {
	q := &EventOutbox{dir: dir}
	raw, err := os.ReadFile(filepath.Join(dir, "event-outbox.json"))
	if errors.Is(err, os.ErrNotExist) {
		return q, nil
	}
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(raw, &q.entries)
	return q, err
}
func (q *EventOutbox) save(next []QueuedEvent) error {
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if len(raw) > 4<<20 {
		return errors.New("event queue exceeds 4 MiB")
	}
	if err = os.MkdirAll(q.dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(q.dir, ".events-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chmod(0600); err != nil {
		return err
	}
	if _, err = f.Write(raw); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), filepath.Join(q.dir, "event-outbox.json")); err != nil {
		return err
	}
	d, err := os.Open(q.dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err = d.Sync(); err != nil {
		return err
	}
	q.entries = next
	return nil
}
func (q *EventOutbox) Add(scope string, frame proto.Frame, at time.Time) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if scope == "" {
		return errors.New("event has no pairing")
	}
	if len(q.entries) >= EventQueueLimit {
		return errors.New("event queue is full")
	}
	next := append(append([]QueuedEvent{}, q.entries...), QueuedEvent{scope, frame, at})
	return q.save(next)
}
func (q *EventOutbox) Remove(scope, id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	next := []QueuedEvent{}
	found := false
	for _, e := range q.entries {
		if e.Scope == scope && e.Frame.ID == id {
			found = true
			continue
		}
		next = append(next, e)
	}
	if !found {
		return nil
	}
	return q.save(next)
}
func (q *EventOutbox) Pending(scope string) []QueuedEvent {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := []QueuedEvent{}
	for _, e := range q.entries {
		if e.Scope == scope {
			out = append(out, e)
		}
	}
	return out
}

// Prune removes expired events and entries belonging to an old pairing.
func (q *EventOutbox) Prune(scope string, now time.Time) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	next := []QueuedEvent{}
	for _, e := range q.entries {
		if scope != "" && e.Scope == scope && now.Sub(e.At) < 24*time.Hour {
			next = append(next, e)
		}
	}
	n := len(q.entries) - len(next)
	if n == 0 {
		return 0, nil
	}
	return n, q.save(next)
}
