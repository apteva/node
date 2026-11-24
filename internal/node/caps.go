package node

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/apteva/node/internal/config"
	"github.com/apteva/node/internal/hw"
	"github.com/apteva/node/internal/proto"
)

// capability is one declared piece of hardware, bound to its backend handle.
type capability struct {
	cfg    config.Capability
	out    hw.Output
	in     hw.Input
	sensor hw.Sensor

	mu        sync.Mutex
	reading   map[string]float64
	readAt    time.Time
	readErr   string
	armed     map[int]bool // threshold index → ready to fire
	pressed   bool
	lastPhoto []byte
	photoAt   time.Time
	camErr    string // detection or last capture failure
	camModel  string // sensor name from detection
}

func toolSpec(name, desc string, schema json.RawMessage) proto.Tool {
	return proto.Tool{Name: name, Description: desc, InputSchema: schema}
}

// toolSpec plus the function that runs it.
type tool struct {
	spec    proto.Tool
	cap     *capability
	mutates bool // needs the capability's confirm policy
	run     func(args json.RawMessage) (any, error)
}

var (
	schemaEmpty = json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
	schemaOnOff = json.RawMessage(`{"type":"object","properties":{"on":{"type":"boolean","description":"true to switch on, false to switch off"}},"required":["on"],"additionalProperties":false}`)
)

func describe(c config.Capability, fallback string) string {
	if c.Description != "" {
		return c.Description
	}
	return fallback
}

func (n *Node) toolsFor(c *capability) []*tool {
	id := c.cfg.ID
	switch c.cfg.Kind {
	case config.KindRelay, config.KindLED:
		noun := map[string]string{config.KindRelay: "relay", config.KindLED: "LED"}[c.cfg.Kind]
		what := describe(c.cfg, fmt.Sprintf("%s on pin %d", noun, c.cfg.Pin))
		return []*tool{
			{spec: proto.Tool{Name: id + "_set", Description: "Switch " + what + " on or off.", InputSchema: schemaOnOff}, cap: c, mutates: true,
				run: func(args json.RawMessage) (any, error) {
					var a struct {
						On *bool `json:"on"`
					}
					if err := json.Unmarshal(args, &a); err != nil || a.On == nil {
						return nil, fmt.Errorf(`expected {"on": true|false}`)
					}
					return n.setOutput(c, *a.On, "agent")
				}},
			{spec: proto.Tool{Name: id + "_get", Description: "Report whether " + what + " is on.", InputSchema: schemaEmpty}, cap: c,
				run: func(json.RawMessage) (any, error) { return map[string]bool{"on": c.out.Get()}, nil }},
		}
	case config.KindButton:
		what := describe(c.cfg, fmt.Sprintf("button on pin %d", c.cfg.Pin))
		return []*tool{
			{spec: proto.Tool{Name: id + "_get", Description: "Report whether " + what + " is currently held down.", InputSchema: schemaEmpty}, cap: c,
				run: func(json.RawMessage) (any, error) {
					v, err := c.in.Get()
					return map[string]bool{"pressed": v}, err
				}},
		}
	case config.KindSensor:
		what := describe(c.cfg, "climate sensor ("+c.cfg.Driver+")")
		return []*tool{
			{spec: proto.Tool{Name: id + "_read", Description: "Take a fresh reading from " + what + ". Returns temperature_c, humidity_pct, pressure_hpa.", InputSchema: schemaEmpty}, cap: c, mutates: true,
				run: func(json.RawMessage) (any, error) { return n.readSensor(c) }},
		}
	case config.KindCamera:
		return []*tool{n.cameraTool(c)}
	}
	return nil
}

func (n *Node) systemTool() *tool {
	return &tool{
		spec: proto.Tool{Name: "system_info", Description: "Report this device's model, CPU temperature, load and uptime.", InputSchema: schemaEmpty},
		run:  func(json.RawMessage) (any, error) { return n.hw.System(), nil },
	}
}

func (n *Node) setOutput(c *capability, on bool, by string) (any, error) {
	if err := c.out.Set(on); err != nil {
		return nil, err
	}
	n.changed()
	n.sendFrame(proto.TypeState, "", proto.State{Capability: c.cfg.ID, Value: map[string]any{"on": on, "by": by}})
	return map[string]bool{"on": on}, nil
}

func (n *Node) readSensor(c *capability) (map[string]float64, error) {
	r, err := c.sensor.Read()
	c.mu.Lock()
	c.readAt = time.Now()
	if err != nil {
		c.readErr = err.Error()
	} else {
		c.reading, c.readErr = r, ""
	}
	c.mu.Unlock()
	n.changed()
	if err != nil {
		return nil, err
	}
	n.checkThresholds(c, r)
	return r, nil
}

// checkThresholds fires an event when a reading crosses a threshold and
// re-arms once it is back on the other side, so a sensor sitting above the
// line doesn't wake the agent every poll.
func (n *Node) checkThresholds(c *capability, r map[string]float64) {
	for i, th := range c.cfg.Events {
		v, ok := r[th.Field]
		if !ok {
			continue
		}
		var crossed bool
		var kind string
		var limit float64
		switch {
		case th.Above != nil:
			crossed, kind, limit = v > *th.Above, "above", *th.Above
		case th.Below != nil:
			crossed, kind, limit = v < *th.Below, "below", *th.Below
		default:
			continue
		}
		c.mu.Lock()
		armed, seen := c.armed[i]
		if !seen {
			armed = true
		}
		fire := crossed && armed
		c.armed[i] = !crossed
		c.mu.Unlock()
		if fire {
			n.emit(c.cfg.ID, th.Field+"_"+kind, map[string]any{"field": th.Field, "value": v, "threshold": limit})
		}
	}
}

func (n *Node) pollSensor(c *capability, stop <-chan struct{}) {
	n.readSensor(c)
	t := time.NewTicker(c.cfg.Poll.Duration)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			n.readSensor(c)
		case <-stop:
			return
		}
	}
}

// view is what the device UI renders for a capability.
type capView struct {
	ID          string             `json:"id"`
	Kind        string             `json:"kind"`
	Description string             `json:"description,omitempty"`
	Pin         *int               `json:"pin,omitempty"`
	Confirm     string             `json:"confirm"`
	SafeState   string             `json:"safe_state,omitempty"`
	On          *bool              `json:"on,omitempty"`
	Pressed     *bool              `json:"pressed,omitempty"`
	Reading     map[string]float64 `json:"reading,omitempty"`
	ReadAt      time.Time          `json:"read_at,omitzero"`
	Error       string             `json:"error,omitempty"`
	PhotoAt     time.Time          `json:"photo_at,omitzero"`
	CameraModel string             `json:"camera_model,omitempty"`
	Thresholds  []config.Threshold `json:"thresholds,omitempty"`
}

func (c *capability) view() capView {
	v := capView{ID: c.cfg.ID, Kind: c.cfg.Kind, Description: c.cfg.Description, Confirm: c.cfg.Confirm, SafeState: c.cfg.SafeState}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch c.cfg.Kind {
	case config.KindRelay, config.KindLED:
		pin, on := c.cfg.Pin, c.out.Get()
		v.Pin, v.On = &pin, &on
	case config.KindButton:
		pin, p := c.cfg.Pin, c.pressed
		v.Pin, v.Pressed = &pin, &p
	case config.KindSensor:
		v.Reading, v.ReadAt, v.Error, v.Thresholds = c.reading, c.readAt, c.readErr, c.cfg.Events
	case config.KindCamera:
		v.PhotoAt, v.Error, v.CameraModel = c.photoAt, c.camErr, c.camModel
	}
	return v
}
