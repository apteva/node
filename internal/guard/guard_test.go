package guard

import (
	"context"
	"testing"
	"time"
)

func waitPending(t *testing.T, g *Guard) Approval {
	t.Helper()
	for i := 0; i < 200; i++ {
		if p := g.Pending(); len(p) > 0 {
			return p[0]
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("no pending approval")
	return Approval{}
}

func TestNoneIsAllowedImmediately(t *testing.T) {
	g := New(time.Second, nil)
	if out := g.Request(context.Background(), "lamp", "lamp_set", "", "none"); !out.Allowed {
		t.Fatal("expected allow")
	}
}

func TestScreenApproval(t *testing.T) {
	g := New(time.Second, nil)
	res := make(chan Outcome, 1)
	go func() { res <- g.Request(context.Background(), "eye", "eye_capture", "take a photo", "screen") }()
	a := waitPending(t, g)
	if !g.Resolve(a.ID, true, false) {
		t.Fatal("resolve failed")
	}
	if out := <-res; !out.Allowed || out.Via != "screen" {
		t.Fatalf("got %+v", out)
	}
	if len(g.Pending()) != 0 {
		t.Fatal("approval not cleared")
	}
}

func TestHoldModeNeedsAHold(t *testing.T) {
	g := New(time.Second, nil)
	res := make(chan Outcome, 1)
	go func() { res <- g.Request(context.Background(), "door", "door_set", "open the door", "hold") }()
	a := waitPending(t, g)
	if g.Resolve(a.ID, true, false) {
		t.Fatal("a plain tap must not allow a hold-mode approval")
	}
	if !g.Resolve(a.ID, true, true) {
		t.Fatal("hold not accepted")
	}
	if out := <-res; !out.Allowed || out.Via != "screen" {
		t.Fatalf("got %+v", out)
	}
}

func TestTapCanDenyHoldMode(t *testing.T) {
	g := New(time.Second, nil)
	res := make(chan Outcome, 1)
	go func() { res <- g.Request(context.Background(), "door", "door_set", "", "hold") }()
	a := waitPending(t, g)
	g.Resolve(a.ID, false, false)
	if out := <-res; out.Allowed {
		t.Fatal("expected deny")
	}
}

func TestTimeoutDenies(t *testing.T) {
	g := New(20*time.Millisecond, nil)
	out := g.Request(context.Background(), "eye", "eye_capture", "", "screen")
	if out.Allowed || out.Via != "timeout" {
		t.Fatalf("got %+v", out)
	}
}
