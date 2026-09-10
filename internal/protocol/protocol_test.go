package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestMetadataControlFramesRoundTripWithoutChangingStreamTypes(t *testing.T) {
	if FrameAgentHello != 9 || FrameAgentMetadataUpdate != 10 || FrameAgentMetadataAck != 11 {
		t.Fatalf("metadata frame values changed: hello=%d update=%d ack=%d", FrameAgentHello, FrameAgentMetadataUpdate, FrameAgentMetadataAck)
	}
	payload := AgentMetadataPayload{
		AgentID: "agent-1", NodeID: "node-1", Epoch: 4, Revision: 7,
		ReportedAt: time.Unix(1700000000, 0).UTC(),
		Items:      []AgentMetadataItem{{Name: "region", Source: "env", Value: "cn-east"}},
	}
	encoded, err := EncodeAgentMetadataPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	frame := Frame{Version: CurrentVersion, Type: FrameAgentHello, Payload: encoded}
	var wire bytes.Buffer
	if err := NewEncoder(&wire).WriteFrame(frame); err != nil {
		t.Fatal(err)
	}
	gotFrame, err := NewDecoder(&wire).ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeAgentMetadataPayload(gotFrame.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentID != payload.AgentID || got.Epoch != payload.Epoch || got.Revision != payload.Revision || got.Items[0].Value != "cn-east" {
		t.Fatalf("payload=%+v", got)
	}
}

func TestMetadataPayloadRejectsUnknownControlAndOversizedBeforeJSON(t *testing.T) {
	if err := (Frame{Version: CurrentVersion, Type: FrameType(250)}).Validate(); !errors.Is(err, ErrUnknownFrameType) {
		t.Fatalf("unknown type error = %v", err)
	}
	if _, err := DecodeAgentMetadataPayload([]byte(strings.Repeat("x", MaxMetadataPayload+1))); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("oversized metadata error = %v", err)
	}
	if _, err := DecodeAgentMetadataAckPayload([]byte(strings.Repeat("x", MaxMetadataPayload+1))); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("oversized ack error = %v", err)
	}
	var raw [16]byte
	raw[0], raw[1] = CurrentVersion, byte(FrameAgentMetadataUpdate)
	binary.BigEndian.PutUint32(raw[8:12], MaxMetadataPayload+1)
	if _, err := NewDecoder(bytes.NewReader(raw[:])).ReadFrame(); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("oversized metadata frame error = %v", err)
	}
}

func TestAgentMetadataPayloadCarriesConnectionIdentity(t *testing.T) {
	payload := AgentMetadataPayload{
		AgentID: "agent-a", InstanceID: "agent-node-a",
		ConnectionID: "conn-1", NodeID: "legacy-node", Epoch: 3,
		Revision: 7, ReportedAt: time.Unix(1700000000, 0).UTC(),
	}
	encoded, err := EncodeAgentMetadataPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeAgentMetadataPayload(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got.InstanceID != "agent-node-a" || got.ConnectionID != "conn-1" {
		t.Fatalf("connection identity = %+v", got)
	}
}

func TestAgentMetadataAckAdvertisesConnectionPool(t *testing.T) {
	payload := AgentMetadataAckPayload{
		AgentID: "agent-a", Epoch: 3, Revision: 7, Accepted: true,
		ConnectionPoolSupported: true, MaxConnectionsPerAgent: 8,
	}
	encoded, err := EncodeAgentMetadataAckPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeAgentMetadataAckPayload(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !got.ConnectionPoolSupported || got.MaxConnectionsPerAgent != 8 {
		t.Fatalf("connection pool ack = %+v", got)
	}
}

func TestAgentMetadataConnectionFieldsRemainOptionalForLegacyAgents(t *testing.T) {
	got, err := DecodeAgentMetadataPayload([]byte(`{"agent_id":"agent-a","node_id":"node-a","epoch":1,"revision":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.InstanceID != "" || got.ConnectionID != "" {
		t.Fatalf("legacy payload gained identity = %+v", got)
	}

	ack, err := DecodeAgentMetadataAckPayload([]byte(`{"agent_id":"agent-a","epoch":1,"revision":1,"accepted":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if ack.ConnectionPoolSupported || ack.MaxConnectionsPerAgent != 0 {
		t.Fatalf("legacy ack gained capability = %+v", ack)
	}
}

func TestFrameRoundTrip(t *testing.T) {
	in := Frame{Version: CurrentVersion, Type: FrameData, Flags: FlagFin, StreamID: 42, Window: 8192, Payload: []byte("hello")}
	var b bytes.Buffer
	if err := NewEncoder(&b).WriteFrame(in); err != nil {
		t.Fatal(err)
	}
	out, err := NewDecoder(&b).ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if out.Version != in.Version || out.Type != in.Type || out.Flags != in.Flags || out.StreamID != in.StreamID || out.Window != in.Window || !bytes.Equal(out.Payload, in.Payload) {
		t.Fatalf("round trip mismatch: %#v != %#v", out, in)
	}
}

func TestFrameRejectsInvalid(t *testing.T) {
	tooLarge := make([]byte, MaxPayload+1)
	if err := (Frame{Version: CurrentVersion, Type: FrameData, StreamID: 1, Payload: tooLarge}).Validate(); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("expected payload limit, got %v", err)
	}
	if err := (Frame{Version: 99, Type: FrameData, StreamID: 1}).Validate(); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("version: %v", err)
	}
	if err := (Frame{Version: CurrentVersion, Type: FrameType(99), StreamID: 1}).Validate(); !errors.Is(err, ErrUnknownFrameType) {
		t.Fatalf("type: %v", err)
	}
	var b bytes.Buffer
	if err := NewEncoder(&b).WriteFrame(Frame{Version: CurrentVersion, Type: FrameData, StreamID: 1, Payload: []byte("abc")}); err != nil {
		t.Fatal(err)
	}
	raw := b.Bytes()[:b.Len()-1]
	if _, err := NewDecoder(bytes.NewReader(raw)).ReadFrame(); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated: %v", err)
	}
}

func TestDecoderRejectsUnknownVersionAndType(t *testing.T) {
	var raw [16]byte
	raw[0], raw[1] = 9, byte(FrameData)
	if _, err := NewDecoder(bytes.NewReader(raw[:])).ReadFrame(); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("version: %v", err)
	}
	raw[0], raw[1] = CurrentVersion, 99
	if _, err := NewDecoder(bytes.NewReader(raw[:])).ReadFrame(); !errors.Is(err, ErrUnknownFrameType) {
		t.Fatalf("type: %v", err)
	}
}

func TestDecoderCustomLimitCannotExceedProtocolLimit(t *testing.T) {
	var raw [16]byte
	raw[0], raw[1] = CurrentVersion, byte(FrameData)
	binary.BigEndian.PutUint32(raw[4:8], 1)
	binary.BigEndian.PutUint32(raw[8:12], MaxPayload+1)
	dec := NewDecoder(bytes.NewReader(raw[:]))
	dec.MaxPayload = MaxPayload * 4
	if _, err := dec.ReadFrame(); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("payload limit: %v", err)
	}
}

