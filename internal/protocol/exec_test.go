package protocol

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestExecHeaderRoundTrip(t *testing.T) {
	raw, err := EncodeExecHeader(ExecHeader{TimeoutSeconds: 30, ScriptBytes: 12})
	if err != nil {
		t.Fatal(err)
	}
	got, consumed, err := ParseExecHeader(raw)
	if err != nil || consumed != len(raw) || got.TimeoutSeconds != 30 || got.ScriptBytes != 12 {
		t.Fatalf("got=%+v consumed=%d err=%v", got, consumed, err)
	}
}

func TestExecHeaderRejectsUnboundedInput(t *testing.T) {
	huge := []byte{0xff, 0xff, 0xff, 0xff}
	if _, _, err := ParseExecHeader(huge); !errors.Is(err, ErrInvalidExecHeader) {
		t.Fatalf("oversized header length: err = %v", err)
	}
	junk := append([]byte{0, 0, 0, 3}, []byte("{x}")...)
	if _, _, err := ParseExecHeader(junk); !errors.Is(err, ErrInvalidExecHeader) {
		t.Fatalf("malformed JSON: err = %v", err)
	}
	body := []byte(`{"timeout_s":-1,"script_len":1}`)
	negative := append([]byte{0, 0, 0, byte(len(body))}, body...)
	if _, _, err := ParseExecHeader(negative); !errors.Is(err, ErrInvalidExecHeader) {
		t.Fatalf("negative timeout: err = %v", err)
	}
	if _, err := EncodeExecHeader(ExecHeader{TimeoutSeconds: -1, ScriptBytes: 1}); err == nil {
		t.Fatal("encoding a negative timeout must fail")
	}
}

func TestExecHeaderReportsIncompleteInput(t *testing.T) {
	raw, _ := EncodeExecHeader(ExecHeader{TimeoutSeconds: 5, ScriptBytes: 1})
	if _, consumed, err := ParseExecHeader(raw[:len(raw)-1]); err != nil || consumed != 0 {
		t.Fatalf("partial header must wait for more bytes: consumed=%d err=%v", consumed, err)
	}
}

func TestExecRecordRoundTripKeepsKindsApart(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteExecRecord(&buf, ExecRecordStdout, []byte("out")); err != nil {
		t.Fatal(err)
	}
	if err := WriteExecRecord(&buf, ExecRecordStderr, []byte("err")); err != nil {
		t.Fatal(err)
	}
	kind, payload, err := ReadExecRecord(&buf)
	if err != nil || kind != ExecRecordStdout || string(payload) != "out" {
		t.Fatalf("first record kind=%d payload=%q err=%v", kind, payload, err)
	}
	kind, payload, err = ReadExecRecord(&buf)
	if err != nil || kind != ExecRecordStderr || string(payload) != "err" {
		t.Fatalf("second record kind=%d payload=%q err=%v", kind, payload, err)
	}
}

func TestExecRecordRefusesOversizedPayload(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteExecRecord(&buf, ExecRecordStdout, bytes.Repeat([]byte("x"), MaxExecRecordPayload+1)); err == nil {
		t.Fatal("oversized record must be refused on write")
	}
	if _, _, err := ReadExecRecord(strings.NewReader("\x01\xff\xff\xff\xff")); !errors.Is(err, ErrInvalidExecRecord) {
		t.Fatalf("oversized record on read: err = %v", err)
	}
}

func TestExecCapabilityAndProtocolNamesAreStable(t *testing.T) {
	if CapabilityAgentExec != "agent_exec.v1" || StreamProtocolExec != "exec" {
		t.Fatalf("wire names changed: %q %q", CapabilityAgentExec, StreamProtocolExec)
	}
}
