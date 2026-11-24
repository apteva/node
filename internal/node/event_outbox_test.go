package node

import (
	"encoding/json"
	"github.com/apteva/node/internal/proto"
	"github.com/apteva/node/internal/state"
	"testing"
)

func TestDeviceEventPersistsUntilAcceptedAck(t *testing.T) {
	n, fs, _ := start(t)
	<-fs.hello
	n.emit("call", "pressed", nil)
	url, token := n.connection()
	scope := state.EventScope(url, token)
	pending := n.events.Pending(scope)
	// Initial sensor readings may already have queued a threshold event.
	var id string
	for _, e := range pending {
		var event proto.Event
		_ = json.Unmarshal(e.Frame.Data, &event)
		if event.Capability == "call" {
			id = e.Frame.ID
			if event.ID != id || id == "" {
				t.Fatal("missing stable event identity")
			}
		}
	}
	if id == "" {
		t.Fatal("event was not saved")
	}
	n.onFrame(proto.Frame{Type: "ack", ID: id, Data: json.RawMessage(`{"accepted":false}`)})
	if len(n.events.Pending(scope)) != len(pending) {
		t.Fatal("unaccepted ACK removed event")
	}
	n.onFrame(proto.Frame{Type: "ack", ID: id, Data: json.RawMessage(`{"accepted":true}`)})
	q, err := state.OpenEventOutbox(n.cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range q.Pending(scope) {
		if e.Frame.ID == id {
			t.Fatal("accepted ACK did not remove event on disk")
		}
	}
	if err = n.UseStandalone(); err != nil {
		t.Fatal(err)
	}
	n.emit("call", "pressed", nil)
	if len(n.events.Pending(scope)) != 0 {
		t.Fatal("standalone kept account events")
	}

}
