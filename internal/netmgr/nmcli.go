package netmgr

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// NMCLI drives NetworkManager. The daemon user needs the polkit rule in
// deploy/ to change connections; reading status works without it.
type NMCLI struct{}

func NewNMCLI() *NMCLI { return &NMCLI{} }

func run(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "nmcli", args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(msg)
	}
	return out.String(), nil
}

// splitTerse splits one line of `nmcli -t` output, which escapes ':' and '\'
// inside fields with a backslash.
func splitTerse(line string) []string {
	var fields []string
	var cur strings.Builder
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '\\' && i+1 < len(line):
			i++
			cur.WriteByte(line[i])
		case c == ':':
			fields = append(fields, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	return append(fields, cur.String())
}

func (NMCLI) Status(ctx context.Context) Status {
	st := Status{ClockSynced: clockSynced(ctx)}
	out, err := run(ctx, "-t", "-f", "TYPE,STATE,CONNECTION", "device")
	if err != nil {
		// No NetworkManager: report what the kernel knows, but setup can't manage Wi-Fi.
		st.Online = hasRoutableAddress()
		return st
	}
	st.Supported = true
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := splitTerse(line)
		if len(f) < 3 || f[1] != "connected" {
			continue
		}
		switch f[0] {
		case "ethernet":
			st.Ethernet, st.Online = true, true
		case "wifi":
			st.WiFiSSID, st.Online = f[2], true
		}
	}
	if st.WiFiSSID != "" {
		if nets, err := parseWiFiList(ctx, false); err == nil {
			for _, n := range nets {
				if n.InUse {
					st.WiFiSignal = n.Signal
				}
			}
		}
	}
	return st
}

func (NMCLI) Scan(ctx context.Context) ([]Network, error) {
	return parseWiFiList(ctx, true)
}

func parseWiFiList(ctx context.Context, rescan bool) ([]Network, error) {
	r := "no"
	if rescan {
		r = "yes"
	}
	out, err := run(ctx, "-t", "-f", "IN-USE,SSID,SIGNAL,SECURITY", "device", "wifi", "list", "--rescan", r)
	if err != nil {
		return nil, err
	}
	return ParseWiFiList(out), nil
}

// ParseWiFiList turns `nmcli -t -f IN-USE,SSID,SIGNAL,SECURITY device wifi
// list` into networks: hidden SSIDs dropped, one entry per SSID (strongest).
func ParseWiFiList(out string) []Network {
	best := map[string]Network{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := splitTerse(line)
		if len(f) < 4 || f[1] == "" {
			continue
		}
		sig, _ := strconv.Atoi(f[2])
		n := Network{SSID: f[1], Signal: sig, Secure: f[3] != "" && f[3] != "--", InUse: f[0] == "*"}
		// Several access points can share an SSID: keep the strongest signal,
		// and mark the SSID in use if any of them is.
		if old, ok := best[n.SSID]; ok {
			n.Signal = max(n.Signal, old.Signal)
			n.InUse = n.InUse || old.InUse
		}
		best[n.SSID] = n
	}
	nets := make([]Network, 0, len(best))
	for _, n := range best {
		nets = append(nets, n)
	}
	sort.Slice(nets, func(i, j int) bool {
		if nets[i].InUse != nets[j].InUse {
			return nets[i].InUse
		}
		return nets[i].Signal > nets[j].Signal
	})
	return nets
}

func (NMCLI) Connect(ctx context.Context, ssid, password string) error {
	// The password is passed as an argument; this runs on a single-purpose
	// device where only root and the daemon user can list processes' args.
	args := []string{"device", "wifi", "connect", ssid}
	if password != "" {
		args = append(args, "password", password)
	}
	_, err := run(ctx, args...)
	if err != nil {
		low := strings.ToLower(err.Error())
		if strings.Contains(low, "secrets were required") || strings.Contains(low, "802-1x") || strings.Contains(low, "password") {
			return ErrWrongPassword
		}
		if strings.Contains(low, "not authorized") {
			return fmt.Errorf("the daemon isn't allowed to change Wi-Fi (install deploy/50-apteva-node-network.rules): %w", err)
		}
	}
	return err
}

func clockSynced(ctx context.Context) bool {
	cmd := exec.CommandContext(ctx, "timedatectl", "show", "-p", "NTPSynchronized", "--value")
	out, err := cmd.Output()
	if err != nil {
		// No systemd-timesyncd to ask: trust the clock if it isn't obviously unset.
		return time.Now().Year() >= 2025
	}
	return strings.TrimSpace(string(out)) == "yes"
}

func hasRoutableAddress() bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if ip, ok := a.(*net.IPNet); ok && !ip.IP.IsLoopback() && !ip.IP.IsLinkLocalUnicast() {
			return true
		}
	}
	return false
}
