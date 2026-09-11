package protocol

import "encoding/json"

type OpenResultStage string

const (
	OpenResultStageAuthorization OpenResultStage = "authorization"
	OpenResultStageSelection     OpenResultStage = "selection"
	OpenResultStageRelay         OpenResultStage = "relay"
	OpenResultStageQueue         OpenResultStage = "queue"
	OpenResultStageDNS           OpenResultStage = "dns"
	OpenResultStageConnect       OpenResultStage = "connect"
	OpenResultStagePolicy        OpenResultStage = "policy"
	OpenResultStageProtocol      OpenResultStage = "protocol"
)

type OpenResultCode string

const (
	OpenResultCodeOK                    OpenResultCode = "ok"
	OpenResultCodeForbidden             OpenResultCode = "forbidden"
	OpenResultCodeAgentOffline          OpenResultCode = "agent_offline"
	OpenResultCodeQueueFull             OpenResultCode = "queue_full"
	OpenResultCodeTimeout               OpenResultCode = "timeout"
	OpenResultCodeNetworkUnreachable    OpenResultCode = "network_unreachable"
	OpenResultCodeHostUnreachable       OpenResultCode = "host_unreachable"
	OpenResultCodeConnectionRefused     OpenResultCode = "connection_refused"
	OpenResultCodeUnsupportedCapability OpenResultCode = "unsupported_capability"
	OpenResultCodeInternalError         OpenResultCode = "internal_error"
)

type OpenResultPayload struct {
	Accepted     bool            `json:"accepted"`
	Stage        OpenResultStage `json:"stage"`
	Code         OpenResultCode  `json:"code"`
	Retryable    bool            `json:"retryable,omitempty"`
	RetryAfterMS int             `json:"retry_after_ms,omitempty"`
}

func EncodeOpenResultPayload(payload OpenResultPayload) ([]byte, error) {
	if err := payload.validate(); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if len(encoded) > MaxOpenResultPayload {
		return nil, ErrPayloadTooLarge
	}
	return encoded, nil
}

func DecodeOpenResultPayload(encoded []byte) (OpenResultPayload, error) {
	if len(encoded) > MaxOpenResultPayload {
		return OpenResultPayload{}, ErrPayloadTooLarge
	}
	var payload OpenResultPayload
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return OpenResultPayload{}, err
	}
	if err := payload.validate(); err != nil {
		return OpenResultPayload{}, err
	}
	return payload, nil
}

func (p OpenResultPayload) validate() error {
	switch p.Stage {
	case OpenResultStageAuthorization, OpenResultStageSelection, OpenResultStageRelay,
		OpenResultStageQueue, OpenResultStageDNS, OpenResultStageConnect,
		OpenResultStagePolicy, OpenResultStageProtocol:
	default:
		return ErrInvalidFrame
	}
	switch p.Code {
	case OpenResultCodeOK, OpenResultCodeForbidden, OpenResultCodeAgentOffline,
		OpenResultCodeQueueFull, OpenResultCodeTimeout, OpenResultCodeNetworkUnreachable,
		OpenResultCodeHostUnreachable, OpenResultCodeConnectionRefused,
		OpenResultCodeUnsupportedCapability, OpenResultCodeInternalError:
	default:
		return ErrInvalidFrame
	}
	if p.Accepted != (p.Code == OpenResultCodeOK) {
		return ErrInvalidFrame
	}
	if p.RetryAfterMS < 0 {
		return ErrInvalidFrame
	}
	return nil
}
