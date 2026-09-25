package proxyurl

import (
	"strings"
	"testing"
)

// url.Parse errors quote the raw URL, so formatting the error — with %v as
// much as with %w — used to put the proxy password into the message.
func TestParse_InvalidURLErrorOmitsCredentials(t *testing.T) {
	_, _, err := Parse("http://user:s3cret-pass@proxy.example.com:bad-port")
	if err == nil {
		t.Fatal("an invalid port must be rejected")
	}
	if strings.Contains(err.Error(), "s3cret-pass") {
		t.Fatalf("error leaks the proxy password: %v", err)
	}
}

func TestRedact(t *testing.T) {
	cases := map[string]string{
		"":                              "",
		"http://proxy.example.com:8080": "http://proxy.example.com:8080",
		"socks5h://user:s3cret@proxy.example.com:1080": "socks5h://user:xxxxx@proxy.example.com:1080",
		" http://user:s3cret@proxy.example.com ":       "http://user:xxxxx@proxy.example.com",
		"http://user:s3cret@proxy.example.com:bad":     "<invalid proxy URL>",
	}
	for raw, want := range cases {
		if got := Redact(raw); got != want {
			t.Errorf("Redact(%q) = %q, want %q", raw, got, want)
		}
	}
}
