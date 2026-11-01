package protocol

import (
	"bufio"
	"bytes"
	"io"
	"sync"
	"time"
)

// DeadlineStream is the byte stream a frame transport runs over. A TCP or TLS
// connection and a QUIC stream both satisfy it, so one framing implementation serves
// every transport.
type DeadlineStream interface {
	io.ReadWriteCloser
	SetDeadline(time.Time) error
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
}

// StreamConn carries frames over a byte stream: one encoded frame per Send, decoded
// by length-prefixed header on Receive. Writes are serialised so concurrent senders
// cannot interleave bytes, and a failed or timed-out write closes the stream because a
// partial frame corrupts everything after it.
type StreamConn struct {
	conn         DeadlineStream
	reader       *bufio.Reader
	decoder      *Decoder
	writeTimeout time.Duration
	idleTimeout  time.Duration

	writeMu   sync.Mutex
	closeOnce sync.Once
	closeErr  error
}

// NewStreamConn wraps conn. writeTimeout bounds every Send; zero disables the bound.
func NewStreamConn(conn DeadlineStream, writeTimeout time.Duration) *StreamConn {
	reader := bufio.NewReader(conn)
	return &StreamConn{conn: conn, reader: reader, decoder: NewDecoder(reader), writeTimeout: writeTimeout}
}

// SetIdleTimeout sets how long the next Receive may wait. It is applied before every
// read, so a peer that goes silent is detected even if it never closes the stream.
// Callers set it from the goroutine that calls Receive.
func (c *StreamConn) SetIdleTimeout(d time.Duration) { c.idleTimeout = d }

// Send encodes one frame and writes it under the write lock.
func (c *StreamConn) Send(f Frame) error {
	var buf bytes.Buffer
	if err := NewEncoder(&buf).WriteFrame(f); err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.writeTimeout > 0 {
		if err := c.conn.SetWriteDeadline(time.Now().Add(c.writeTimeout)); err != nil {
			_ = c.Close()
			return err
		}
	}
	if _, err := c.conn.Write(buf.Bytes()); err != nil {
		_ = c.Close()
		return err
	}
	return nil
}

// Receive reads the next frame. It must be called from one goroutine.
func (c *StreamConn) Receive() (Frame, error) {
	if c.idleTimeout > 0 {
		if err := c.conn.SetReadDeadline(time.Now().Add(c.idleTimeout)); err != nil {
			return Frame{}, err
		}
	}
	return c.decoder.ReadFrame()
}

// Close closes the underlying stream. It is idempotent and safe to call from any
// goroutine, including while a Send is blocked.
func (c *StreamConn) Close() error {
	c.closeOnce.Do(func() { c.closeErr = c.conn.Close() })
	return c.closeErr
}
