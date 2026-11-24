package netmgr

import (
	"context"
	"errors"
	"testing"
)

func TestParseWiFiList(t *testing.T) {
	out := ` :Studio:82:WPA2
*:Studio:64:WPA2
 :Caf\:e Guest:58:
 ::40:WPA2
 :Studio 5G:70:WPA2 WPA3
 :Open One:20:--
`
	nets := ParseWiFiList(out)
	if len(nets) != 4 {
		t.Fatalf("got %d networks: %+v", len(nets), nets)
	}
	if nets[0].SSID != "Studio" || !nets[0].InUse || nets[0].Signal != 82 {
		t.Errorf("in-use network should come first with its strongest signal: %+v", nets[0])
	}
	var cafe, open *Network
	for i := range nets {
		switch nets[i].SSID {
		case "Caf:e Guest":
			cafe = &nets[i]
		case "Open One":
			open = &nets[i]
		}
	}
	if cafe == nil || cafe.Secure {
		t.Errorf("escaped colon / open network not parsed: %+v", nets)
	}
	if open == nil || open.Secure {
		t.Errorf("'--' security means open: %+v", open)
	}
}

func TestSimConnectPersists(t *testing.T) {
	dir := t.TempDir()
	s := NewSim(dir)
	if s.Status(context.Background()).Online {
		t.Fatal("sim should start offline")
	}
	if err := s.Connect(context.Background(), "Studio", "wrong"); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("want wrong password, got %v", err)
	}
	if err := s.Connect(context.Background(), "Studio", "correct-horse"); err != nil {
		t.Fatal(err)
	}
	if st := NewSim(dir).Status(context.Background()); st.WiFiSSID != "Studio" || !st.Online {
		t.Fatalf("joined network not remembered: %+v", st)
	}
}