func TestStreamStateTransitionsAndWindow(t *testing.T) {
	s, err := NewStreamState(7, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Handle(Frame{Version: CurrentVersion, Type: FrameOpenStream, StreamID: 7, Window: 1024}); err != nil {
		t.Fatal(err)
	}
	if s.Status() != StreamOpen {
		t.Fatalf("status=%v", s.Status())
	}
	if err := s.Handle(Frame{Version: CurrentVersion, Type: FrameData, StreamID: 7, Payload: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	if s.ReceiveWindow() != 1023 {
		t.Fatalf("window=%d", s.ReceiveWindow())
	}
	if err := s.Handle(Frame{Version: CurrentVersion, Type: FrameWindowUpdate, StreamID: 7, Window: 10}); err != nil {
		t.Fatal(err)
	}
	if s.SendWindow() != 1034 {
		t.Fatalf("send window=%d", s.SendWindow())
	}
	if err := s.Handle(Frame{Version: CurrentVersion, Type: FrameHalfClose, StreamID: 7}); err != nil {
		t.Fatal(err)
	}
	if s.Status() != StreamHalfClosedRemote {
		t.Fatalf("status=%v", s.Status())
	}
	if err := s.HalfCloseLocal(); err != nil {
		t.Fatal(err)
	}
	if s.Status() != StreamClosed {
		t.Fatalf("status=%v", s.Status())
	}
	if err := s.Handle(Frame{Version: CurrentVersion, Type: FrameData, StreamID: 7, Payload: []byte("bad")}); !errors.Is(err, ErrStreamClosed) {
		t.Fatalf("closed data: %v", err)
	}
}

func TestStreamReset(t *testing.T) {
	s, _ := NewStreamState(1, 10)
	_ = s.Handle(Frame{Version: CurrentVersion, Type: FrameOpenStream, StreamID: 1})
	if err := s.Handle(Frame{Version: CurrentVersion, Type: FrameReset, StreamID: 1}); err != nil {
		t.Fatal(err)
	}
	if s.Status() != StreamReset {
		t.Fatalf("status=%v", s.Status())
	}
	if err := s.Reset(); !errors.Is(err, ErrStreamReset) {
		t.Fatalf("reset twice: %v", err)
	}
}

func TestStreamWindowUpdateRejectedAfterTerminalState(t *testing.T) {
	closed, _ := NewStreamState(2, 10)
	_ = closed.Handle(Frame{Version: CurrentVersion, Type: FrameOpenStream, StreamID: 2})
	_ = closed.Handle(Frame{Version: CurrentVersion, Type: FrameHalfClose, StreamID: 2})
	_ = closed.HalfCloseLocal()
	before := closed.SendWindow()
	if err := closed.Handle(Frame{Version: CurrentVersion, Type: FrameWindowUpdate, StreamID: 2, Window: 5}); !errors.Is(err, ErrStreamClosed) {
		t.Fatalf("closed update: %v", err)
	}
	if closed.SendWindow() != before {
		t.Fatalf("closed window changed: %d -> %d", before, closed.SendWindow())
	}

	reset, _ := NewStreamState(3, 10)
	_ = reset.Handle(Frame{Version: CurrentVersion, Type: FrameOpenStream, StreamID: 3})
	_ = reset.Handle(Frame{Version: CurrentVersion, Type: FrameReset, StreamID: 3})
	before = reset.SendWindow()
	if err := reset.Handle(Frame{Version: CurrentVersion, Type: FrameWindowUpdate, StreamID: 3, Window: 5}); !errors.Is(err, ErrStreamReset) {
		t.Fatalf("reset update: %v", err)
	}
	if reset.SendWindow() != before {
		t.Fatalf("reset window changed: %d -> %d", before, reset.SendWindow())
	}
}

func TestUDPAssociationPreservesDatagramBoundaries(t *testing.T) {
	a := NewUDPAssociation(64)
	for _, want := range [][]byte{[]byte("one"), []byte{}, []byte("three")} {
		encoded, err := a.EncodeDatagram(want)
		if err != nil {
			t.Fatal(err)
		}
		got, err := a.DecodeDatagram(encoded)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%q != %q", got, want)
		}
	}
	if _, err := a.EncodeDatagram(make([]byte, 65)); !errors.Is(err, ErrDatagramTooLarge) {
		t.Fatalf("large datagram: %v", err)
	}
}
