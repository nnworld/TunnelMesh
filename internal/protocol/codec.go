package protocol

import (
	"encoding/binary"
	"fmt"
	"io"
)

const headerSize = 16

// Encoder writes frames using a fixed 16-byte network-order header.
// Layout: version(1), type(1), flags(2), stream id(4), payload length(4), window(4).
type Encoder struct {
	w          io.Writer
	MaxPayload int
}

func NewEncoder(w io.Writer) *Encoder { return &Encoder{w: w, MaxPayload: MaxPayload} }

func (e *Encoder) WriteFrame(f Frame) error {
	if e == nil || e.w == nil {
		return fmt.Errorf("protocol: nil encoder")
	}
	if e.MaxPayload <= 0 {
		e.MaxPayload = MaxPayload
	}
	if len(f.Payload) > e.MaxPayload {
		return ErrPayloadTooLarge
	}
	if err := f.Validate(); err != nil {
		return err
	}
	var h [headerSize]byte
	h[0], h[1] = f.Version, byte(f.Type)
	binary.BigEndian.PutUint16(h[2:4], f.Flags)
	binary.BigEndian.PutUint32(h[4:8], f.StreamID)
	binary.BigEndian.PutUint32(h[8:12], uint32(len(f.Payload)))
	binary.BigEndian.PutUint32(h[12:16], f.Window)
	if err := writeFull(e.w, h[:]); err != nil {
		return err
	}
	if len(f.Payload) > 0 {
		return writeFull(e.w, f.Payload)
	}
	return nil
}

func (e *Encoder) Encode(f Frame) error { return e.WriteFrame(f) }

func writeFull(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if n > 0 {
			p = p[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func Encode(w io.Writer, f Frame) error { return NewEncoder(w).WriteFrame(f) }

// Decoder reads bounded frames and never allocates based on an untrusted length.
type Decoder struct {
	r          io.Reader
	MaxPayload int
}

func NewDecoder(r io.Reader) *Decoder { return &Decoder{r: r, MaxPayload: MaxPayload} }

func (d *Decoder) ReadFrame() (Frame, error) {
	if d == nil || d.r == nil {
		return Frame{}, fmt.Errorf("protocol: nil decoder")
	}
	effectiveMaxPayload := d.MaxPayload
	if effectiveMaxPayload <= 0 || effectiveMaxPayload > MaxPayload {
		effectiveMaxPayload = MaxPayload
	}
	var h [headerSize]byte
	if _, err := io.ReadFull(d.r, h[:]); err != nil {
		return Frame{}, err
	}
	f := Frame{Version: h[0], Type: FrameType(h[1]), Flags: binary.BigEndian.Uint16(h[2:4]), StreamID: binary.BigEndian.Uint32(h[4:8]), Window: binary.BigEndian.Uint32(h[12:16])}
	n := binary.BigEndian.Uint32(h[8:12])
	// Reject the protocol namespace before inspecting the untrusted payload
	// length, so a frame from a future version cannot be misclassified.
	if f.Version != CurrentVersion {
		return Frame{}, fmt.Errorf("%w: %d", ErrUnsupportedVersion, f.Version)
	}
	if !knownFrameType(f.Type) {
		return Frame{}, fmt.Errorf("%w: %d", ErrUnknownFrameType, f.Type)
	}
	if n > uint32(effectiveMaxPayload) {
		return Frame{}, ErrPayloadTooLarge
	}
	if isMetadataFrame(f.Type) && n > MaxMetadataPayload {
		return Frame{}, ErrPayloadTooLarge
	}
	if n > 0 {
		f.Payload = make([]byte, n)
		if _, err := io.ReadFull(d.r, f.Payload); err != nil {
			return Frame{}, err
		}
	}
	if err := f.Validate(); err != nil {
		return Frame{}, err
	}
	return f, nil
}

func (d *Decoder) Decode() (Frame, error) { return d.ReadFrame() }
func Decode(r io.Reader) (Frame, error)   { return NewDecoder(r).ReadFrame() }
