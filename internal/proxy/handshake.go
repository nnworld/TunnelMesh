package proxy

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

var (
	ErrMalformedHandshake = errors.New("proxy: malformed handshake")
	ErrUnsupportedCommand = errors.New("proxy: unsupported command")
)

type ConnectRequest struct {
	Host string
	Port int
}

func ParseHTTPConnect(payload []byte) (ConnectRequest, error) {
	text := string(payload)
	end := strings.Index(text, "\r\n\r\n")
	if end < 0 || end > 16<<10 {
		return ConnectRequest{}, ErrMalformedHandshake
	}
	lines := strings.Split(text[:end], "\r\n")
	if len(lines) == 0 {
		return ConnectRequest{}, ErrMalformedHandshake
	}
	parts := strings.Fields(lines[0])
	if len(parts) != 3 || strings.ToUpper(parts[0]) != "CONNECT" || !strings.HasPrefix(parts[2], "HTTP/") {
		return ConnectRequest{}, ErrMalformedHandshake
	}
	host, portText, err := net.SplitHostPort(parts[1])
	if err != nil || host == "" {
		return ConnectRequest{}, ErrMalformedHandshake
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return ConnectRequest{}, ErrMalformedHandshake
	}
	return ConnectRequest{Host: host, Port: port}, nil
}

type SOCKS5Command byte

const SOCKS5Connect SOCKS5Command = 1

type SOCKS5Request struct {
	Command SOCKS5Command
	Host    string
	Port    int
}

func ParseSOCKS5Connect(payload []byte) (SOCKS5Request, error) {
	if len(payload) < 7 || payload[0] != 5 {
		return SOCKS5Request{}, ErrMalformedHandshake
	}
	nmethods := int(payload[1])
	if nmethods < 1 || len(payload) < 2+nmethods+4 {
		return SOCKS5Request{}, ErrMalformedHandshake
	}
	pos := 2 + nmethods
	if payload[pos] != 5 || payload[pos+2] != 0 || payload[pos+1] != byte(SOCKS5Connect) {
		return SOCKS5Request{}, ErrUnsupportedCommand
	}
	pos += 3
	var host string
	switch payload[pos] {
	case 1:
		if len(payload) < pos+7 {
			return SOCKS5Request{}, ErrMalformedHandshake
		}
		host = net.IP(payload[pos+1 : pos+5]).String()
		pos += 5
	case 3:
		length := int(payload[pos+1])
		if length == 0 || len(payload) < pos+2+length+2 {
			return SOCKS5Request{}, ErrMalformedHandshake
		}
		host = string(payload[pos+2 : pos+2+length])
		pos += 2 + length
	case 4:
		if len(payload) < pos+19 {
			return SOCKS5Request{}, ErrMalformedHandshake
		}
		host = net.IP(payload[pos+1 : pos+17]).String()
		pos += 17
	default:
		return SOCKS5Request{}, ErrMalformedHandshake
	}
	port := int(binary.BigEndian.Uint16(payload[pos : pos+2]))
	if port < 1 {
		return SOCKS5Request{}, ErrMalformedHandshake
	}
	return SOCKS5Request{Command: SOCKS5Connect, Host: host, Port: port}, nil
}

type ProxyV2Header struct {
	Source      net.TCPAddr
	Destination net.TCPAddr
}

var proxyV2Signature = []byte{0x0d, 0x0a, 0x0d, 0x0a, 0x00, 0x0d, 0x0a, 0x51, 0x55, 0x49, 0x54, 0x0a}

func ParseProxyV2(payload []byte) (ProxyV2Header, error) {
	if len(payload) < 16 || string(payload[:12]) != string(proxyV2Signature) {
		return ProxyV2Header{}, ErrMalformedHandshake
	}
	if payload[12] != 0x21 || payload[13] != 0x11 {
		return ProxyV2Header{}, fmt.Errorf("%w: only TCP4 is supported", ErrMalformedHandshake)
	}
	length := int(binary.BigEndian.Uint16(payload[14:16]))
	if length != 12 || len(payload) < 28 {
		return ProxyV2Header{}, ErrMalformedHandshake
	}
	src := net.IP(payload[16:20]).To4()
	dst := net.IP(payload[20:24]).To4()
	if src == nil || dst == nil {
		return ProxyV2Header{}, ErrMalformedHandshake
	}
	return ProxyV2Header{Source: net.TCPAddr{IP: src, Port: int(binary.BigEndian.Uint16(payload[24:26]))}, Destination: net.TCPAddr{IP: dst, Port: int(binary.BigEndian.Uint16(payload[26:28]))}}, nil
}
