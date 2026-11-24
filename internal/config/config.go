// Package config loads node.yaml, the owner's declaration of what this
// device is and which pieces of hardware the agent may touch. Anything not
// declared here does not exist as far as the agent is concerned.
package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Capability kinds the daemon knows how to drive.
const (
	KindRelay  = "relay"
	KindLED    = "led"
	KindButton = "button"
	KindSensor = "i2c_sensor"
	KindCamera = "camera"
)

// Confirmation modes for agent-initiated actions.
const (
	ConfirmNone   = "none"
	ConfirmScreen = "screen" // tap Allow on the touchscreen
	ConfirmHold   = "hold"   // press and hold Allow on the touchscreen: for actions with real-world consequences
)

type Config struct {
	Name string `yaml:"name"`
	// Where setup results live (pairing, token, PIN). Default /var/lib/apteva-node.
	StateDir     string       `yaml:"state_dir"`
	Server       Server       `yaml:"server"`
	UI           UI           `yaml:"ui"`
	Hardware     Hardware     `yaml:"hardware"`
	Safety       Safety       `yaml:"safety"`
	Capabilities []Capability `yaml:"capabilities"`
}

type Server struct {
	// URL of the main Apteva instance, e.g. https://agents.example.com.
	URL string `yaml:"url"`
	// Device credential issued at pairing. TokenFile wins when both are set.
	Token     string `yaml:"token"`
	TokenFile string `yaml:"token_file"`
}

type UI struct {
	// Local address for the on-device UI. Keep it on loopback.
	Listen string `yaml:"listen"`
}

type Hardware struct {
	// "auto" picks the SoC's pinctrl chip; otherwise a path like /dev/gpiochip0.
	GPIOChip string `yaml:"gpio_chip"`
	I2CBus   int    `yaml:"i2c_bus"`
	// Sim forces the simulated backend even on a Pi.
	Sim bool `yaml:"sim"`
}

type Safety struct {
	// After the link has been down this long, capabilities with a safe_state
	// are driven to it.
	SafeStateAfter Duration `yaml:"safe_state_after"`
	// How long an approval waits before it is denied.
	ConfirmTimeout Duration `yaml:"confirm_timeout"`
}

type Capability struct {
	ID          string `yaml:"id"`
	Kind        string `yaml:"kind"`
	Description string `yaml:"description"`

	// GPIO kinds.
	Pin       int    `yaml:"pin"`
	ActiveLow bool   `yaml:"active_low"`
	SafeState string `yaml:"safe_state"` // "", "on" or "off"

	// i2c_sensor.
	Driver  string      `yaml:"driver"`
	Address int         `yaml:"address"`
	Events  []Threshold `yaml:"events"`
	Poll    Duration    `yaml:"poll"`

	// camera. Width and Height are the default photo size; an agent may ask
	// for any size up to MaxWidth × MaxHeight (default: the default size).
	// Index is the camera port as `rpicam-hello --list-cameras` numbers it.
	// Rotation (0 or 180) and the flips correct for how it is mounted.
	Index     int  `yaml:"index"`
	Width     int  `yaml:"width"`
	Height    int  `yaml:"height"`
	MaxWidth  int  `yaml:"max_width"`
	MaxHeight int  `yaml:"max_height"`
	Rotation  int  `yaml:"rotation"`
	HFlip     bool `yaml:"hflip"`
	VFlip     bool `yaml:"vflip"`

	Confirm string `yaml:"confirm"`
}

// Threshold turns a sensor reading into an event that can wake the agent.
type Threshold struct {
	Field string   `yaml:"field"` // e.g. temperature_c
	Above *float64 `yaml:"above"`
	Below *float64 `yaml:"below"`
}

// Duration accepts "30s"-style strings in YAML.
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	d.Duration = v
	return nil
}

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(raw)
}

