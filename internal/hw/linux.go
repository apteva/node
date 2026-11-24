//go:build linux

package hw

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Linux drives a Raspberry Pi through kernel interfaces only: the GPIO
// character device (uAPI v2), /dev/i2c-N, and rpicam-still for the camera.
// No sysfs GPIO, no pigpio daemon, no cgo.
type Linux struct {
	chip *os.File
	mu   sync.Mutex
	open []interface{ Close() error }
}

// NewLinux opens the GPIO chip. chipPath "auto" picks the SoC's pinctrl
// chip, which moved between gpiochip0 and gpiochip4 across Pi models and
// kernel versions.
func NewLinux(chipPath string) (*Linux, error) {
	if chipPath == "auto" {
		p, err := findPinctrlChip()
		if err != nil {
			return nil, err
		}
		chipPath = p
	}
	f, err := os.OpenFile(chipPath, os.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", chipPath, err)
	}
	return &Linux{chip: f}, nil
}

func (l *Linux) Name() string { return "linux:" + l.chip.Name() }

// --- GPIO character device uAPI v2 (include/uapi/linux/gpio.h) ---

const (
	gpioLineFlagActiveLow   = 1 << 1
	gpioLineFlagInput       = 1 << 2
	gpioLineFlagOutput      = 1 << 3
	gpioLineFlagEdgeRising  = 1 << 4
	gpioLineFlagEdgeFalling = 1 << 5
	gpioLineFlagBiasPullUp  = 1 << 8

	gpioAttrIDOutputValues = 2
	gpioAttrIDDebounce     = 3

	gpioEventRising = 1
)

type gpiochipInfo struct {
	Name  [32]byte
	Label [32]byte
	Lines uint32
}

type gpioLineAttribute struct {
	ID      uint32
	Padding uint32
	Value   uint64 // flags | values | debounce_period_us
}

type gpioLineConfigAttribute struct {
	Attr gpioLineAttribute
	Mask uint64
}

type gpioLineConfig struct {
	Flags    uint64
	NumAttrs uint32
	Padding  [5]uint32
	Attrs    [10]gpioLineConfigAttribute
}

type gpioLineRequest struct {
	Offsets         [64]uint32
	Consumer        [32]byte
	Config          gpioLineConfig
	NumLines        uint32
	EventBufferSize uint32
	Padding         [5]uint32
	Fd              int32
}

type gpioLineValues struct {
	Bits uint64
	Mask uint64
}

type gpioLineEvent struct {
	TimestampNs uint64
	ID          uint32
	Offset      uint32
	Seqno       uint32
	LineSeqno   uint32
	Padding     [6]uint32
}

func iowr(nr, size uintptr) uintptr { return 3<<30 | size<<16 | 0xB4<<8 | nr }
func ior(nr, size uintptr) uintptr  { return 2<<30 | size<<16 | 0xB4<<8 | nr }

var (
	ioctlChipInfo  = ior(0x01, unsafe.Sizeof(gpiochipInfo{}))
	ioctlGetLine   = iowr(0x07, unsafe.Sizeof(gpioLineRequest{}))
	ioctlGetValues = iowr(0x0E, unsafe.Sizeof(gpioLineValues{}))
	ioctlSetValues = iowr(0x0F, unsafe.Sizeof(gpioLineValues{}))
)

func ioctl(fd uintptr, req uintptr, arg unsafe.Pointer) error {
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, fd, req, uintptr(arg)); e != 0 {
		return e
	}
	return nil
}

func findPinctrlChip() (string, error) {
	paths, _ := filepath.Glob("/dev/gpiochip*")
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		var info gpiochipInfo
		err = ioctl(f.Fd(), ioctlChipInfo, unsafe.Pointer(&info))
		f.Close()
		if err == nil && strings.HasPrefix(cstr(info.Label[:]), "pinctrl-") {
			return p, nil
		}
	}
	return "", errors.New("no pinctrl GPIO chip found; set hardware.gpio_chip")
}

