package proxy_test

import (
	"github.com/tunnelmesh/tunnelmesh/internal/proxy"
	"net"
	"testing"
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

func TestParseProxyV2LocalTCP4(t *testing.T) {
	input := []byte{0x0d, 0x0a, 0x0d, 0x0a, 0x00, 0x0d, 0x0a, 0x51, 0x55, 0x49, 0x54, 0x0a, 0x21, 0x11, 0x00, 0x0c, 127, 0, 0, 1, 10, 0, 0, 2, 0x1f, 0x90, 0x00, 0x16}
	h, err := proxy.ParseProxyV2(input)
	if err != nil || h.Source.String() != "127.0.0.1:8080" || h.Destination.String() != "10.0.0.2:22" {
		t.Fatalf("header=%+v err=%v", h, err)
	}
}
