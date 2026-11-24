package config

import (
	"strings"
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	c, err := Parse([]byte("capabilities:\n  - {id: climate, kind: i2c_sensor, driver: bme280}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.UI.Listen != "127.0.0.1:7480" || c.Safety.SafeStateAfter.Duration != 30*time.Second {
		t.Errorf("defaults not applied: %+v", c)
	}
	if c.Capabilities[0].Address != 0x76 || c.Capabilities[0].Confirm != ConfirmNone {
		t.Errorf("capability defaults: %+v", c.Capabilities[0])
	}
}

func TestCameraDefaults(t *testing.T) {
	c, err := Parse([]byte("capabilities:\n  - {id: eye, kind: camera}\n  - {id: quiet, kind: camera, index: 1, confirm: none, rotation: 180}\n"))
	if err != nil {
		t.Fatal(err)
	}
	eye, quiet := c.Capabilities[0], c.Capabilities[1]
	if eye.Confirm != ConfirmScreen {
		t.Errorf("camera without confirm must ask on screen, got %q", eye.Confirm)
	}
	if eye.Width != 1280 || eye.Height != 720 || eye.MaxWidth != 1280 || eye.MaxHeight != 720 {
		t.Errorf("camera size defaults: %+v", eye)
	}
	if quiet.Confirm != ConfirmNone {
		t.Errorf("explicit confirm: none must be kept, got %q", quiet.Confirm)
	}
}

func TestValidation(t *testing.T) {
	cases := map[string]string{
		"duplicate pin": `
capabilities:
  - {id: a, kind: relay, pin: 4}
  - {id: b, kind: led, pin: 4}`,
		"reserved id": `
capabilities:
  - {id: system, kind: relay, pin: 4}`,
		"bad kind": `
capabilities:
  - {id: a, kind: servo, pin: 4}`,
		"unknown confirm mode": `
capabilities:
  - {id: a, kind: relay, pin: 4, confirm: button}`,
		"safe_state on input": `
capabilities:
  - {id: a, kind: button, pin: 4, safe_state: off}`,
		"camera rotated 90": `
capabilities:
  - {id: eye, kind: camera, rotation: 90}`,
		"camera max below default": `
capabilities:
  - {id: eye, kind: camera, width: 1920, height: 1080, max_width: 1280}`,
		"camera bad index": `
capabilities:
  - {id: eye, kind: camera, index: 9}`,
		"bad id": `
capabilities:
  - {id: Lamp, kind: relay, pin: 4}`,
	}
	for name, y := range cases {
		if _, err := Parse([]byte(y)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestExampleConfigParses(t *testing.T) {
	c, err := Load("../../node.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(c.Server.URL, "http") {
		t.Errorf("server url %q", c.Server.URL)
	}
}
