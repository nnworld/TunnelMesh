package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// The preface is the first thing a raw TCP or QUIC agent sends, before any frame. It
// is read by a dedicated, strictly bounded parser: nothing reaches Decoder until the
// token has been accepted, so unauthenticated bytes never meet the frame limits.
//
// Layout: magic "TMAG" | version u8 | flags u8 (must be 0) | tokenLen u16 | token.
const (
	prefaceMagic = "TMAG"
	// PrefaceVersion is the preface layout version. It is independent of the frame
	// version so the authentication step can evolve without touching frames.
	PrefaceVersion   = 1
	MaxPrefaceToken  = 2048
	prefaceHeaderLen = 8
)

// ErrBadPreface reports a malformed, oversized or unsupported preface. Its message is
// fixed and never includes the bytes that were received.
var ErrBadPreface = errors.New("protocol: malformed agent preface")

// PrefaceStatus is the one-byte answer the server gives after the preface.
type PrefaceStatus uint8

const (
	StatusOK                 PrefaceStatus = 0
	StatusUnauthorized       PrefaceStatus = 1
	StatusBadPreface         PrefaceStatus = 2
	StatusUnsupportedVersion PrefaceStatus = 3
	StatusBusy               PrefaceStatus = 4
)

var prefaceStatusNames = map[PrefaceStatus]string{
	StatusOK:                 "ok",
	StatusUnauthorized:       "unauthorized",
	StatusBadPreface:         "bad_preface",
	StatusUnsupportedVersion: "unsupported_version",
	StatusBusy:               "busy",
}

func (s PrefaceStatus) String() string {
	if name, ok := prefaceStatusNames[s]; ok {
		return name
	}
	return fmt.Sprintf("status(%d)", uint8(s))
}

// PrefaceToken wraps the bearer value so it cannot be printed by accident. Every
// formatting path renders a fixed marker.
type PrefaceToken []byte

func (PrefaceToken) String() string   { return "[redacted]" }
func (PrefaceToken) GoString() string { return "[redacted]" }
func (PrefaceToken) LogValue() string { return "[redacted]" }

// WritePreface encodes the preface for token. An empty or oversized token is refused
// here so the agent fails before opening anything on the wire.
func WritePreface(w io.Writer, token []byte) error {
	if len(token) == 0 || len(token) > MaxPrefaceToken {
		return ErrBadPreface
	}
	buf := make([]byte, prefaceHeaderLen+len(token))
	copy(buf, prefaceMagic)
	buf[4] = PrefaceVersion
	buf[5] = 0
	binary.BigEndian.PutUint16(buf[6:8], uint16(len(token)))
	copy(buf[prefaceHeaderLen:], token)
	_, err := w.Write(buf)
	return err
}

// ReadPreface reads and validates one preface. It refuses a declared length beyond the
// limit before reading the body, so a hostile peer cannot make the server buffer more
// than MaxPrefaceToken bytes.
func ReadPreface(r io.Reader) ([]byte, error) {
	var header [prefaceHeaderLen]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, ErrBadPreface
	}
	if string(header[:4]) != prefaceMagic || header[4] != PrefaceVersion || header[5] != 0 {
		return nil, ErrBadPreface
	}
	length := int(binary.BigEndian.Uint16(header[6:8]))
	if length == 0 || length > MaxPrefaceToken {
		return nil, ErrBadPreface
	}
	token := make([]byte, length)
	if _, err := io.ReadFull(r, token); err != nil {
		return nil, ErrBadPreface
	}
	return token, nil
}
