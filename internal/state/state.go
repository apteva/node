// Package state is what the device learns during setup and must remember
// across reboots: whether setup is finished, which server it is paired with,
// its device token, and the settings PIN. It lives in one 0600 JSON file in
// the state directory; node.yaml stays the owner's hardware declaration.
package state

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

const fileName = "state.json"

type State struct {
	SetupComplete bool      `json:"setup_complete"`
	ServerURL     string    `json:"server_url,omitempty"`
	Token         string    `json:"token,omitempty"`
	Agent         string    `json:"agent,omitempty"`
	PairedAt      time.Time `json:"paired_at,omitzero"`
	PinSalt       string    `json:"pin_salt,omitempty"`
	PinHash       string    `json:"pin_hash,omitempty"`
}

type Store struct {
	dir string
	mu  sync.Mutex
	s   State
}

// Open loads the state file, or starts empty if there is none yet.
func Open(dir string) (*Store, error) {
	st := &Store{dir: dir}
	b, err := os.ReadFile(filepath.Join(dir, fileName))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return st, nil
	case err != nil:
		return nil, err
	}
	if err := json.Unmarshal(b, &st.s); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(dir, fileName), err)
	}
	return st, nil
}

func (st *Store) Get() State {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.s
}

// Update applies fn and writes the result atomically.
func (st *Store) Update(fn func(*State)) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	next := st.s
	fn(&next)
	if err := st.write(next); err != nil {
		return err
	}
	st.s = next
	return nil
}

func (st *Store) write(s State) error {
	if err := os.MkdirAll(st.dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(st.dir, ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(st.dir, fileName))
}

// Reset forgets everything (factory reset). node.yaml is untouched.
func (st *Store) Reset() error {
	return st.Update(func(s *State) { *s = State{} })
}

// --- PIN ---

var pinPattern = regexp.MustCompile(`^[0-9]{4,8}$`)

const pinIterations = 120_000

func ValidPIN(pin string) bool { return pinPattern.MatchString(pin) }

func hashPIN(pin string, salt []byte) (string, error) {
	k, err := pbkdf2.Key(sha256.New, pin, salt, pinIterations, 32)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(k), nil
}

// SetPIN stores a new PIN; an empty pin removes it.
func (st *Store) SetPIN(pin string) error {
	if pin == "" {
		return st.Update(func(s *State) { s.PinSalt, s.PinHash = "", "" })
	}
	if !ValidPIN(pin) {
		return errors.New("PIN must be 4 to 8 digits")
	}
	salt := make([]byte, 16)
	rand.Read(salt)
	h, err := hashPIN(pin, salt)
	if err != nil {
		return err
	}
	return st.Update(func(s *State) { s.PinSalt, s.PinHash = hex.EncodeToString(salt), h })
}

func (st *Store) HasPIN() bool { return st.Get().PinHash != "" }

func (st *Store) CheckPIN(pin string) bool {
	s := st.Get()
	if s.PinHash == "" {
		return true
	}
	salt, err := hex.DecodeString(s.PinSalt)
	if err != nil {
		return false
	}
	h, err := hashPIN(pin, salt)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(h), []byte(s.PinHash)) == 1
}
