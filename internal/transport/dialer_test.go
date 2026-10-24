package transport

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewDialerRejectsProxyURLCredentials(t *testing.T) {
	_, err := NewDialer(DialOptions{Mode: ProxyModeHTTPConnect, URL: "http://alice:secret@proxy.example:8080"})
	if err == nil || !strings.Contains(err.Error(), "proxy URL must not contain credentials") {
		t.Fatalf("NewDialer() error = %v", err)
	}
}

func TestNewDialerLoadsPasswordFromEnvironmentOrFile(t *testing.T) {
	t.Setenv("TUNNELMESH_PROXY_PASSWORD", "from-env")
	dialer, err := NewDialer(DialOptions{
		Mode:        ProxyModeHTTPConnect,
		URL:         "http://proxy.example:8080",
		Username:    "alice",
		PasswordEnv: "TUNNELMESH_PROXY_PASSWORD",
	})
	if err != nil {
		t.Fatal(err)
	}
	if dialer.password != "from-env" {
		t.Fatalf("password = %q, want environment value", dialer.password)
	}

	path := filepath.Join(t.TempDir(), "proxy.secret")
	if err := os.WriteFile(path, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dialer, err = NewDialer(DialOptions{Mode: ProxyModeSOCKS5, URL: "socks5://proxy.example:1080", PasswordFile: path})
	if err != nil {
		t.Fatal(err)
	}
	if dialer.password != "from-file" {
		t.Fatalf("password = %q, want file value", dialer.password)
	}
}

func TestDialContextDirectHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dialer, err := NewDialer(DialOptions{Mode: ProxyModeDirect})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dialer.DialContext(ctx, "tcp", "127.0.0.1:1"); err == nil {
		t.Fatal("DialContext() error = nil, want cancellation")
	}
}

func TestDialContextHTTPConnectSendsProxyAuthorization(t *testing.T) {
	checked := make(chan string, 1)
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			t.Errorf("proxy method = %s, want CONNECT", r.Method)
		}
		checked <- r.Header.Get("Proxy-Authorization")
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("proxy does not support hijacking")
			return
		}
		conn, writer, err := hijacker.Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_, _ = writer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = writer.Flush()
		_ = conn.Close()
	}))
	defer proxyServer.Close()

	dialer, err := NewDialer(DialOptions{
		Mode:        ProxyModeHTTPConnect,
		URL:         proxyServer.URL,
		Username:    "alice",
		PasswordEnv: "TUNNELMESH_PROXY_PASSWORD",
	})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := dialer.DialContext(context.Background(), "tcp", "target.internal:443")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	select {
	case authorization := <-checked:
		if authorization == "" {
			t.Fatal("proxy authorization header is empty")
		}
	case <-time.After(time.Second):
		t.Fatal("proxy did not receive CONNECT")
	}
}

func TestDialContextForSchemeUsesHTTPSProxyOnAlternateTLSPort(t *testing.T) {
	seen := make(chan string, 2)
	newProxy := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen <- name
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Error("proxy does not support hijacking")
				return
			}
			conn, writer, err := hijacker.Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_, _ = writer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
			_ = writer.Flush()
			_ = conn.Close()
		}))
	}
	httpProxy := newProxy("http")
	defer httpProxy.Close()
	httpsProxy := newProxy("https")
	defer httpsProxy.Close()
	t.Setenv("HTTP_PROXY", httpProxy.URL)
	t.Setenv("HTTPS_PROXY", httpsProxy.URL)
	t.Setenv("ALL_PROXY", "")
	t.Setenv("NO_PROXY", "")

	dialer, err := NewDialer(DialOptions{Mode: ProxyModeSystem})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := dialer.DialContextForScheme(context.Background(), "tcp", net.JoinHostPort("target.internal", "8443"), "wss")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	select {
	case proxy := <-seen:
		if proxy != "https" {
			t.Fatalf("system proxy = %q, want https proxy", proxy)
		}
	case <-time.After(time.Second):
		t.Fatal("system proxy did not receive CONNECT")
	}
	select {
	case <-seen:
		t.Fatal("system proxy used more than one proxy for one dial")
	case <-time.After(50 * time.Millisecond):
	}
}
