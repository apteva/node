//go:build !linux

package hw

import "errors"

// NewLinux is only available on Linux; elsewhere the daemon runs on Sim.
func NewLinux(chipPath string) (Backend, error) {
	return nil, errors.New("real hardware needs Linux; run with --sim")
}
