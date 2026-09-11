package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	CurrentVersion       uint8 = 1
	MaxPayload                 = 1 << 20
	MaxMetadataPayload         = 32 << 10
	MaxOpenResultPayload       = 1 << 10
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
	FrameAgentHello
	FrameAgentMetadataUpdate
	FrameAgentMetadataAck
	FrameTraceStart
	FrameTraceHop
	FrameTraceEnd
	FrameOpenResult
	FrameClientHello
	FrameClientMetadataUpdate
	FrameClientMetadataAck
)

// Descriptive aliases keep call sites readable while retaining the wire names.
const (
	FrameTypeOpenStream     = FrameOpenStream
	FrameTypeData           = FrameData
	FrameTypeHalfClose      = FrameHalfClose
	FrameTypeReset          = FrameReset
	FrameTypeWindowUpdate   = FrameWindowUpdate
	FrameTypeGoAway         = FrameGoAway
	FrameTypePing           = FramePing
	FrameTypePong           = FramePong
	FrameTypeAgentHello     = FrameAgentHello
	FrameTypeMetadataUpdate = FrameAgentMetadataUpdate
	FrameTypeMetadataAck    = FrameAgentMetadataAck
	FrameTypeTraceStart     = FrameTraceStart
	FrameTypeTraceHop       = FrameTraceHop
	FrameTypeTraceEnd       = FrameTraceEnd
	FrameTypeOpenResult     = FrameOpenResult
	FrameClose              = FrameHalfClose
)

const (
	FlagFin uint16 = 1 << iota
	FlagAck
	FlagStrictOpen
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
	if isMetadataFrame(f.Type) && len(f.Payload) > MaxMetadataPayload {
		return fmt.Errorf("%w: %d", ErrPayloadTooLarge, len(f.Payload))
	}
	if f.Type == FrameOpenResult && len(f.Payload) > MaxOpenResultPayload {
		return fmt.Errorf("%w: %d", ErrPayloadTooLarge, len(f.Payload))
	}
	if f.StreamID == 0 && f.Type != FrameGoAway && f.Type != FramePing && f.Type != FramePong && !isMetadataFrame(f.Type) && !isTraceFrame(f.Type) {
		return ErrInvalidFrame
	}
	if f.Type == FrameWindowUpdate && f.Window == 0 {
		return ErrInvalidFrame
	}
	return nil
}

func knownFrameType(t FrameType) bool { return t >= FrameOpenStream && t <= FrameClientMetadataAck }

func isMetadataFrame(t FrameType) bool {
	return (t >= FrameAgentHello && t <= FrameAgentMetadataAck) ||
		(t >= FrameClientHello && t <= FrameClientMetadataAck)
}

func isTraceFrame(t FrameType) bool { return t >= FrameTraceStart && t <= FrameTraceEnd }

// AgentMetadataItem is one allowlisted value sent in a metadata snapshot.
type AgentMetadataItem struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Value  string `json:"value,omitempty"`
}

