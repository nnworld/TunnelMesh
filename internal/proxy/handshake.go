package proxy

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
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

const (
	SOCKS5Connect SOCKS5Command = 1
)

const (
	SOCKS5MethodNoAuth           byte = 0x00
	SOCKS5MethodUsernamePassword byte = 0x02
)

type SOCKS5Reply byte

const (
	SOCKS5ReplySucceeded            SOCKS5Reply = 0x00
	SOCKS5ReplyGeneralFailure       SOCKS5Reply = 0x01
	SOCKS5ReplyConnectionNotAllowed SOCKS5Reply = 0x02
	SOCKS5ReplyNetworkUnreachable   SOCKS5Reply = 0x03
	SOCKS5ReplyHostUnreachable      SOCKS5Reply = 0x04
	SOCKS5ReplyConnectionRefused    SOCKS5Reply = 0x05
	SOCKS5ReplyCommandUnsupported   SOCKS5Reply = 0x07
)

type SOCKS5Credentials struct {
	Username string
	Password string
}

type SOCKS5Request struct {
	Command SOCKS5Command
	Host    string
	Port    int
}

func ReadSOCKS5Methods(r io.Reader) ([]byte, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, ErrMalformedHandshake
	}
	if header[0] != 5 || header[1] == 0 {
		return nil, ErrMalformedHandshake
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(r, methods); err != nil {
		return nil, ErrMalformedHandshake
	}
	return methods, nil
}

func SupportsSOCKS5NoAuth(methods []byte) bool {
	return supportsSOCKS5Method(methods, SOCKS5MethodNoAuth)
}

func SupportsSOCKS5UsernamePassword(methods []byte) bool {
	return supportsSOCKS5Method(methods, SOCKS5MethodUsernamePassword)
}

func supportsSOCKS5Method(methods []byte, method byte) bool {
	for _, offered := range methods {
		if offered == method {
			return true
		}
	}
	return false
}

func EncodeSOCKS5MethodSelection(method byte) []byte {
	return []byte{5, method}
}

func ReadSOCKS5UsernamePassword(r io.Reader) (SOCKS5Credentials, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(r, header); err != nil || header[0] != 1 {
		return SOCKS5Credentials{}, ErrMalformedHandshake
	}
	username := make([]byte, int(header[1]))
	if _, err := io.ReadFull(r, username); err != nil {
		return SOCKS5Credentials{}, ErrMalformedHandshake
	}
	length := make([]byte, 1)
	if _, err := io.ReadFull(r, length); err != nil {
		return SOCKS5Credentials{}, ErrMalformedHandshake
	}
	password := make([]byte, int(length[0]))
	if _, err := io.ReadFull(r, password); err != nil {
		return SOCKS5Credentials{}, ErrMalformedHandshake
	}
	return SOCKS5Credentials{Username: string(username), Password: string(password)}, nil
}

func EncodeSOCKS5UsernamePasswordReply(success bool) []byte {
	status := byte(1)
	if success {
		status = 0
	}
	return []byte{1, status}
}

func ReadSOCKS5Request(r io.Reader) (SOCKS5Request, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(r, header); err != nil {
		return SOCKS5Request{}, ErrMalformedHandshake
	}
	if header[0] != 5 || header[2] != 0 {
		return SOCKS5Request{}, ErrMalformedHandshake
	}
	var host string
	switch header[3] {
	case 1:
		address := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(r, address); err != nil {
			return SOCKS5Request{}, ErrMalformedHandshake
		}
		host = net.IP(address).String()
	case 3:
		length := make([]byte, 1)
		if _, err := io.ReadFull(r, length); err != nil || length[0] == 0 {
			return SOCKS5Request{}, ErrMalformedHandshake
		}
		address := make([]byte, int(length[0]))
		if _, err := io.ReadFull(r, address); err != nil {
			return SOCKS5Request{}, ErrMalformedHandshake
		}
		host = string(address)
	case 4:
		address := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(r, address); err != nil {
			return SOCKS5Request{}, ErrMalformedHandshake
		}
		host = net.IP(address).String()
	default:
		return SOCKS5Request{}, ErrMalformedHandshake
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(r, portBytes); err != nil {
		return SOCKS5Request{}, ErrMalformedHandshake
	}
	port := int(binary.BigEndian.Uint16(portBytes))
	if port < 1 {
		return SOCKS5Request{}, ErrMalformedHandshake
	}
	return SOCKS5Request{Command: SOCKS5Command(header[1]), Host: host, Port: port}, nil
}

func EncodeSOCKS5Reply(reply SOCKS5Reply) []byte {
	return []byte{5, byte(reply), 0, 1, 0, 0, 0, 0, 0, 0}
}

// SOCKS5ReplyForResult maps the protocol's stable open result to RFC 1928
// reply codes. Unknown values fail closed as a general failure.
func SOCKS5ReplyForResult(result protocol.OpenResultPayload) byte {
	switch result.Code {
	case protocol.OpenResultCodeOK:
		return byte(SOCKS5ReplySucceeded)
	case protocol.OpenResultCodeForbidden:
		return byte(SOCKS5ReplyConnectionNotAllowed)
	case protocol.OpenResultCodeNetworkUnreachable:
		return byte(SOCKS5ReplyNetworkUnreachable)
	case protocol.OpenResultCodeHostUnreachable:
		return byte(SOCKS5ReplyHostUnreachable)
	case protocol.OpenResultCodeConnectionRefused:
		return byte(SOCKS5ReplyConnectionRefused)
	case protocol.OpenResultCodeUnsupportedCapability:
		return byte(SOCKS5ReplyCommandUnsupported)
	default:
		return byte(SOCKS5ReplyGeneralFailure)
	}
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
