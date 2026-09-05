package protocol

import (
	"errors"
	"fmt"
)

const (
	CurrentVersion uint8 = 1
	MaxPayload           = 1 << 20
)

var (
	ErrUnsupportedVersion = errors.New("protocol: unsupported version")
	ErrUnknownFrameType   = errors.New("protocol: unknown frame type")
	ErrPayloadTooLarge    = errors.New("protocol: payload too large")
	ErrInvalidFrame       = errors.New("protocol: invalid frame")
	ErrStreamClosed       = errors.New("protocol: stream is closed")
	ErrStreamReset        = errors.New("protocol: stream is reset")
	ErrWindowExhausted    = errors.New("protocol: flow-control window exhausted")
	ErrInvalidTransition  = errors.New("protocol: invalid stream transition")
	ErrDatagramTooLarge   = errors.New("protocol: datagram too large")
)

type FrameType uint8

const (
	FrameOpenStream FrameType = iota + 1
	FrameData
	FrameHalfClose
	FrameReset
	FrameWindowUpdate
	FrameGoAway
	FramePing
	FramePong
)

// Descriptive aliases keep call sites readable while retaining the wire names.
const (
	FrameTypeOpenStream   = FrameOpenStream
	FrameTypeData         = FrameData
	FrameTypeHalfClose    = FrameHalfClose
	FrameTypeReset        = FrameReset
	FrameTypeWindowUpdate = FrameWindowUpdate
	FrameTypeGoAway       = FrameGoAway
	FrameTypePing         = FramePing
	FrameTypePong         = FramePong
	FrameClose            = FrameHalfClose
)

const (
	FlagFin uint16 = 1 << iota
	FlagAck
)

// Frame is the versioned unit exchanged over an agent/client WebSocket.
// Window is meaningful for OPEN_STREAM and WINDOW_UPDATE frames.
type Frame struct {
	Version  uint8
	Type     FrameType
	Flags    uint16
	StreamID uint32
	Window   uint32
	Payload  []byte
}

func (f Frame) Validate() error {
	if f.Version != CurrentVersion {
		return fmt.Errorf("%w: %d", ErrUnsupportedVersion, f.Version)
	}
	if !knownFrameType(f.Type) {
		return fmt.Errorf("%w: %d", ErrUnknownFrameType, f.Type)
	}
	if len(f.Payload) > MaxPayload {
		return fmt.Errorf("%w: %d", ErrPayloadTooLarge, len(f.Payload))
	}
	if f.StreamID == 0 && f.Type != FrameGoAway && f.Type != FramePing && f.Type != FramePong {
		return ErrInvalidFrame
	}
	if f.Type == FrameWindowUpdate && f.Window == 0 {
		return ErrInvalidFrame
	}
	return nil
}

func knownFrameType(t FrameType) bool { return t >= FrameOpenStream && t <= FramePong }
