package openresty_test

import (
	"context"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
)

// http2Directive matches the nginx directive only, never the prose that forbids
// it in the template header comment.
var http2Directive = regexp.MustCompile(`(?m)^\s*http2\s+on;`)

// readArtifact loads a shipped OpenResty artifact. Tests run with the package
// directory as working directory, so names are relative.
func readArtifact(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// TestProxyEntryArtifactsMatchServerContract pins the values that break a
// deployment silently when only one side changes: the trusted header names, the
// internal entry address and the Lua read timeout. Lua and nginx cannot be unit
// tested from Go, but the constants they must agree with can.
func TestProxyEntryArtifactsMatchServerContract(t *testing.T) {
	defaults, err := config.Load(context.Background(), config.ConfigOptions{})
	if err != nil {
		t.Fatalf("load defaults: %v", err)
	}
	entry := defaults.Server.ProxyEntry
	host, port, err := net.SplitHostPort(entry.Listen)
	if err != nil {
		t.Fatalf("default listen %q: %v", entry.Listen, err)
	}

	lua := readArtifact(t, "tunnelmesh_proxy_entry.lua")
	conf := readArtifact(t, "tunnelmesh-proxy.conf.example")

	for _, header := range []string{entry.RouteHeader, entry.ClientIPHeader, entry.ClientPortHeader} {
		if !strings.Contains(lua, header) {
			t.Errorf("lua does not inject trusted header %q", header)
		}
		if !strings.Contains(conf, "proxy_set_header "+header+" ") {
			t.Errorf("conf example does not forward trusted header %q", header)
		}
	}

	wantReadTimeout := int64((entry.IdleTimeout + 30*time.Second) / time.Millisecond)
	for _, marker := range []string{
		`host = "` + host + `", -- __TM_INTERNAL_HOST__`,
		`port = ` + port + `, -- __TM_INTERNAL_PORT__`,
		`read_timeout_ms = ` + strconv.FormatInt(wantReadTimeout, 10) + `, -- __TM_READ_TIMEOUT_MS__`,
	} {
		if !strings.Contains(lua, marker) {
			t.Errorf("lua CONFIG drifted from the server defaults; want marker %q", marker)
		}
	}

	if http2Directive.MatchString(conf) {
		t.Error("the tp-* server block must not enable http2: ngx.req.socket(true) is unavailable on HTTP/2 downstreams and proxy_connect does not support CONNECT over HTTP/2")
	}
	if !strings.Contains(conf, "listen __LISTEN__ ssl;") {
		t.Error("conf example must keep the listen placeholder together with ssl")
	}
	// Proxy-Authorization is hop-by-hop: nginx drops it on the way upstream
	// unless the config re-adds it explicitly.
	if !strings.Contains(conf, "proxy_set_header Proxy-Authorization $http_proxy_authorization;") {
		t.Error("conf example must forward Proxy-Authorization explicitly, otherwise every non-CONNECT proxy request fails with 407")
	}
	if !strings.Contains(conf, "access_by_lua_file __LUA_FILE__;") {
		t.Error("conf example must run the Lua mover from the server-level access phase")
	}
	if !strings.Contains(conf, "lua_check_client_abort on;") {
		t.Error("conf example must enable lua_check_client_abort so an aborted client releases the tunnel")
	}
	for _, placeholder := range []string{"__LISTEN__", "__SERVER_NAME_REGEX__", "__SSL_CERT__", "__SSL_CERT_KEY__", "__LUA_FILE__", "__INTERNAL_UPSTREAM__", "__EDGE_ALLOW__"} {
		if !strings.Contains(conf, placeholder) {
			t.Errorf("conf example lost placeholder %q", placeholder)
		}
	}

	if !strings.Contains(lua, "settimeouts(") {
		t.Error("lua must set cosocket timeouts explicitly; the 60s default kills long tunnels")
	}
	if !strings.Contains(lua, "ngx.exit(444)") {
		t.Error("lua must finish tunnels with ngx.exit(444) so nginx closes the connection without appending its own response")
	}
	if !strings.Contains(lua, "ngx.on_abort") {
		t.Error("lua must register ngx.on_abort so a client disconnect closes the upstream socket immediately")
	}
	// The mover must stay policy-free: any of these means logic leaked out of Go.
	for _, forbidden := range []string{"proxy_basic", "ParseCIDR", "sha256", "ngx.shared", "credentials"} {
		if strings.Contains(lua, forbidden) {
			t.Errorf("lua must not carry policy logic; found %q", forbidden)
		}
	}
}

// TestProxyConnectDockerfilePinsCompatibleVersions guards the pairing rule from
// the upstream module README: an OpenResty release only builds with the patch
// listed for its nginx core, and that patch only takes effect when the module
// config defines NGX_HTTP_PROXY_CONNECT. Bumping one without the other yields a
// binary that answers 405 to every CONNECT.
func TestProxyConnectDockerfilePinsCompatibleVersions(t *testing.T) {
	dockerfile := readArtifact(t, "Dockerfile.proxy-connect")
	for _, want := range []string{
		"OPENRESTY_VERSION=1.25.3.1",
		"PROXY_CONNECT_VERSION=v0.0.7",
		"PROXY_CONNECT_PATCH=proxy_connect_rewrite_102101.patch",
		"--add-module=/build/ngx_http_proxy_connect_module",
	} {
		if !strings.Contains(dockerfile, want) {
			t.Errorf("Dockerfile.proxy-connect must pin %q", want)
		}
	}
	configureIdx := strings.Index(dockerfile, "./configure")
	patchIdx := strings.Index(dockerfile, "patch -d")
	makeIdx := strings.Index(dockerfile, "make -j")
	if configureIdx < 0 || patchIdx < 0 || makeIdx < 0 {
		t.Fatalf("Dockerfile must contain ./configure, patch -d and make -j")
	}
	if !(configureIdx < patchIdx && patchIdx < makeIdx) {
		t.Error("OpenResty build order must be ./configure -> patch -> make; configure is what unpacks build/nginx-<ver>/")
	}
	if !strings.Contains(dockerfile, "USER openresty:openresty") {
		t.Error("runtime stage must not run as root")
	}
}
