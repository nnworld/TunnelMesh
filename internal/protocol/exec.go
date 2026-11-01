package protocol

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// CapabilityAgentExec says the agent runs scripts sent through the "exec" stream
// protocol. It is advertised only when the agent has script execution enabled, so a
// server never asks an agent that would refuse.
const CapabilityAgentExec = "agent_exec.v1"

// StreamProtocolExec opens a script run on an agent. The request is an ExecHeader
// followed by the script bytes; the response is a sequence of ExecRecord values.
const StreamProtocolExec = "exec"

// Output record kinds. Each record is kind(1) | length(4, big-endian) | payload.
const (
	ExecRecordStdout byte = 1
	ExecRecordStderr byte = 2
	ExecRecordExit   byte = 3
	ExecRecordError  byte = 4
)

const (
	// MaxExecHeaderBytes bounds the request header. It holds two small numbers, so
	// anything larger is not a header.
	MaxExecHeaderBytes = 1024
	// MaxExecRecordPayload bounds one output record, so a single write cannot pin
	// unbounded memory on either side.
	MaxExecRecordPayload = 64 << 10
)

var (
	ErrInvalidExecHeader = errors.New("protocol: invalid exec header")
	ErrInvalidExecRecord = errors.New("protocol: invalid exec record")
)

// ExecHeader is the request that precedes the script on an exec stream.
type ExecHeader struct {
	TimeoutSeconds int `json:"timeout_s"`
	ScriptBytes    int `json:"script_len"`
}

// ExecExit is the payload of the final ExecRecordExit. It carries outcome facts only,
// never output or script text.
type ExecExit struct {
	ExitCode   int   `json:"exit_code"`
	Signaled   bool  `json:"signaled"`
	TimedOut   bool  `json:"timed_out"`
	Truncated  bool  `json:"truncated"`
	DurationMS int64 `json:"duration_ms"`
}

// EncodeExecHeader frames the header as a 4-byte length followed by JSON.
func EncodeExecHeader(h ExecHeader) ([]byte, error) {
	if h.TimeoutSeconds < 0 || h.ScriptBytes < 0 {
		return nil, ErrInvalidExecHeader
	}
	body, err := json.Marshal(h)
	if err != nil {
		return nil, err
	}
	if len(body) > MaxExecHeaderBytes {
		return nil, ErrInvalidExecHeader
	}
	out := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(out, uint32(len(body)))
	copy(out[4:], body)
	return out, nil
}

// ParseExecHeader reads one header from the front of buf. It returns consumed == 0 and
// no error while buf is still short, so a caller can keep appending bytes.
func ParseExecHeader(buf []byte) (ExecHeader, int, error) {
	if len(buf) < 4 {
		return ExecHeader{}, 0, nil
	}
	length := binary.BigEndian.Uint32(buf)
	if length == 0 || length > MaxExecHeaderBytes {
		return ExecHeader{}, 0, ErrInvalidExecHeader
	}
	if len(buf) < 4+int(length) {
		return ExecHeader{}, 0, nil
	}
	var h ExecHeader
	if err := json.Unmarshal(buf[4:4+length], &h); err != nil {
		return ExecHeader{}, 0, ErrInvalidExecHeader
	}
	if h.TimeoutSeconds < 0 || h.ScriptBytes < 0 {
		return ExecHeader{}, 0, ErrInvalidExecHeader
	}
	return h, 4 + int(length), nil
}

// WriteExecRecord writes one output record in a single Write, so records written from
// concurrent goroutines never interleave when the writer serialises calls.
func WriteExecRecord(w io.Writer, kind byte, payload []byte) error {
	if len(payload) > MaxExecRecordPayload {
		return fmt.Errorf("%w: payload %d bytes", ErrInvalidExecRecord, len(payload))
	}
	buf := make([]byte, 5+len(payload))
	buf[0] = kind
	binary.BigEndian.PutUint32(buf[1:5], uint32(len(payload)))
	copy(buf[5:], payload)
	_, err := w.Write(buf)
	return err
}

// ReadExecRecord reads one record and refuses a length beyond the limit before reading
// the payload.
func ReadExecRecord(r io.Reader) (byte, []byte, error) {
	var header [5]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, nil, err
	}
	length := binary.BigEndian.Uint32(header[1:])
	if length > MaxExecRecordPayload {
		return 0, nil, ErrInvalidExecRecord
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return header[0], payload, nil
}