func cstr(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

func (l *Linux) requestLine(pin int, cfg gpioLineConfig) (*os.File, error) {
	var req gpioLineRequest
	req.Offsets[0] = uint32(pin)
	req.NumLines = 1
	copy(req.Consumer[:], "apteva-node")
	req.Config = cfg
	if err := ioctl(l.chip.Fd(), ioctlGetLine, unsafe.Pointer(&req)); err != nil {
		return nil, fmt.Errorf("request gpio %d: %w", pin, err)
	}
	return os.NewFile(uintptr(req.Fd), fmt.Sprintf("gpio%d", pin)), nil
}

func (l *Linux) track(c interface{ Close() error }) {
	l.mu.Lock()
	l.open = append(l.open, c)
	l.mu.Unlock()
}

type linuxOutput struct {
	f  *os.File
	mu sync.Mutex
	on bool
}

func (l *Linux) Output(pin int, activeLow bool) (Output, error) {
	cfg := gpioLineConfig{Flags: gpioLineFlagOutput}
	if activeLow {
		cfg.Flags |= gpioLineFlagActiveLow
	}
	// Start inactive.
	cfg.NumAttrs = 1
	cfg.Attrs[0] = gpioLineConfigAttribute{Attr: gpioLineAttribute{ID: gpioAttrIDOutputValues, Value: 0}, Mask: 1}
	f, err := l.requestLine(pin, cfg)
	if err != nil {
		return nil, err
	}
	o := &linuxOutput{f: f}
	l.track(o)
	return o, nil
}

func (o *linuxOutput) Set(on bool) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	v := gpioLineValues{Mask: 1}
	if on {
		v.Bits = 1
	}
	if err := ioctl(o.f.Fd(), ioctlSetValues, unsafe.Pointer(&v)); err != nil {
		return err
	}
	o.on = on
	return nil
}

func (o *linuxOutput) Get() bool    { o.mu.Lock(); defer o.mu.Unlock(); return o.on }
func (o *linuxOutput) Close() error { return o.f.Close() }

type linuxInput struct {
	f *os.File
}

func (l *Linux) Input(pin int, activeLow bool, onChange func(bool)) (Input, error) {
	cfg := gpioLineConfig{Flags: gpioLineFlagInput | gpioLineFlagBiasPullUp | gpioLineFlagEdgeRising | gpioLineFlagEdgeFalling}
	if activeLow {
		cfg.Flags |= gpioLineFlagActiveLow
	}
	cfg.NumAttrs = 1
	cfg.Attrs[0] = gpioLineConfigAttribute{Attr: gpioLineAttribute{ID: gpioAttrIDDebounce, Value: uint64(debounce.Microseconds())}, Mask: 1}
	f, err := l.requestLine(pin, cfg)
	if err != nil {
		return nil, err
	}
	in := &linuxInput{f: f}
	l.track(in)
	go func() {
		buf := make([]byte, unsafe.Sizeof(gpioLineEvent{}))
		for {
			if _, err := f.Read(buf); err != nil {
				return // closed
			}
			ev := (*gpioLineEvent)(unsafe.Pointer(&buf[0]))
			if onChange != nil {
				onChange(ev.ID == gpioEventRising)
			}
		}
	}()
	return in, nil
}

func (i *linuxInput) Get() (bool, error) {
	v := gpioLineValues{Mask: 1}
	if err := ioctl(i.f.Fd(), ioctlGetValues, unsafe.Pointer(&v)); err != nil {
		return false, err
	}
	return v.Bits&1 == 1, nil
}

func (i *linuxInput) Close() error { return i.f.Close() }

// --- I2C ---

const i2cSlave = 0x0703

type linuxI2C struct {
	f  *os.File
	mu sync.Mutex
}

