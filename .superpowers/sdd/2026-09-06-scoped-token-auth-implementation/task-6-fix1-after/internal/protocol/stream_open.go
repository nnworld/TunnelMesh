package protocol

import "encoding/json"

// StreamOpenPayload is the single shared JSON model for OPEN_STREAM frames.
// Authentication identity is deliberately absent: the server derives it from
// the pre-upgrade bearer credential instead of trusting frame data.
type StreamOpenPayload struct {
	AgentID    string `json:"agent_id,omitempty"`
	Protocol   string `json:"protocol"`
	TargetHost string `json:"target_host"`
	TargetPort int    `json:"target_port"`
	Metadata   []byte `json:"metadata,omitempty"`
}

func EncodeStreamOpenPayload(payload StreamOpenPayload) ([]byte, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if len(encoded) > MaxPayload {
		return nil, ErrPayloadTooLarge
	}
	return encoded, nil
}

func DecodeStreamOpenPayload(encoded []byte) (StreamOpenPayload, error) {
	if len(encoded) > MaxPayload {
		return StreamOpenPayload{}, ErrPayloadTooLarge
	}
	var payload StreamOpenPayload
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return StreamOpenPayload{}, err
	}
	return payload, nil
}
