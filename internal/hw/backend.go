// Package hw is the boundary between the daemon and physical hardware.
// A Backend hands out pins, I2C devices and the camera; the Linux backend
// talks to the kernel directly, the sim backend fakes everything so the
// daemon and UI can be developed on a laptop.
package hw

import "time"

type Backend interface {
	Name() string
	Output(pin int, activeLow bool) (Output, error)
	// Input watches a pin; onChange fires (debounced) with the logical level.
	Input(pin int, activeLow bool, onChange func(active bool)) (Input, error)
	Sensor(driver string, bus, addr int) (Sensor, error)
	Capture(o CaptureOptions) ([]byte, error) // JPEG
	// Cameras lists the cameras the system can see.
	Cameras() ([]CameraInfo, error)
	System() SystemInfo
	Close() error
}

type Output interface {
	Set(on bool) error
	Get() bool
	Close() error
}

type Input interface {
	Get() (bool, error)
	Close() error
}

// Sensor returns named readings, e.g. {"temperature_c": 21.4}.
type Sensor interface {
	Read() (map[string]float64, error)
	Close() error
}

type I2CDevice interface {
	ReadReg(reg byte, n int) ([]byte, error)
	WriteReg(reg, val byte) error
	Close() error
}

type SystemInfo struct {
	Hostname  string        `json:"hostname"`
	Model     string        `json:"model"`
	CPUTempC  *float64      `json:"cpu_temp_c,omitempty"`
	Uptime    time.Duration `json:"-"`
	UptimeSec int64         `json:"uptime_s"`
	LoadAvg1  *float64      `json:"load_avg_1m,omitempty"`
}

// Simulator is implemented by the sim backend so the UI can stand in for
// fingers on buttons during development.
type Simulator interface {
	Press(pin int, down bool)
}

const debounce = 30 * time.Millisecond
