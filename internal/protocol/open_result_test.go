package protocol

import (
	"errors"
	"strings"
	"testing"
)

func TestOpenResultFrameRoundTrip(t *testing.T) {
	payload := OpenResultPayload{
		Accepted: true, Stage: OpenResultStageConnect, Code: OpenResultCodeOK,
	}
	encoded, err := EncodeOpenResultPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	frame := Frame{Version: CurrentVersion, Type: FrameOpenResult, StreamID: 7, Payload: encoded}
	if err := frame.Validate(); err != nil {
		t.Fatal(err)
	}
	got, err := DecodeOpenResultPayload(frame.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if got != payload {
		t.Fatalf("open result = %+v, want %+v", got, payload)
	}
}

func TestOpenResultFrameRequiresStream(t *testing.T) {
	frame := Frame{Version: CurrentVersion, Type: FrameOpenResult}
	if err := frame.Validate(); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("zero stream error = %v, want ErrInvalidFrame", err)
	}
}

func TestOpenResultPayloadRejectsOversizedAndInvalidValues(t *testing.T) {
	if _, err := DecodeOpenResultPayload([]byte(strings.Repeat("x", MaxOpenResultPayload+1))); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("oversized payload error = %v, want ErrPayloadTooLarge", err)
	}
	if _, err := EncodeOpenResultPayload(OpenResultPayload{Accepted: true, Stage: OpenResultStageConnect, Code: OpenResultCodeForbidden}); err == nil {
		t.Fatal("accepted result must use ok code")
	}
	if _, err := EncodeOpenResultPayload(OpenResultPayload{Stage: OpenResultStageDNS, Code: OpenResultCodeOK}); err == nil {
		t.Fatal("failure result must not use ok code")
	}
	if _, err := EncodeOpenResultPayload(OpenResultPayload{Stage: OpenResultStage("bad"), Code: OpenResultCodeTimeout}); err == nil {
		t.Fatal("unknown stage must be rejected")
	}
	if _, err := EncodeOpenResultPayload(OpenResultPayload{Stage: OpenResultStageDNS, Code: OpenResultCode("bad")}); err == nil {
		t.Fatal("unknown code must be rejected")
	}
}

func TestOpenResultStagesAndCodesRoundTrip(t *testing.T) {
	for _, stage := range []OpenResultStage{
		OpenResultStageAuthorization, OpenResultStageSelection, OpenResultStageRelay,
		OpenResultStageQueue, OpenResultStageDNS, OpenResultStageConnect,
		OpenResultStagePolicy, OpenResultStageProtocol,
	} {
		for _, code := range []OpenResultCode{
			OpenResultCodeForbidden, OpenResultCodeAgentOffline, OpenResultCodeQueueFull,
			OpenResultCodeTimeout, OpenResultCodeNetworkUnreachable, OpenResultCodeHostUnreachable,
			OpenResultCodeConnectionRefused, OpenResultCodeUnsupportedCapability,
			OpenResultCodeInternalError,
		} {
			payload := OpenResultPayload{Stage: stage, Code: code, Retryable: true, RetryAfterMS: 1000}
			encoded, err := EncodeOpenResultPayload(payload)
			if err != nil {
				t.Fatalf("encode %s/%s: %v", stage, code, err)
			}
			got, err := DecodeOpenResultPayload(encoded)
			if err != nil {
				t.Fatalf("decode %s/%s: %v", stage, code, err)
			}
			if got != payload {
				t.Fatalf("round trip %s/%s = %+v, want %+v", stage, code, got, payload)
			}
		}
	}
}
