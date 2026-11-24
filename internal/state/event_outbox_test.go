package state

import (
	"github.com/apteva/node/internal/proto"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEventOutboxRestartAcknowledgementAndIsolation(t *testing.T) {
	dir := t.TempDir()
	q, err := OpenEventOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	scope := EventScope("https://server", "secret")
	f, _ := proto.New(proto.TypeEvent, "stable-event", proto.Event{ID: "stable-event", Capability: "button", Kind: "pressed", At: time.Now()})
	if err = q.Add(scope, f, time.Now()); err != nil {
		t.Fatal(err)
	}
	q, err = OpenEventOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	pending := q.Pending(scope)
	if len(pending) != 1 || pending[0].Frame.ID != f.ID || string(pending[0].Frame.Data) != string(f.Data) {
		t.Fatal("restart lost original event")
	}
	if len(q.Pending(EventScope("https://server", "other-account"))) != 0 {
		t.Fatal("event leaked across pairing")
	}
	if err = q.Remove("wrong-scope", f.ID); err != nil {
		t.Fatal(err)
	}
	if len(q.Pending(scope)) != 1 {
		t.Fatal("foreign ACK removed event")
	}
	if err = q.Remove(scope, f.ID); err != nil {
		t.Fatal(err)
	}
	q, err = OpenEventOutbox(dir)
	if err != nil || len(q.Pending(scope)) != 0 {
		t.Fatal("ACK not durable", err)
	}
	info, _ := os.Stat(filepath.Join(dir, "event-outbox.json"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("queue must be private")
	}
}
func TestEventOutboxBoundedAndExpiryVisible(t *testing.T) {
	q, _ := OpenEventOutbox(t.TempDir())
	scope := EventScope("server", "token")
	f, _ := proto.New(proto.TypeEvent, "event", nil)
	// Seed the disk snapshot without doing 512 fsyncs; Add must refuse eviction.
	q.entries = make([]QueuedEvent, EventQueueLimit)
	for i := range q.entries {
		q.entries[i] = QueuedEvent{scope, f, time.Now()}
	}
	if err := q.Add(scope, f, time.Now()); err == nil {
		t.Fatal("capacity did not reject")
	}
	if len(q.Pending(scope)) != EventQueueLimit {
		t.Fatal("overflow evicted events")
	}
	count, err := q.Prune(scope, time.Now().Add(25*time.Hour))
	if err != nil || count != EventQueueLimit {
		t.Fatal("expiry not reported", count, err)
	}
	if err = q.Add(scope, f, time.Now()); err != nil {
		t.Fatal(err)
	}
	count, err = q.Prune(EventScope("server", "replacement-token"), time.Now())
	if count != 1 || err != nil {
		t.Fatal("old pairing was retained")
	}
}
