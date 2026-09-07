package protocol

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type TraceRequest struct {
	TraceID          string `json:"traceId"`
	AgentID          string `json:"agentId"`
	TimeoutMs        int    `json:"timeoutMs"`
	MaxHops          int    `json:"maxHops"`
	Verbose          bool   `json:"verbose"`
	IncludeSensitive bool   `json:"includeSensitive"`
	IncludeSecrets   bool   `json:"includeSecrets"`
}

type CertificateInfo struct {
	Subject           string    `json:"subject"`
	Issuer            string    `json:"issuer"`
	Serial            string    `json:"serial"`
	FingerprintSHA256 string    `json:"fingerprintSHA256"`
	NotBefore         time.Time `json:"notBefore"`
	NotAfter          time.Time `json:"notAfter"`
}
type TokenInfo struct {
	ID     string `json:"tokenId"`
	Type   string `json:"tokenType"`
	Prefix string `json:"prefix,omitempty"`
	Scope  string `json:"scope,omitempty"`
	Secret string `json:"secret,omitempty"`
}

type TraceHop struct {
	TraceID           string           `json:"traceId"`
	Hop               int              `json:"hop"`
	Role              string           `json:"role"`
	NodeID            string           `json:"nodeId,omitempty"`
	PeerType          string           `json:"peerType,omitempty"`
	Transport         string           `json:"transport,omitempty"`
	ReceivedAt        time.Time        `json:"receivedAt,omitempty"`
	ForwardedAt       time.Time        `json:"forwardedAt,omitempty"`
	QueueDelayMs      int64            `json:"queueDelayMs,omitempty"`
	HopRTTMs          int64            `json:"hopRttMs,omitempty"`
	Result            string           `json:"result"`
	PrivateIPs        []string         `json:"privateIPs,omitempty"`
	RealAddress       string           `json:"realAddress,omitempty"`
	Certificate       *CertificateInfo `json:"certificate,omitempty"`
	Token             *TokenInfo       `json:"token,omitempty"`
	PreviousSignature string           `json:"previousSignature,omitempty"`
	Signature         string           `json:"signature,omitempty"`
}

type TraceResult struct {
	TraceID   string     `json:"traceId"`
	Hops      []TraceHop `json:"hops"`
	Completed bool       `json:"completed"`
	Error     string     `json:"error,omitempty"`
}

func EncodeTraceRequest(request TraceRequest) ([]byte, error) { return encodeTracePayload(request) }
func DecodeTraceRequest(payload []byte) (TraceRequest, error) {
	var request TraceRequest
	err := decodeTracePayload(payload, &request)
	return request, err
}
func EncodeTraceHop(hop TraceHop) ([]byte, error) { return encodeTracePayload(hop) }
func DecodeTraceHop(payload []byte) (TraceHop, error) {
	var hop TraceHop
	err := decodeTracePayload(payload, &hop)
	return hop, err
}
func EncodeTraceResult(result TraceResult) ([]byte, error) { return encodeTracePayload(result) }
func DecodeTraceResult(payload []byte) (TraceResult, error) {
	var result TraceResult
	err := decodeTracePayload(payload, &result)
	return result, err
}
func encodeTracePayload(value any) ([]byte, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(payload) > MaxMetadataPayload {
		return nil, ErrPayloadTooLarge
	}
	return payload, nil
}
func decodeTracePayload(payload []byte, target any) error {
	if len(payload) > MaxMetadataPayload {
		return ErrPayloadTooLarge
	}
	return json.Unmarshal(payload, target)
}

type TraceHopChain struct {
	key      []byte
	previous string
	next     int
}

func NewTraceHopChain(key []byte) *TraceHopChain {
	return &TraceHopChain{key: append([]byte(nil), key...), next: 1}
}
func (c *TraceHopChain) Append(hop *TraceHop) error {
	if c == nil || len(c.key) == 0 || hop == nil {
		return errors.New("trace hop chain is unavailable")
	}
	if hop.Hop == 0 {
		hop.Hop = c.next
	}
	if hop.Hop != c.next {
		return fmt.Errorf("trace hop sequence mismatch: got %d want %d", hop.Hop, c.next)
	}
	hop.PreviousSignature = c.previous
	hop.Signature = traceHopSignature(*hop, c.key)
	c.previous, c.next = hop.Signature, c.next+1
	return nil
}
func VerifyTraceHopChain(hops []TraceHop, key []byte) error {
	if len(key) == 0 {
		return errors.New("trace key is required")
	}
	previous := ""
	for i, hop := range hops {
		if hop.Hop != i+1 || hop.PreviousSignature != previous {
			return errors.New("trace hop chain sequence mismatch")
		}
		if !hmac.Equal([]byte(hop.Signature), []byte(traceHopSignature(hop, key))) {
			return errors.New("trace hop signature mismatch")
		}
		previous = hop.Signature
	}
	return nil
}
func traceHopSignature(hop TraceHop, key []byte) string {
	hop.Signature = ""
	payload, _ := json.Marshal(hop)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(hop.PreviousSignature))
	_, _ = mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}
func RedactTraceHop(hop TraceHop, includeSensitive bool) TraceHop {
	if !includeSensitive {
		hop.PrivateIPs = nil
		hop.RealAddress = ""
		hop.Certificate = nil
		hop.Token = nil
	}
	return hop
}
