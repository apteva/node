package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPersistAndPermissions(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Get().SetupComplete {
		t.Fatal("fresh state must not be complete")
	}
	if err := st.Update(func(s *State) { s.ServerURL, s.Token, s.SetupComplete = "https://a.example", "tok", true }); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, fileName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("state file mode %v, want 0600", info.Mode().Perm())
	}
	again, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if g := again.Get(); g.Token != "tok" || !g.SetupComplete {
		t.Fatalf("reloaded %+v", g)
	}
	if err := again.Reset(); err != nil {
		t.Fatal(err)
	}
	if g := again.Get(); g.Token != "" || g.SetupComplete {
		t.Fatalf("after reset %+v", g)
	}
}

func TestPIN(t *testing.T) {
	st, _ := Open(t.TempDir())
	if !st.CheckPIN("anything") {
		t.Fatal("no PIN set should accept")
	}
	if err := st.SetPIN("12a4"); err == nil {
		t.Fatal("non-digit PIN accepted")
	}
	if err := st.SetPIN("2468"); err != nil {
		t.Fatal(err)
	}
	if st.Get().PinHash == "2468" || st.Get().PinHash == "" {
		t.Fatal("PIN not hashed")
	}
	if st.CheckPIN("1357") || !st.CheckPIN("2468") {
		t.Fatal("PIN check wrong")
	}
	st.SetPIN("")
	if st.HasPIN() {
		t.Fatal("PIN not removed")
	}
}
