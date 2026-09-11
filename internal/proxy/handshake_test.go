package proxy_test

import (
	"bytes"
	"net"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/proxy"
)

func TestParseHTTPConnect(t *testing.T) {
	req, err := proxy.ParseHTTPConnect([]byte("CONNECT db.internal:5432 HTTP/1.1\r\nHost: db.internal:5432\r\n\r\n"))
	if err != nil || req.Host != "db.internal" || req.Port != 5432 {
		t.Fatalf("request=%+v err=%v", req, err)
	}
	if _, err := proxy.ParseHTTPConnect([]byte("CONNECT evil\r\nX: y\r\n\r\n")); err == nil {
		t.Fatal("malformed CONNECT accepted")
	}
}

func TestParseSOCKS5Connect(t *testing.T) {
	request, err := proxy.ParseSOCKS5Connect([]byte{5, 1, 0, 5, 1, 0, 1, 127, 0, 0, 1, 0x1f, 0x90})
	if err != nil || request.Command != proxy.SOCKS5Connect {
		t.Fatalf("request=%+v err=%v", request, err)
	}
	_ = net.IPv4(127, 0, 0, 1)
}

func TestReadSOCKS5Methods(t *testing.T) {
	methods, err := proxy.ReadSOCKS5Methods(bytes.NewReader([]byte{5, 2, 0, 2}))
	if err != nil || len(methods) != 2 || methods[0] != 0 || methods[1] != 2 {
		t.Fatalf("methods=%v err=%v", methods, err)
	}
	if _, err := proxy.ReadSOCKS5Methods(bytes.NewReader([]byte{5, 0})); err == nil {
		t.Fatal("empty method list was accepted")
	}
	if _, err := proxy.ReadSOCKS5Methods(bytes.NewReader([]byte{4, 1, 0})); err == nil {
		t.Fatal("invalid SOCKS version was accepted")
	}
}

func TestSOCKS5MethodSelection(t *testing.T) {
	if !proxy.SupportsSOCKS5NoAuth([]byte{1, 0, 2}) {
		t.Fatal("no-auth method was not detected")
	}
	if proxy.SupportsSOCKS5NoAuth([]byte{1, 2}) {
		t.Fatal("no-auth was incorrectly detected")
	}
	if !proxy.SupportsSOCKS5UsernamePassword([]byte{0, 2}) {
		t.Fatal("username/password method was not detected")
	}
	if proxy.SupportsSOCKS5UsernamePassword([]byte{0, 1}) {
		t.Fatal("username/password was incorrectly detected")
	}
	if got := proxy.EncodeSOCKS5MethodSelection(0xff); !bytes.Equal(got, []byte{5, 0xff}) {
		t.Fatalf("method selection=%x", got)
	}
}

func TestReadSOCKS5UsernamePassword(t *testing.T) {
	raw := append([]byte{1, 5}, []byte("alice")...)
	raw = append(raw, 6)
	raw = append(raw, []byte("secret")...)
	credentials, err := proxy.ReadSOCKS5UsernamePassword(bytes.NewReader(raw))
	if err != nil || credentials.Username != "alice" || credentials.Password != "secret" {
		t.Fatalf("credentials=%+v err=%v", credentials, err)
	}
	if _, err := proxy.ReadSOCKS5UsernamePassword(bytes.NewReader([]byte{2, 0, 0})); err == nil {
		t.Fatal("invalid RFC 1929 version was accepted")
	}
	if _, err := proxy.ReadSOCKS5UsernamePassword(bytes.NewReader([]byte{1, 255})); err == nil {
		t.Fatal("truncated username was accepted")
	}
}

func TestEncodeSOCKS5UsernamePasswordReply(t *testing.T) {
	if got := proxy.EncodeSOCKS5UsernamePasswordReply(true); !bytes.Equal(got, []byte{1, 0}) {
		t.Fatalf("success reply=%x", got)
	}
	if got := proxy.EncodeSOCKS5UsernamePasswordReply(false); !bytes.Equal(got, []byte{1, 1}) {
		t.Fatalf("failure reply=%x", got)
	}
}

