package protocol

import (
	"encoding/json"
	"errors"
	"sort"
)

var ErrNoCommonVersion = errors.New("protocol: no common version")

type CapabilityHello struct {
	Versions []uint8  `json:"versions"`
	Features []string `json:"features"`
}

type NegotiatedCapabilities struct {
	Version  uint8    `json:"version"`
	Features []string `json:"features"`
}

func EncodeCapabilityHello(hello CapabilityHello) ([]byte, error) { return json.Marshal(hello) }
func DecodeCapabilityHello(payload []byte) (CapabilityHello, error) {
	if len(payload) > MaxMetadataPayload {
		return CapabilityHello{}, ErrPayloadTooLarge
	}
	var hello CapabilityHello
	if err := json.Unmarshal(payload, &hello); err != nil {
		return CapabilityHello{}, err
	}
	return hello, nil
}

func NegotiateCapabilities(client, server CapabilityHello) (NegotiatedCapabilities, error) {
	versions := make(map[uint8]struct{}, len(server.Versions))
	for _, version := range server.Versions {
		versions[version] = struct{}{}
	}
	var selected uint8
	for _, version := range client.Versions {
		if _, ok := versions[version]; ok && version > selected {
			selected = version
		}
	}
	if selected == 0 {
		return NegotiatedCapabilities{}, ErrNoCommonVersion
	}
	serverFeatures := make(map[string]struct{}, len(server.Features))
	for _, feature := range server.Features {
		serverFeatures[feature] = struct{}{}
	}
	seen := map[string]struct{}{}
	features := make([]string, 0)
	for _, feature := range client.Features {
		if _, ok := serverFeatures[feature]; ok {
			if _, duplicate := seen[feature]; !duplicate {
				seen[feature] = struct{}{}
				features = append(features, feature)
			}
		}
	}
	sort.Strings(features)
	return NegotiatedCapabilities{Version: selected, Features: features}, nil
}

type ErrorCode string

const (
	ErrorCodeUnauthorized      ErrorCode = "UNAUTHORIZED"
	ErrorCodeForbidden         ErrorCode = "FORBIDDEN"
	ErrorCodeUnsupported       ErrorCode = "UNSUPPORTED"
	ErrorCodeTimeout           ErrorCode = "TIMEOUT"
	ErrorCodeResourceExhausted ErrorCode = "RESOURCE_EXHAUSTED"
	ErrorCodeProtocol          ErrorCode = "PROTOCOL_ERROR"
)

type ProtocolError struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

func (e ProtocolError) Error() string { return string(e.Code) + ": " + e.Message }
