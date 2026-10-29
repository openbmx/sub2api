package websearch

import (
	"strings"
	"testing"
)

// A proxy URL that fails to parse used to be quoted into the error, password and
// all, and url.Error repeats the input a second time.
func TestNewHTTPClientDoesNotEchoTheProxyPassword(t *testing.T) {
	_, err := newHTTPClient("http://user:s3cret-proxy-pass@bad host:8080")
	if err == nil {
		t.Fatal("an unparseable proxy URL must be rejected")
	}
	if strings.Contains(err.Error(), "s3cret-proxy-pass") {
		t.Fatalf("error leaks the proxy password: %v", err)
	}
}