func TestReadSOCKS5RequestSupportsAddressTypes(t *testing.T) {
	requests := []struct {
		name string
		raw  []byte
		host string
		port int
	}{
		{"ipv4", []byte{5, 1, 0, 1, 127, 0, 0, 1, 0x1f, 0x90}, "127.0.0.1", 8080},
		{"domain", append(append([]byte{5, 1, 0, 3, 11}, []byte("example.com")...), 0, 80), "example.com", 80},
		{"ipv6", []byte{5, 1, 0, 4, 0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x00, 0x53, 0x00, 0x50}, "2001:db8::53", 80},
	}
	for _, test := range requests {
		t.Run(test.name, func(t *testing.T) {
			request, err := proxy.ReadSOCKS5Request(bytes.NewReader(test.raw))
			if err != nil || request.Command != proxy.SOCKS5Connect || request.Host != test.host || request.Port != test.port {
				t.Fatalf("request=%+v err=%v", request, err)
			}
		})
	}
}

func TestReadSOCKS5RequestKeepsUnsupportedCommand(t *testing.T) {
	request, err := proxy.ReadSOCKS5Request(bytes.NewReader([]byte{5, 3, 0, 1, 127, 0, 0, 1, 0, 80}))
	if err != nil || request.Command == proxy.SOCKS5Connect || request.Host != "127.0.0.1" || request.Port != 80 {
		t.Fatalf("request=%+v err=%v", request, err)
	}
}

func TestReadSOCKS5RequestRejectsMalformedInput(t *testing.T) {
	if _, err := proxy.ReadSOCKS5Request(bytes.NewReader([]byte{4, 1, 0, 1, 127, 0, 0, 1, 0, 80})); err == nil {
		t.Fatal("invalid SOCKS version was accepted")
	}
	if _, err := proxy.ReadSOCKS5Request(bytes.NewReader([]byte{5, 1, 1, 1, 127, 0, 0, 1, 0, 80})); err == nil {
		t.Fatal("invalid reserved byte was accepted")
	}
	if _, err := proxy.ReadSOCKS5Request(bytes.NewReader([]byte{5, 1, 0, 3, 0, 0, 80})); err == nil {
		t.Fatal("empty domain was accepted")
	}
}

func TestEncodeSOCKS5Reply(t *testing.T) {
	want := []byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}
	if got := proxy.EncodeSOCKS5Reply(proxy.SOCKS5ReplySucceeded); !bytes.Equal(got, want) {
		t.Fatalf("reply=%x want=%x", got, want)
	}
}

func TestSOCKS5ReplyForResultMapsStableCodes(t *testing.T) {
	tests := []struct {
		code protocol.OpenResultCode
		want proxy.SOCKS5Reply
	}{
		{protocol.OpenResultCodeOK, proxy.SOCKS5ReplySucceeded},
		{protocol.OpenResultCodeForbidden, proxy.SOCKS5ReplyConnectionNotAllowed},
		{protocol.OpenResultCodeAgentOffline, proxy.SOCKS5ReplyGeneralFailure},
		{protocol.OpenResultCodeQueueFull, proxy.SOCKS5ReplyGeneralFailure},
		{protocol.OpenResultCodeTimeout, proxy.SOCKS5ReplyGeneralFailure},
		{protocol.OpenResultCodeNetworkUnreachable, proxy.SOCKS5ReplyNetworkUnreachable},
		{protocol.OpenResultCodeHostUnreachable, proxy.SOCKS5ReplyHostUnreachable},
		{protocol.OpenResultCodeConnectionRefused, proxy.SOCKS5ReplyConnectionRefused},
		{protocol.OpenResultCodeUnsupportedCapability, proxy.SOCKS5ReplyCommandUnsupported},
		{protocol.OpenResultCodeInternalError, proxy.SOCKS5ReplyGeneralFailure},
	}
	for _, test := range tests {
		t.Run(string(test.code), func(t *testing.T) {
			result := protocol.OpenResultPayload{Accepted: test.code == protocol.OpenResultCodeOK, Code: test.code}
			if got := proxy.SOCKS5ReplyForResult(result); got != byte(test.want) {
				t.Fatalf("reply=%#x, want %#x", byte(got), byte(test.want))
			}
		})
	}
}

func TestParseProxyV2LocalTCP4(t *testing.T) {
	input := []byte{0x0d, 0x0a, 0x0d, 0x0a, 0x00, 0x0d, 0x0a, 0x51, 0x55, 0x49, 0x54, 0x0a, 0x21, 0x11, 0x00, 0x0c, 127, 0, 0, 1, 10, 0, 0, 2, 0x1f, 0x90, 0x00, 0x16}
	h, err := proxy.ParseProxyV2(input)
	if err != nil || h.Source.String() != "127.0.0.1:8080" || h.Destination.String() != "10.0.0.2:22" {
		t.Fatalf("header=%+v err=%v", h, err)
	}
}
