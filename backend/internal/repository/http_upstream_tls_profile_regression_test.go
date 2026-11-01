package repository

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"

	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
)

func TestClaudeRepairTLSCancelClosesProxySocket(t *testing.T) {
	for _, scheme := range []string{"http", "https", "socks5"} {
		t.Run(scheme, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = listener.Close() }()
			accepted := make(chan net.Conn, 1)
			go func() {
				conn, err := listener.Accept()
				if err == nil {
					accepted <- conn
				}
			}()
			upstream := NewHTTPUpstream(nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://review.example.invalid/test", nil)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				resp, err := upstream.DoWithTLS(req, scheme+"://"+listener.Addr().String(), 901, 1, &tlsfingerprint.Profile{Name: "review"})
				if resp != nil {
					_ = resp.Body.Close()
				}
				done <- err
			}()
			var peer net.Conn
			select {
			case peer = <-accepted:
			case <-time.After(time.Second):
				t.Fatal("proxy not reached")
			}
			defer func() { _ = peer.Close() }()
			if err := peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			switch scheme {
			case "http":
				if _, err := http.ReadRequest(bufio.NewReader(peer)); err != nil {
					t.Fatal(err)
				}
			case "https":
				header := make([]byte, 5)
				if _, err := io.ReadFull(peer, header); err != nil {
					t.Fatal(err)
				}
				if _, err := io.ReadFull(peer, make([]byte, binary.BigEndian.Uint16(header[3:]))); err != nil {
					t.Fatal(err)
				}
			default:
				if _, err := io.ReadFull(peer, make([]byte, 3)); err != nil {
					t.Fatal(err)
				}
			}
			cancel()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("cancelled request succeeded")
				}
			case <-time.After(time.Second):
				t.Fatal("DoWithTLS did not return on cancellation")
			}
			if err := peer.SetReadDeadline(time.Now().Add(300 * time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			_, err = peer.Read(make([]byte, 1))
			if err != io.EOF {
				t.Fatalf("cancelled dial retained its proxy socket: %v", err)
			}
			t.Log("DoWithTLS cancellation also closed the stalled proxy socket")
		})
	}
}

func TestClaudeRepairTLSProfileChangeCapturedOnWire(t *testing.T) {
	captured := make(chan []uint16, 4)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	server.TLS = &tls.Config{GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		captured <- append([]uint16(nil), hello.CipherSuites...)
		return nil, nil
	}}
	server.StartTLS()
	defer server.Close()
	upstream := NewHTTPUpstream(nil)
	profiles := []*tlsfingerprint.Profile{
		{Name: "review-profile-A", CipherSuites: []uint16{0x1301}},
		{Name: "review-profile-B", CipherSuites: []uint16{0x1302}},
	}
	observed := [][]uint16{}
	for _, profile := range profiles {
		req, err := http.NewRequest(http.MethodGet, server.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		req = req.WithContext(ctx)
		resp, err := upstream.DoWithTLS(req, "", 902, 1, profile)
		cancel()
		if resp != nil {
			_ = resp.Body.Close()
		}
		if err == nil {
			t.Fatal("local untrusted certificate unexpectedly accepted")
		}
		select {
		case suites := <-captured:
			observed = append(observed, suites)
		case <-time.After(time.Second):
			t.Fatal("no actual ClientHello captured")
		}
	}
	if len(observed[0]) != 1 || observed[0][0] != 0x1301 || len(observed[1]) != 1 || observed[1][0] != 0x1302 {
		t.Fatalf("TLS profile change did not reach the wire: first=%x second=%x", observed[0], observed[1])
	}
}

func TestHTTPProxyTLSFingerprintEndToEnd(t *testing.T) {
	if os.Getenv("SUB2API_TEST_TLS_PROXY_CHILD") != "1" {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(ctx, exe, "-test.run=^TestHTTPProxyTLSFingerprintEndToEnd$", "-test.v")
		cmd.Env = append(os.Environ(), "SUB2API_TEST_TLS_PROXY_CHILD=1", "GODEBUG=x509usefallbackroots=1")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("HTTPS tunnel subprocess: %v\n%s", err, output)
		}
		t.Log(string(output))
		return
	}
	captured := make(chan []uint16, 4)
	target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "fingerprint-ok") }))
	target.TLS = &tls.Config{GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		captured <- append([]uint16(nil), hello.CipherSuites...)
		return nil, nil
	}}
	target.StartTLS()
	defer target.Close()
	proxyCalls := make(chan bool, 4)
	proxy := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		valid := r.Method == http.MethodConnect && r.Host == target.Listener.Addr().String() && r.Header.Get("Proxy-Authorization") == "Basic "+base64.StdEncoding.EncodeToString([]byte("test-user:test-password"))
		proxyCalls <- valid
		if !valid {
			http.Error(w, "bad tunnel", 400)
			return
		}
		destination, err := net.Dial("tcp", r.Host)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = destination.Close() }()
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "hijacking unavailable", http.StatusInternalServerError)
			return
		}
		client, buffer, err := hijacker.Hijack()
		if err != nil {
			return
		}
		defer func() { _ = client.Close() }()
		if _, err = io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			return
		}
		go func() { _, _ = io.Copy(destination, buffer); _ = destination.Close() }()
		_, _ = io.Copy(client, destination)
	}))
	defer proxy.Close()
	roots := x509.NewCertPool()
	roots.AddCert(target.Certificate())
	roots.AddCert(proxy.Certificate())
	// Isolate fallback roots in this child process; production certificate checks stay enabled.
	x509.SetFallbackRoots(roots)
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxyURL.User = url.UserPassword("test-user", "test-password")
	upstream, ok := NewHTTPUpstream(nil).(*httpUpstreamService)
	if !ok {
		t.Fatal("unexpected HTTP upstream implementation")
	}
	defer func() {
		for _, entry := range upstream.clients {
			entry.client.CloseIdleConnections()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	requestCtx, cancelRequest := context.WithCancel(ctx)
	defer cancelRequest()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, target.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := upstream.DoWithTLS(req, proxyURL.String(), 903, 1, &tlsfingerprint.Profile{Name: "HTTPS tunnel", CipherSuites: []uint16{0x1302}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || string(result) != "fingerprint-ok" {
		t.Fatalf("bad tunnel response %q: %v", result, err)
	}
	if !<-proxyCalls {
		t.Fatal("proxy authentication or CONNECT target was wrong")
	}
	suites := <-captured
	if len(suites) != 1 || suites[0] != 0x1302 {
		t.Fatalf("selected profile bypassed through HTTPS proxy: %x", suites)
	}
	cancelRequest()
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, target.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err = upstream.DoWithTLS(req, proxyURL.String(), 903, 1, &tlsfingerprint.Profile{Name: "HTTPS tunnel", CipherSuites: []uint16{0x1302}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || len(proxyCalls) != 0 || len(captured) != 0 {
		t.Fatalf("cancellation after a completed request broke TLS connection reuse: %v", err)
	}
}