func openI2C(bus, addr int) (*linuxI2C, error) {
	f, err := os.OpenFile(fmt.Sprintf("/dev/i2c-%d", bus), os.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, f.Fd(), i2cSlave, uintptr(addr)); e != 0 {
		f.Close()
		return nil, fmt.Errorf("i2c address 0x%02x: %w", addr, e)
	}
	return &linuxI2C{f: f}, nil
}

func (d *linuxI2C) ReadReg(reg byte, n int) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, err := d.f.Write([]byte{reg}); err != nil {
		return nil, err
	}
	buf := make([]byte, n)
	if _, err := d.f.Read(buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func (d *linuxI2C) WriteReg(reg, val byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.f.Write([]byte{reg, val})
	return err
}

func (d *linuxI2C) Close() error { return d.f.Close() }

func (l *Linux) Sensor(driver string, bus, addr int) (Sensor, error) {
	if driver != "bme280" {
		return nil, fmt.Errorf("unknown sensor driver %q", driver)
	}
	dev, err := openI2C(bus, addr)
	if err != nil {
		return nil, err
	}
	s, err := NewBME280(dev)
	if err != nil {
		dev.Close()
		return nil, err
	}
	l.track(s)
	return s, nil
}

// --- Camera ---

// rpicamBin returns the installed rpicam tool, trying the pre-Bookworm
// libcamera-* name too.
func rpicamBin(tool string) (string, error) {
	for _, bin := range []string{"rpicam-" + tool, "libcamera-" + tool} {
		if _, err := exec.LookPath(bin); err == nil {
			return bin, nil
		}
	}
	return "", ErrNoCameraSoftware
}

func (l *Linux) Capture(o CaptureOptions) ([]byte, error) {
	bin, err := rpicamBin("still")
	if err != nil {
		return nil, fmt.Errorf("camera: %w", err)
	}
	out, stderr, err := runTimed(bin, captureArgs(o), 15*time.Second)
	if err != nil {
		return nil, fmt.Errorf("camera: %w%s", err, tail(stderr))
	}
	return out, nil
}

func (l *Linux) Cameras() ([]CameraInfo, error) {
	bin, err := rpicamBin("hello")
	if err != nil {
		return nil, err
	}
	out, stderr, err := runTimed(bin, []string{"--list-cameras"}, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("list cameras: %w%s", err, tail(stderr))
	}
	// Recent versions print the list on stderr, older ones on stdout.
	return parseCameraList(string(out) + "\n" + string(stderr)), nil
}

// runTimed runs a command, killing it after timeout.
func runTimed(bin string, args []string, timeout time.Duration) (stdout, stderr []byte, err error) {
	cmd := exec.Command(bin, args...)
	var out, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return out.Bytes(), errBuf.Bytes(), err
	case <-time.After(timeout):
		cmd.Process.Kill()
		<-done
		return nil, errBuf.Bytes(), errors.New("timed out")
	}
}

// tail keeps the last line of a tool's stderr for the error message.
func tail(stderr []byte) string {
	lines := strings.Split(strings.TrimSpace(string(stderr)), "\n")
	if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
		return ": " + last
	}
	return ""
}

// --- System ---

func (l *Linux) System() SystemInfo {
	host, _ := os.Hostname()
	info := SystemInfo{Hostname: host}
	if b, err := os.ReadFile("/proc/device-tree/model"); err == nil {
		info.Model = cstr(b)
	}
	if b, err := os.ReadFile("/sys/class/thermal/thermal_zone0/temp"); err == nil {
		if milli, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
			t := float64(milli) / 1000
			info.CPUTempC = &t
		}
	}
	if b, err := os.ReadFile("/proc/uptime"); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			if s, err := strconv.ParseFloat(f[0], 64); err == nil {
				info.Uptime = time.Duration(s * float64(time.Second))
				info.UptimeSec = int64(s)
			}
		}
	}
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			if v, err := strconv.ParseFloat(f[0], 64); err == nil {
				info.LoadAvg1 = &v
			}
		}
	}
	return info
}

func (l *Linux) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, c := range l.open {
		c.Close()
	}
	l.open = nil
	return l.chip.Close()
}
