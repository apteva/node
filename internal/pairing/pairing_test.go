package pairing

import "testing"

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"agents.example.com":           "https://agents.example.com",
		" https://agents.example.com/": "https://agents.example.com",
		"192.168.1.20:5280":            "http://192.168.1.20:5280",
		"127.0.0.1:7490":               "http://127.0.0.1:7490",
		"apteva.local:5280":            "http://apteva.local:5280",
		"example.com:8443":             "https://example.com:8443",
		"http://10.0.0.5:5280/?x=1":    "http://10.0.0.5:5280",
	}
	for in, want := range cases {
		got, err := Normalize(in)
		if err != nil || got != want {
			t.Errorf("Normalize(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "ftp://x.example", "://"} {
		if _, err := Normalize(bad); err == nil {
			t.Errorf("Normalize(%q) should fail", bad)
		}
	}
}
