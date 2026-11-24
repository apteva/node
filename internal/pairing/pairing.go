// Package pairing is the device side of joining an Apteva instance, a
// device-code flow like signing in a TV app:
//
//	POST /api/devices/pair/start  {name, version, hardware}
//	  → {pairing_id, user_code, verify_url, expires_in, interval}
//	POST /api/devices/pair/poll   {pairing_id}
//	  → {status: pending|approved|denied|expired, token?, agent?}
//
// The device shows user_code (and verify_url as a QR); a person approves it
// in the dashboard and picks the project and agent. pairing_id never leaves
// the device, so only it can collect the token.
package pairing

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Hardware struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Description string `json:"description,omitempty"`
}

type StartRequest struct {
	Name     string     `json:"name"`
	Version  string     `json:"version"`
	Hardware []Hardware `json:"hardware"`
}

type Started struct {
	PairingID string `json:"pairing_id"`
	UserCode  string `json:"user_code"`
	VerifyURL string `json:"verify_url"`
	ExpiresIn int    `json:"expires_in"` // seconds
	Interval  int    `json:"interval"`   // seconds between polls
}

type PollResult struct {
	Status string `json:"status"`
	Token  string `json:"token,omitempty"`
	Agent  string `json:"agent,omitempty"`
}

var httpClient = &http.Client{Timeout: 10 * time.Second}

// Normalize accepts what a person types: "agents.example.com",
// "http://192.168.1.20:5280/". No scheme means https, except for a bare
// local address with a port, which is almost always a dev server.
func Normalize(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("enter the address of your Apteva instance")
	}
	if !strings.Contains(s, "://") {
		host := s
		if i := strings.IndexAny(host, "/"); i >= 0 {
			host = host[:i]
		}
		scheme := "https://"
		if h, port, err := net.SplitHostPort(host); err == nil && port != "" && isLocal(h) {
			scheme = "http://"
		}
		s = scheme + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("%q isn't a valid address", raw)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", errors.New("the address must start with https:// or http://")
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	u.RawQuery, u.Fragment = "", ""
	return u.String(), nil
}

func isLocal(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".local") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

// Probe checks there is an Apteva instance at base. clockSynced lets the
// error explain the classic Pi failure: no clock yet, so TLS can't verify.
func Probe(ctx context.Context, base string, clockSynced bool) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/health", nil)
	resp, err := httpClient.Do(req)
	if err != nil {
		return friendly(err, clockSynced)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("that address answered, but it doesn't look like Apteva (HTTP %d)", resp.StatusCode)
	}
	return nil
}

func Start(ctx context.Context, base string, r StartRequest) (Started, error) {
	var out Started
	if err := post(ctx, base+"/api/devices/pair/start", r, &out); err != nil {
		return out, err
	}
	if out.PairingID == "" || out.UserCode == "" {
		return out, errors.New("the server didn't return a pairing code")
	}
	if out.Interval <= 0 {
		out.Interval = 2
	}
	if out.ExpiresIn <= 0 {
		out.ExpiresIn = 600
	}
	return out, nil
}

func Poll(ctx context.Context, base, pairingID string) (PollResult, error) {
	var out PollResult
	err := post(ctx, base+"/api/devices/pair/poll", map[string]string{"pairing_id": pairingID}, &out)
	return out, err
}

func post(ctx context.Context, u string, body, out any) error {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return friendly(err, true)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return errors.New("this Apteva instance doesn't support device pairing yet — update it")
	case resp.StatusCode == http.StatusGone:
		return json.Unmarshal([]byte(`{"status":"expired"}`), out)
	case resp.StatusCode >= 300:
		var e struct {
			Error string `json:"error"`
		}
		json.Unmarshal(data, &e)
		if e.Error == "" {
			e.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return errors.New(e.Error)
	}
	return json.Unmarshal(data, out)
}

func friendly(err error, clockSynced bool) error {
	var unknownAuth x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	var dnsErr *net.DNSError
	var opErr *net.OpError
	switch {
	case errors.As(err, &invalid) && invalid.Reason == x509.Expired && !clockSynced:
		return errors.New("secure connection failed because this device's clock isn't set yet — wait for the network time, then try again")
	case errors.As(err, &invalid), errors.As(err, &unknownAuth):
		return errors.New("the server's certificate isn't trusted")
	case errors.As(err, &dnsErr):
		return errors.New("couldn't find that address — check the spelling and the network")
	case errors.As(err, &opErr):
		return errors.New("couldn't reach that address — is it online and reachable from here?")
	case errors.Is(err, context.DeadlineExceeded):
		return errors.New("the server took too long to answer")
	}
	return err
}
