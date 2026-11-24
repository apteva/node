//go:build linux

package hw

import (
	"testing"
	"unsafe"
)

// The kernel rejects a request whose size doesn't match the ioctl number,
// so these must match include/uapi/linux/gpio.h exactly.
func TestGPIOStructSizes(t *testing.T) {
	cases := map[string][2]uintptr{
		"gpiochip_info":        {unsafe.Sizeof(gpiochipInfo{}), 68},
		"gpio_v2_line_config":  {unsafe.Sizeof(gpioLineConfig{}), 272},
		"gpio_v2_line_request": {unsafe.Sizeof(gpioLineRequest{}), 592},
		"gpio_v2_line_values":  {unsafe.Sizeof(gpioLineValues{}), 16},
		"gpio_v2_line_event":   {unsafe.Sizeof(gpioLineEvent{}), 48},
	}
	for name, c := range cases {
		if c[0] != c[1] {
			t.Errorf("%s: size %d, want %d", name, c[0], c[1])
		}
	}
}
