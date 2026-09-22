package protocol

import (
	"encoding/json"
	"errors"
	"strings"
)

// StreamProtocolICMPEcho is the OPEN_STREAM protocol value for one in-flight
// ICMP echo. One stream carries exactly one request datagram and one reply
// datagram, which is what keeps a lost or late echo from being attributed to
// the next one.
const StreamProtocolICMPEcho = "icmp-echo"

// ErrInvalidICMPEcho reports a message that cannot be acted on: no correlation
// identifier, or a status outside the closed enumeration. Refusing it here is
// what stops an unknown status from reaching the policy layer, where it would
// have to be mapped onto a metric label that does not exist.
var ErrInvalidICMPEcho = errors.New("protocol: invalid icmp echo message")

// The reply statuses are a closed set. Phase 6 maps each one onto a data-plane
// error_class label, so adding a status without adding the mapping produces a
// failure that no metric can name.
const (
	ICMPEchoStatusOK                = "ok"
	ICMPEchoStatusTimeout           = "timeout"
	ICMPEchoStatusCapacityExhausted = "capacity_exhausted"
	ICMPEchoStatusUnreachable       = "unreachable"
	ICMPEchoStatusUnsupported       = "unsupported"
	ICMPEchoStatusCancelled         = "cancelled"
)

// ICMPEchoStatuses returns the enumeration in declaration order. It exists so a
// test can pin the set and so the data plane can assert that every status it
// receives is one it can report.
func ICMPEchoStatuses() []string {
	return []string{
		ICMPEchoStatusOK, ICMPEchoStatusTimeout, ICMPEchoStatusCapacityExhausted,
		ICMPEchoStatusUnreachable, ICMPEchoStatusUnsupported, ICMPEchoStatusCancelled,
	}
}

// ICMPEchoRequest is what the server writes on an "icmp-echo" stream.
//
// Identifier and Sequence are the values the VPN peer put in its own echo, not
// the values that reach the internal host: an unprivileged ping socket has its
// identifier rewritten by the kernel, so the agent allocates its own on the wire
// and the server restores these when it forges the reply. CorrelationID is the
// server's own handle for the in-flight echo and the only field the agent echoes
// back verbatim.
type ICMPEchoRequest struct {
	CorrelationID string `json:"correlation_id"`
	Identifier    uint16 `json:"identifier"`
	Sequence      uint16 `json:"sequence"`
	Data          []byte `json:"data,omitempty"`
}

// ICMPEchoReply is the single datagram the agent writes back. A failed echo is
// still a reply: IP has no error channel, so the status is the only way the
// server learns whether to count a timeout, an exhausted agent-side budget or a
// refusal, and the identifiers travel with it so the peer's own ping can be
// answered with a matching header.
type ICMPEchoReply struct {
	CorrelationID string `json:"correlation_id"`
	Identifier    uint16 `json:"identifier"`
	Sequence      uint16 `json:"sequence"`
	Data          []byte `json:"data,omitempty"`
	Status        string `json:"status"`
	RTTMillis     int64  `json:"rtt_millis,omitempty"`
}

func EncodeICMPEchoRequest(request ICMPEchoRequest) ([]byte, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	if len(encoded) > MaxDatagram {
		return nil, ErrDatagramTooLarge
	}
	return encoded, nil
}

func DecodeICMPEchoRequest(encoded []byte) (ICMPEchoRequest, error) {
	if len(encoded) > MaxDatagram {
		return ICMPEchoRequest{}, ErrDatagramTooLarge
	}
	var request ICMPEchoRequest
	if err := json.Unmarshal(encoded, &request); err != nil {
		return ICMPEchoRequest{}, err
	}
	// Unknown fields are ignored on purpose: a newer server may add one and an
	// older agent still has to serve the echo.
	if strings.TrimSpace(request.CorrelationID) == "" {
		return ICMPEchoRequest{}, ErrInvalidICMPEcho
	}
	return request, nil
}

func EncodeICMPEchoReply(reply ICMPEchoReply) ([]byte, error) {
	encoded, err := json.Marshal(reply)
	if err != nil {
		return nil, err
	}
	if len(encoded) > MaxDatagram {
		return nil, ErrDatagramTooLarge
	}
	return encoded, nil
}

func DecodeICMPEchoReply(encoded []byte) (ICMPEchoReply, error) {
	if len(encoded) > MaxDatagram {
		return ICMPEchoReply{}, ErrDatagramTooLarge
	}
	var reply ICMPEchoReply
	if err := json.Unmarshal(encoded, &reply); err != nil {
		return ICMPEchoReply{}, err
	}
	if strings.TrimSpace(reply.CorrelationID) == "" {
		return ICMPEchoReply{}, ErrInvalidICMPEcho
	}
	if !isICMPEchoStatus(reply.Status) {
		return ICMPEchoReply{}, ErrInvalidICMPEcho
	}
	return reply, nil
}

func isICMPEchoStatus(status string) bool {
	for _, candidate := range ICMPEchoStatuses() {
		if candidate == status {
			return true
		}
	}
	return false
}
