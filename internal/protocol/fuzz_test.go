package protocol

import (
	"bytes"
	"testing"
)

func FuzzDecoderRejectsMalformedInput(f *testing.F) {
	var b bytes.Buffer
	_ = NewEncoder(&b).WriteFrame(Frame{Version: CurrentVersion, Type: FrameData, StreamID: 1, Payload: []byte("seed")})
	f.Add(b.Bytes())
	f.Add([]byte{0, 1, 2})
	f.Fuzz(func(t *testing.T, raw []byte) {
		dec := NewDecoder(bytes.NewReader(raw))
		_, _ = dec.ReadFrame()
	})
}

func FuzzStreamStateNeverPanics(f *testing.F) {
	f.Add(uint8(1), uint32(1), []byte("data"))
	f.Fuzz(func(t *testing.T, typ uint8, n uint32, payload []byte) {
		s, err := NewStreamState(1, 1024)
		if err != nil {
			t.Fatal(err)
		}
		_ = s.Handle(Frame{Version: CurrentVersion, Type: FrameOpenStream, StreamID: 1})
		_ = s.Handle(Frame{Version: CurrentVersion, Type: FrameType(typ), StreamID: n, Payload: payload})
	})
}