// AgentMetadataError is a field-scoped rejection or collection failure. The
// message must never contain the source value or secret-bearing configuration.
type AgentMetadataError struct {
	Name    string `json:"name"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// AgentMetadataPayload is used by both AGENT_HELLO and AGENT_METADATA_UPDATE.
type AgentMetadataPayload struct {
	AgentID      string               `json:"agent_id"`
	InstanceID   string               `json:"instance_id,omitempty"`
	ConnectionID string               `json:"connection_id,omitempty"`
	NodeID       string               `json:"node_id,omitempty"`
	Epoch        int64                `json:"epoch"`
	Revision     uint64               `json:"revision"`
	ReportedAt   time.Time            `json:"reported_at"`
	Items        []AgentMetadataItem  `json:"items"`
	Errors       []AgentMetadataError `json:"errors,omitempty"`
	Capabilities []string             `json:"capabilities,omitempty"`
}

// AgentMetadataAckPayload is returned by the server after fencing and
// validating a metadata snapshot.
type AgentMetadataAckPayload struct {
	AgentID                 string               `json:"agent_id"`
	Epoch                   int64                `json:"epoch"`
	Revision                uint64               `json:"revision"`
	Accepted                bool                 `json:"accepted"`
	Idempotent              bool                 `json:"idempotent,omitempty"`
	ConnectionPoolSupported bool                 `json:"connection_pool_supported,omitempty"`
	MaxConnectionsPerAgent  int                  `json:"max_connections_per_agent,omitempty"`
	Errors                  []AgentMetadataError `json:"errors,omitempty"`
	Capabilities            []string             `json:"capabilities,omitempty"`
}

func EncodeAgentMetadataPayload(payload AgentMetadataPayload) ([]byte, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if len(encoded) > MaxMetadataPayload {
		return nil, ErrPayloadTooLarge
	}
	return encoded, nil
}

func DecodeAgentMetadataPayload(encoded []byte) (AgentMetadataPayload, error) {
	if len(encoded) > MaxMetadataPayload {
		return AgentMetadataPayload{}, ErrPayloadTooLarge
	}
	var payload AgentMetadataPayload
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return AgentMetadataPayload{}, err
	}
	return payload, nil
}

func EncodeAgentMetadataAckPayload(payload AgentMetadataAckPayload) ([]byte, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if len(encoded) > MaxMetadataPayload {
		return nil, ErrPayloadTooLarge
	}
	return encoded, nil
}

func DecodeAgentMetadataAckPayload(encoded []byte) (AgentMetadataAckPayload, error) {
	if len(encoded) > MaxMetadataPayload {
		return AgentMetadataAckPayload{}, ErrPayloadTooLarge
	}
	var payload AgentMetadataAckPayload
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return AgentMetadataAckPayload{}, err
	}
	return payload, nil
}

// ClientMetadataItem is one allowlisted value sent by a client process.
type ClientMetadataItem struct {
	Name     string `json:"name"`
	Source   string `json:"source"`
	Value    string `json:"value,omitempty"`
	Redacted bool   `json:"redacted,omitempty"`
}

// ClientMetadataError reports a bounded field-scoped collection failure.
type ClientMetadataError struct {
	Name    string `json:"name"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ClientListener describes a local ingress without exposing credentials.
type ClientListener struct {
	Protocol      string `json:"protocol"`
	ListenAddress string `json:"listenAddress"`
	AgentID       string `json:"agentId"`
	Enabled       bool   `json:"enabled"`
}

// ClientMetadataPayload is used by CLIENT_HELLO and updates.
type ClientMetadataPayload struct {
	InstanceID     string                `json:"instance_id"`
	ConnectionSlot int                   `json:"connection_slot,omitempty"`
	AgentIDs       []string              `json:"agent_ids,omitempty"`
	Version        string                `json:"version,omitempty"`
	Commit         string                `json:"commit,omitempty"`
	Platform       string                `json:"platform,omitempty"`
	Hostname       string                `json:"hostname,omitempty"`
	ProcessStartAt time.Time             `json:"process_start_at,omitempty"`
	ReportedAt     time.Time             `json:"reported_at"`
	Revision       uint64                `json:"revision"`
	Listeners      []ClientListener      `json:"listeners,omitempty"`
	Items          []ClientMetadataItem  `json:"items,omitempty"`
	Capabilities   []string              `json:"capabilities,omitempty"`
	Errors         []ClientMetadataError `json:"errors,omitempty"`
}

// ClientMetadataAckPayload confirms the server-managed client resource ID.
type ClientMetadataAckPayload struct {
	ClientInstanceID string                `json:"client_instance_id"`
	InstanceID       string                `json:"instance_id"`
	ConnectionID     string                `json:"connection_id"`
	Revision         uint64                `json:"revision"`
	Accepted         bool                  `json:"accepted"`
	Idempotent       bool                  `json:"idempotent,omitempty"`
	Errors           []ClientMetadataError `json:"errors,omitempty"`
}

func EncodeClientMetadataPayload(payload ClientMetadataPayload) ([]byte, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if len(encoded) > MaxMetadataPayload {
		return nil, ErrPayloadTooLarge
	}
	return encoded, nil
}

func DecodeClientMetadataPayload(encoded []byte) (ClientMetadataPayload, error) {
	if len(encoded) > MaxMetadataPayload {
		return ClientMetadataPayload{}, ErrPayloadTooLarge
	}
	var payload ClientMetadataPayload
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return ClientMetadataPayload{}, err
	}
	return payload, nil
}

func EncodeClientMetadataAckPayload(payload ClientMetadataAckPayload) ([]byte, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if len(encoded) > MaxMetadataPayload {
		return nil, ErrPayloadTooLarge
	}
	return encoded, nil
}

func DecodeClientMetadataAckPayload(encoded []byte) (ClientMetadataAckPayload, error) {
	if len(encoded) > MaxMetadataPayload {
		return ClientMetadataAckPayload{}, ErrPayloadTooLarge
	}
	var payload ClientMetadataAckPayload
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return ClientMetadataAckPayload{}, err
	}
	return payload, nil
}