func Parse(raw []byte) (*Config, error) {
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Name == "" {
		c.Name = "node"
	}
	if c.StateDir == "" {
		c.StateDir = "/var/lib/apteva-node"
	}
	if c.UI.Listen == "" {
		c.UI.Listen = "127.0.0.1:7480"
	}
	if c.Hardware.GPIOChip == "" {
		c.Hardware.GPIOChip = "auto"
	}
	if c.Hardware.I2CBus == 0 {
		c.Hardware.I2CBus = 1
	}
	if c.Safety.SafeStateAfter.Duration == 0 {
		c.Safety.SafeStateAfter.Duration = 30 * time.Second
	}
	if c.Safety.ConfirmTimeout.Duration == 0 {
		c.Safety.ConfirmTimeout.Duration = 30 * time.Second
	}
	for i := range c.Capabilities {
		cp := &c.Capabilities[i]
		if cp.Confirm == "" {
			// A camera is never silent unless the owner writes confirm: none.
			cp.Confirm = ConfirmNone
			if cp.Kind == KindCamera {
				cp.Confirm = ConfirmScreen
			}
		}
		switch cp.Kind {
		case KindSensor:
			if cp.Poll.Duration == 0 {
				cp.Poll.Duration = 10 * time.Second
			}
			if cp.Driver == "bme280" && cp.Address == 0 {
				cp.Address = 0x76
			}
		case KindCamera:
			if cp.Width == 0 {
				cp.Width = 1280
			}
			if cp.Height == 0 {
				cp.Height = 720
			}
			if cp.MaxWidth == 0 {
				cp.MaxWidth = cp.Width
			}
			if cp.MaxHeight == 0 {
				cp.MaxHeight = cp.Height
			}
		}
	}
}

func (c *Config) validate() error {
	if !idPattern.MatchString(c.Name) {
		return fmt.Errorf("name %q: use lowercase letters, digits and _", c.Name)
	}
	seenID := map[string]bool{"system": true}
	seenPin := map[int]string{}
	for _, cp := range c.Capabilities {
		if !idPattern.MatchString(cp.ID) {
			return fmt.Errorf("capability id %q: use lowercase letters, digits and _", cp.ID)
		}
		if seenID[cp.ID] {
			return fmt.Errorf("capability %q: duplicate or reserved id", cp.ID)
		}
		seenID[cp.ID] = true
		switch cp.Kind {
		case KindRelay, KindLED, KindButton:
			if cp.Pin < 0 || cp.Pin > 63 {
				return fmt.Errorf("capability %q: pin %d out of range", cp.ID, cp.Pin)
			}
			if other, ok := seenPin[cp.Pin]; ok {
				return fmt.Errorf("capability %q: pin %d already used by %q", cp.ID, cp.Pin, other)
			}
			seenPin[cp.Pin] = cp.ID
		case KindSensor:
			if cp.Driver != "bme280" {
				return fmt.Errorf("capability %q: unknown sensor driver %q", cp.ID, cp.Driver)
			}
		case KindCamera:
			if err := validateCamera(cp); err != nil {
				return fmt.Errorf("capability %q: %w", cp.ID, err)
			}
		default:
			return fmt.Errorf("capability %q: unknown kind %q", cp.ID, cp.Kind)
		}
		switch cp.SafeState {
		case "", "on", "off":
		default:
			return fmt.Errorf("capability %q: safe_state must be on or off", cp.ID)
		}
		if cp.SafeState != "" && cp.Kind != KindRelay && cp.Kind != KindLED {
			return fmt.Errorf("capability %q: safe_state only applies to outputs", cp.ID)
		}
		switch cp.Confirm {
		case ConfirmNone, ConfirmScreen, ConfirmHold:
		default:
			return fmt.Errorf("capability %q: confirm must be none, screen or hold", cp.ID)
		}
	}
	return nil
}

// Largest side rpicam-still is asked for; above every Pi camera sensor.
const maxCameraSide = 10000

func validateCamera(cp Capability) error {
	if cp.Index < 0 || cp.Index > 7 {
		return fmt.Errorf("index %d out of range", cp.Index)
	}
	for _, v := range []int{cp.Width, cp.Height, cp.MaxWidth, cp.MaxHeight} {
		if v < 16 || v > maxCameraSide {
			return fmt.Errorf("photo sizes must be between 16 and %d pixels", maxCameraSide)
		}
	}
	if cp.MaxWidth < cp.Width || cp.MaxHeight < cp.Height {
		return fmt.Errorf("max_width/max_height must be at least width/height")
	}
	if cp.Rotation != 0 && cp.Rotation != 180 {
		return fmt.Errorf("rotation must be 0 or 180 (the camera stack can't rotate by 90; use hflip/vflip or mount it differently)")
	}
	return nil
}

func (c *Config) Capability(id string) *Capability {
	for i := range c.Capabilities {
		if c.Capabilities[i].ID == id {
			return &c.Capabilities[i]
		}
	}
	return nil
}

// ServerToken resolves the device credential.
func (c *Config) ServerToken() (string, error) {
	if c.Server.TokenFile != "" {
		b, err := os.ReadFile(c.Server.TokenFile)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	return c.Server.Token, nil
}
