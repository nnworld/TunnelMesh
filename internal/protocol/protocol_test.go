package protocol

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

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
