package hw

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"os"
	"strconv"
	"sync"
	"time"
)

// Sim is an in-memory backend. Outputs remember their level, inputs are
// driven through Press, the BME280 at 0x76/0x77 answers with a plausible
// slowly drifting climate, and the camera returns a generated test card.
type Sim struct {
	mu     sync.Mutex
	inputs map[int]*simInput
	start  time.Time
}

func NewSim() *Sim {
	return &Sim{inputs: map[int]*simInput{}, start: time.Now()}
}

func (s *Sim) Name() string { return "sim" }

func (s *Sim) Output(pin int, activeLow bool) (Output, error) {
	return &simOutput{}, nil
}

func (s *Sim) Input(pin int, activeLow bool, onChange func(bool)) (Input, error) {
	in := &simInput{onChange: onChange}
	s.mu.Lock()
	s.inputs[pin] = in
	s.mu.Unlock()
	return in, nil
}

func (s *Sim) Press(pin int, down bool) {
	s.mu.Lock()
	in := s.inputs[pin]
	s.mu.Unlock()
	if in == nil {
		return
	}
	in.mu.Lock()
	changed := in.level != down
	in.level = down
	in.mu.Unlock()
	if changed && in.onChange != nil {
		in.onChange(down)
	}
}

func (s *Sim) Sensor(driver string, bus, addr int) (Sensor, error) {
	if driver != "bme280" {
		return nil, errors.New("sim: unknown sensor driver " + driver)
	}
	return &simClimate{start: s.start}, nil
}

func (s *Sim) Cameras() ([]CameraInfo, error) {
	return []CameraInfo{{Index: 0, Model: "sim"}}, nil
}

func (s *Sim) Capture(o CaptureOptions) ([]byte, error) {
	if o.Index != 0 {
		return nil, errors.New("camera: no camera on port " + strconv.Itoa(o.Index))
	}
	width, height := o.Width, o.Height
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	t := float64(time.Since(s.start).Milliseconds()) / 1000
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			u, v := float64(x)/float64(width), float64(y)/float64(height)
			img.Set(x, y, color.RGBA{
				R: uint8(40 + 120*u),
				G: uint8(60 + 80*v),
				B: uint8(120 + 100*(0.5+0.5*math.Sin(t+u*6))),
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: o.Quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (s *Sim) System() SystemInfo {
	host, _ := os.Hostname()
	temp := 48 + 4*math.Sin(float64(time.Now().Unix())/60)
	up := time.Since(s.start)
	return SystemInfo{Hostname: host, Model: "Simulated Node", CPUTempC: &temp, Uptime: up, UptimeSec: int64(up.Seconds())}
}

func (s *Sim) Close() error { return nil }

type simOutput struct {
	mu sync.Mutex
	on bool
}

func (o *simOutput) Set(on bool) error { o.mu.Lock(); o.on = on; o.mu.Unlock(); return nil }
func (o *simOutput) Get() bool         { o.mu.Lock(); defer o.mu.Unlock(); return o.on }
func (o *simOutput) Close() error      { return nil }

type simInput struct {
	mu       sync.Mutex
	level    bool
	onChange func(bool)
}

func (i *simInput) Get() (bool, error) { i.mu.Lock(); defer i.mu.Unlock(); return i.level, nil }
func (i *simInput) Close() error       { return nil }

type simClimate struct{ start time.Time }

func (c *simClimate) Read() (map[string]float64, error) {
	t := time.Since(c.start).Seconds()
	return map[string]float64{
		"temperature_c": round1(22 + 3*math.Sin(t/90)),
		"humidity_pct":  round1(45 + 8*math.Sin(t/150+1)),
		"pressure_hpa":  round1(1013 + 2*math.Sin(t/300)),
	}, nil
}

func (c *simClimate) Close() error { return nil }

func round1(v float64) float64 { return math.Round(v*10) / 10 }
