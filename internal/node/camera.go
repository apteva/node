package node

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/apteva/node/internal/config"
	"github.com/apteva/node/internal/hw"
)

// maxPhotoBytes keeps a photo result under the server's 1 MiB limit on a
// link frame: base64 grows it by 4/3 (720 KiB → 960 KiB), leaving room for
// the JSON around it.
var maxPhotoBytes = 720 << 10

// captureQuality is the JPEG quality asked of the camera; FitJPEG lowers it
// only when a photo would not fit on the link.
const captureQuality = 85

// SentPhoto is the last photo that left the device, shown to the person
// at the device so a camera is never used unseen.
type SentPhoto struct {
	Capability string    `json:"capability"`
	To         string    `json:"to"`
	At         time.Time `json:"at"`
	Width      int       `json:"width"`
	Height     int       `json:"height"`
}

var schemaCapture = json.RawMessage(`{"type":"object","properties":{` +
	`"width":{"type":"integer","minimum":16,"description":"Photo width in pixels. Omit for the default size; larger than the device allows is reduced."},` +
	`"height":{"type":"integer","minimum":16,"description":"Photo height in pixels. Omit to keep the camera's aspect ratio."}` +
	`},"additionalProperties":false}`)

func (n *Node) cameraTool(c *capability) *tool {
	what := describe(c.cfg, "the camera")
	desc := fmt.Sprintf("Take a photo with %s. Returns a base64 JPEG. Default %d×%d, at most %d×%d.",
		what, c.cfg.Width, c.cfg.Height, c.cfg.MaxWidth, c.cfg.MaxHeight)
	return &tool{spec: toolSpec(c.cfg.ID+"_capture", desc, schemaCapture), cap: c, mutates: true,
		run: func(args json.RawMessage) (any, error) {
			var a struct{ Width, Height int }
			if err := json.Unmarshal(args, &a); err != nil {
				return nil, errors.New(`expected {"width"?: integer, "height"?: integer}`)
			}
			if a.Width < 0 || a.Height < 0 {
				return nil, errors.New("width and height must be positive")
			}
			w, h := photoSize(c.cfg, a.Width, a.Height)
			jpg, err := n.capture(c, w, h)
			if err != nil {
				return nil, err
			}
			out, w, h, err := hw.FitJPEG(jpg, maxPhotoBytes)
			if err != nil {
				return nil, err
			}
			c.mu.Lock()
			c.lastPhoto, c.photoAt = out, time.Now()
			c.mu.Unlock()
			n.sentPhoto(c, w, h)
			return map[string]any{
				"mime_type": "image/jpeg", "width": w, "height": h,
				"data": base64.StdEncoding.EncodeToString(out),
			}, nil
		}}
}

// photoSize resolves an agent's requested size against the camera's
// default and maximum. A missing side keeps the default aspect ratio; a
// request over the maximum is scaled down, keeping its own aspect ratio.
func photoSize(cfg config.Capability, w, h int) (int, int) {
	switch {
	case w == 0 && h == 0:
		w, h = cfg.Width, cfg.Height
	case h == 0:
		h = w * cfg.Height / cfg.Width
	case w == 0:
		w = h * cfg.Width / cfg.Height
	}
	if w > cfg.MaxWidth {
		w, h = cfg.MaxWidth, h*cfg.MaxWidth/w
	}
	if h > cfg.MaxHeight {
		w, h = w*cfg.MaxHeight/h, cfg.MaxHeight
	}
	return max(w, 16), max(h, 16)
}

// capture takes one photo. Captures are serialized device-wide: the
// camera stack opens a camera exclusively, so a second rpicam process
// would fail rather than wait.
func (n *Node) capture(c *capability, w, h int) ([]byte, error) {
	n.camMu.Lock()
	defer n.camMu.Unlock()
	jpg, err := n.hw.Capture(hw.CaptureOptions{
		Index: c.cfg.Index, Width: w, Height: h, Quality: captureQuality,
		Rotation: c.cfg.Rotation, HFlip: c.cfg.HFlip, VFlip: c.cfg.VFlip,
	})
	c.mu.Lock()
	if err != nil {
		c.camErr = err.Error()
	} else {
		c.camErr = ""
	}
	c.mu.Unlock()
	n.changed()
	return jpg, err
}

// detectCameras checks once at start that each declared camera exists, so
// a missing ribbon cable shows on the hardware screen before the first
// photo is asked for.
func (n *Node) detectCameras() {
	var cams []*capability
	for _, c := range n.caps {
		if c.cfg.Kind == config.KindCamera {
			cams = append(cams, c)
		}
	}
	if len(cams) == 0 {
		return
	}
	n.camMu.Lock()
	found, err := n.hw.Cameras()
	n.camMu.Unlock()
	for _, c := range cams {
		msg, model := "", ""
		switch {
		case err != nil:
			msg = "camera check failed: " + err.Error()
		default:
			msg = fmt.Sprintf("no camera found on port %d", c.cfg.Index)
			for _, f := range found {
				if f.Index == c.cfg.Index {
					msg, model = "", f.Model
				}
			}
		}
		c.mu.Lock()
		c.camErr, c.camModel = msg, model
		c.mu.Unlock()
	}
	n.changed()
}

func (n *Node) sentPhoto(c *capability, w, h int) {
	n.mu.Lock()
	to := n.agent.Name
	if to == "" {
		to = "your agent"
	}
	n.lastSent = &SentPhoto{Capability: c.cfg.ID, To: to, At: time.Now(), Width: w, Height: h}
	n.mu.Unlock()
	n.addLine("system", fmt.Sprintf("Sent a photo from %s to %s.", describe(c.cfg, c.cfg.ID), to))
}
