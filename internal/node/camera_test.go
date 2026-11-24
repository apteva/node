package node

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apteva/node/internal/config"
	"github.com/apteva/node/internal/hw"
	"github.com/apteva/node/internal/link"
	"github.com/apteva/node/internal/proto"
)

const cameraYAML = `
name: desk
safety:
  confirm_timeout: 2s
capabilities:
  - id: eye
    kind: camera
    width: 320
    height: 240
    max_width: 640
    max_height: 480
    confirm: none
  - id: back
    kind: camera
    index: 1
`

// slowCam records how many captures overlap and what it was asked for.
type slowCam struct {
	*hw.Sim
	active, peak atomic.Int32
	mu           sync.Mutex
	asked        []hw.CaptureOptions
}

func (s *slowCam) Capture(o hw.CaptureOptions) ([]byte, error) {
	if a := s.active.Add(1); a > s.peak.Load() {
		s.peak.Store(a)
	}
	defer s.active.Add(-1)
	s.mu.Lock()
	s.asked = append(s.asked, o)
	s.mu.Unlock()
	time.Sleep(40 * time.Millisecond)
	return s.Sim.Capture(o)
}

func photoResult(t *testing.T, r proto.Result) (w, h int, jpg []byte) {
	t.Helper()
	if !r.OK {
		t.Fatalf("capture failed: %+v", r)
	}
	var p struct {
		Width, Height int
		Data          string
	}
	raw, _ := json.Marshal(r.Data)
	json.Unmarshal(raw, &p)
	jpg, _ = base64.StdEncoding.DecodeString(p.Data)
	return p.Width, p.Height, jpg
}

func TestCapturesNeverOverlap(t *testing.T) {
	cam := &slowCam{Sim: hw.NewSim()}
	n, fs := startWith(t, cameraYAML, cam)
	<-fs.hello
	for i := range 3 {
		fs.send(proto.TypeInvoke, string(rune('a'+i)), proto.Invoke{Tool: "eye_capture"})
	}
	done := make(chan error, 1)
	go func() { done <- n.LocalCapture("eye") }()
	// Results may come back in any order; fs.result would drop the others.
	got := map[string]bool{}
	for range 3 {
		f := fs.next(proto.TypeResult)
		r, _ := link.Decode[proto.Result](f)
		photoResult(t, r)
		got[f.ID] = true
	}
	if len(got) != 3 {
		t.Errorf("results: %v", got)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if p := cam.peak.Load(); p != 1 {
		t.Errorf("%d captures ran at once", p)
	}
}

func TestCaptureSizeIsCappedAndReported(t *testing.T) {
	cam := &slowCam{Sim: hw.NewSim()}
	n, fs := startWith(t, cameraYAML, cam)
	<-fs.hello
	args, _ := json.Marshal(map[string]int{"width": 4000})
	fs.send(proto.TypeInvoke, "big", proto.Invoke{Tool: "eye_capture", Args: args})
	w, h, _ := photoResult(t, fs.result("big"))
	if w != 640 || h != 480 {
		t.Errorf("got %d×%d, want the 640×480 maximum", w, h)
	}
	cam.mu.Lock()
	o := cam.asked[len(cam.asked)-1]
	cam.mu.Unlock()
	if o.Width != 640 || o.Height != 480 || o.Quality != captureQuality {
		t.Errorf("camera asked for %+v", o)
	}
	sent := n.Snapshot().SentPhoto
	if sent == nil || sent.Capability != "eye" || sent.Width != 640 {
		t.Errorf("sent photo not recorded: %+v", sent)
	}
	conv := n.Snapshot().Conversation
	if len(conv) == 0 || !strings.Contains(conv[len(conv)-1].Text, "Sent a photo") {
		t.Errorf("no conversation line for the photo: %+v", conv)
	}
}

func TestPhotoShrunkToFitTheLink(t *testing.T) {
	old := maxPhotoBytes
	maxPhotoBytes = 2000
	t.Cleanup(func() { maxPhotoBytes = old })
	n, fs := startWith(t, cameraYAML, hw.NewSim())
	<-fs.hello
	fs.send(proto.TypeInvoke, "1", proto.Invoke{Tool: "eye_capture", Args: json.RawMessage(`{"width":640}`)})
	_, _, jpg := photoResult(t, fs.result("1"))
	if len(jpg) > 2000 {
		t.Errorf("photo is %d bytes, limit 2000", len(jpg))
	}
	if photo, _ := n.Photo("eye"); len(photo) != len(jpg) {
		t.Errorf("device shows %d bytes, sent %d: the screen must show what was sent", len(photo), len(jpg))
	}
}

func TestMissingCameraShownAndDefaultAsks(t *testing.T) {
	n, fs := startWith(t, cameraYAML, hw.NewSim())
	h := <-fs.hello
	waitFor(t, "camera check", func() bool {
		for _, c := range n.Snapshot().Capabilities {
			if c.ID == "back" && c.Error != "" {
				return true
			}
		}
		return false
	})
	for _, c := range n.Snapshot().Capabilities {
		switch c.ID {
		case "eye":
			if c.Error != "" || c.CameraModel != "sim" {
				t.Errorf("eye: %+v", c)
			}
		case "back":
			if !strings.Contains(c.Error, "port 1") || c.Confirm != config.ConfirmScreen {
				t.Errorf("back: %+v", c)
			}
		}
	}
	// The camera without confirm in node.yaml asks on screen.
	var schema string
	for _, tl := range h.Tools {
		if tl.Name == "back_capture" {
			schema = string(tl.InputSchema)
		}
	}
	if !strings.Contains(schema, "width") {
		t.Errorf("capture schema lacks size: %s", schema)
	}
	fs.send(proto.TypeInvoke, "q", proto.Invoke{Tool: "back_capture"})
	waitFor(t, "approval", func() bool { return len(n.Snapshot().Approvals) == 1 })
	n.Resolve(n.Snapshot().Approvals[0].ID, true, false)
	if r := fs.result("q"); r.OK || !strings.Contains(r.Error, "port 1") {
		t.Errorf("expected a missing-camera error, got %+v", r)
	}
}

func TestPhotoSize(t *testing.T) {
	cfg := config.Capability{Width: 1280, Height: 720, MaxWidth: 1920, MaxHeight: 1080}
	for _, c := range []struct{ w, h, ww, wh int }{
		{0, 0, 1280, 720},
		{640, 0, 640, 360},
		{0, 1080, 1920, 1080},
		{4000, 0, 1920, 1080},
		{1000, 3000, 360, 1080},
	} {
		if w, h := photoSize(cfg, c.w, c.h); w != c.ww || h != c.wh {
			t.Errorf("photoSize(%d,%d) = %d×%d, want %d×%d", c.w, c.h, w, h, c.ww, c.wh)
		}
	}
}
