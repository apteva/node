// Package link keeps the Node's one outbound WebSocket to its Apteva
// instance alive: dial, hello, read/write loops, reconnect with backoff,
// and a bounded queue for things that should survive a short outage.
package link

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/apteva/node/internal/proto"
)

// Path the server serves the link on.
const Path = "/api/devices/link"

type State string

const (
	StateUnpaired     State = "unpaired" // no server or token configured
	StateConnecting   State = "connecting"
	StateConnected    State = "connected"
	StateDisconnected State = "disconnected" // waiting to retry
	StateRejected     State = "rejected"     // server refused the credential
	// Reported by the node, never by a Client: deliberately not connected.
	StateStandalone State = "standalone"
)

type Status struct {
	State   State     `json:"state"`
	Server  string    `json:"server,omitempty"`
	Since   time.Time `json:"since"`
	Error   string    `json:"error,omitempty"`
	RetryAt time.Time `json:"retry_at,omitzero"`
}

type Client struct {
	ServerURL string
	Token     string
	Hello     func() proto.Hello
	OnFrame   func(proto.Frame)
	OnStatus  func(Status)

	mu     sync.Mutex
	status Status
	out    chan proto.Frame // live connection only
	queue  []proto.Frame    // held while offline
}

const (
	queueLimit   = 200
	writeTimeout = 10 * time.Second
	pongWait     = 60 * time.Second
	pingEvery    = 25 * time.Second
	maxBackoff   = 30 * time.Second
)

// queueable frames are replayed after reconnect; everything else (results
// for invokes that died with the old socket) is dropped.
func queueable(typ string) bool {
	return typ == proto.TypeEvent || typ == proto.TypeMessage || typ == proto.TypeState
}

func (c *Client) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

func (c *Client) setStatus(s Status) {
	s.Server = c.ServerURL
	if s.Since.IsZero() {
		s.Since = time.Now()
	}
	c.mu.Lock()
	c.status = s
	c.mu.Unlock()
	if c.OnStatus != nil {
		c.OnStatus(s)
	}
}

// Send delivers a frame now, queues it if offline, or drops it. It never
// blocks the caller on the network.
func (c *Client) Send(f proto.Frame) {
	c.mu.Lock()
	out := c.out
	if out == nil {
		if queueable(f.Type) {
			c.queue = append(c.queue, f)
			if len(c.queue) > queueLimit {
				c.queue = c.queue[len(c.queue)-queueLimit:]
			}
		}
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	select {
	case out <- f:
	default:
		// Writer is wedged; the connection is about to be torn down.
	}
}

// SendLive leaves durability to the event outbox and never queues a second copy.
func (c *Client) SendLive(f proto.Frame) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.out == nil {
		return false
	}
	select {
	case c.out <- f:
		return true
	default:
		return false
	}
}

func (c *Client) Queued() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.queue)
}

// Run blocks until ctx ends.
func (c *Client) Run(ctx context.Context) {
	if c.ServerURL == "" || c.Token == "" {
		c.setStatus(Status{State: StateUnpaired})
		<-ctx.Done()
		return
	}
	wsURL, err := linkURL(c.ServerURL)
	if err != nil {
		c.setStatus(Status{State: StateRejected, Error: err.Error()})
		<-ctx.Done()
		return
	}
	backoff := time.Second
	for ctx.Err() == nil {
		c.setStatus(Status{State: StateConnecting})
		started := time.Now()
		err := c.session(ctx, wsURL)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second // it was a healthy session, retry quickly
		}
		wait := backoff + time.Duration(rand.Int64N(int64(backoff/2)+1))
		st := StateDisconnected
		var rej *rejectedError
		if errors.As(err, &rej) {
			st = StateRejected
			wait = maxBackoff
		}
		c.setStatus(Status{State: st, Error: errString(err), RetryAt: time.Now().Add(wait)})
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

type rejectedError struct{ code int }

func (e *rejectedError) Error() string {
	return fmt.Sprintf("server refused this device (HTTP %d) — re-pair it", e.code)
}

func (c *Client) session(ctx context.Context, wsURL string) error {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+c.Token)
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	conn, resp, err := dialer.DialContext(ctx, wsURL, h)
	if err != nil {
		if resp != nil && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) {
			return &rejectedError{code: resp.StatusCode}
		}
		return err
	}
	defer conn.Close()

	hello, err := proto.New(proto.TypeHello, "", c.Hello())
	if err != nil {
		return err
	}
	conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	if err := conn.WriteJSON(hello); err != nil {
		return err
	}

	out := make(chan proto.Frame, 64)
	c.mu.Lock()
	backlog := c.queue
	c.queue = nil
	c.out = out
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.out = nil
		c.mu.Unlock()
	}()

	c.setStatus(Status{State: StateConnected})

	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	writeErr := make(chan error, 1)
	go func() { writeErr <- writeLoop(sctx, conn, out, backlog) }()

	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	readErr := make(chan error, 1)
	go func() {
		for {
			var f proto.Frame
			if err := conn.ReadJSON(&f); err != nil {
				readErr <- err
				return
			}
			conn.SetReadDeadline(time.Now().Add(pongWait))
			if c.OnFrame != nil {
				c.OnFrame(f)
			}
		}
	}()

	select {
	case err := <-readErr:
		if websocket.IsCloseError(err, 4003) {
			return &rejectedError{code: http.StatusUnauthorized}
		}
		return err
	case err := <-writeErr:
		return err
	case <-ctx.Done():
		conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseGoingAway, "node shutting down"), time.Now().Add(time.Second))
		return ctx.Err()
	}
}

func writeLoop(ctx context.Context, conn *websocket.Conn, out <-chan proto.Frame, backlog []proto.Frame) error {
	write := func(f proto.Frame) error {
		conn.SetWriteDeadline(time.Now().Add(writeTimeout))
		return conn.WriteJSON(f)
	}
	for _, f := range backlog {
		if err := write(f); err != nil {
			return err
		}
	}
	ping := time.NewTicker(pingEvery)
	defer ping.Stop()
	for {
		select {
		case f := <-out:
			if err := write(f); err != nil {
				return err
			}
		case <-ping.C:
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeTimeout)); err != nil {
				return err
			}
		case <-ctx.Done():
			return nil
		}
	}
}

// linkURL turns https://host[/base] into wss://host[/base]/api/devices/link.
func linkURL(server string) (string, error) {
	u, err := url.Parse(server)
	if err != nil {
		return "", fmt.Errorf("server url: %w", err)
	}
	switch u.Scheme {
	case "https", "wss":
		u.Scheme = "wss"
	case "http", "ws":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("server url %q: need http(s)://", server)
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + Path
	return u.String(), nil
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	var ce *websocket.CloseError
	if errors.As(err, &ce) {
		if ce.Text != "" {
			return ce.Text
		}
		return fmt.Sprintf("connection closed (%d)", ce.Code)
	}
	return err.Error()
}

// Decode is a small helper for frame payloads.
func Decode[T any](f proto.Frame) (T, error) {
	var v T
	if len(f.Data) == 0 {
		return v, nil
	}
	err := json.Unmarshal(f.Data, &v)
	return v, err
}
