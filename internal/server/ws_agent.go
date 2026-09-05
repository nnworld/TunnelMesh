package server

import (
	"bytes"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"io"
	"sync"
)

// WSConn keeps the server independent from a particular WebSocket library.
type WSConn interface {
	ReadMessage() (int, []byte, error)
	WriteMessage(int, []byte) error
	Close() error
}
type WSFrameTransport struct {
	conn WSConn
	mu   sync.Mutex
}

func NewWSFrameTransport(c WSConn) *WSFrameTransport { return &WSFrameTransport{conn: c} }
func (t *WSFrameTransport) Send(f protocol.Frame) error {
	var b bytes.Buffer
	if err := protocol.NewEncoder(&b).WriteFrame(f); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.conn.WriteMessage(2, b.Bytes())
}
func (t *WSFrameTransport) Receive() (protocol.Frame, error) {
	_, b, err := t.conn.ReadMessage()
	if err != nil {
		return protocol.Frame{}, err
	}
	return protocol.NewDecoder(bytes.NewReader(b)).ReadFrame()
}
func (t *WSFrameTransport) Close() error { return t.conn.Close() }

func ServeAgentFrames(tr *WSFrameTransport, onFrame func(protocol.Frame) error) error {
	for {
		f, err := tr.Receive()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if f.Type == protocol.FrameGoAway {
			return nil
		}
		if onFrame != nil {
			if err := onFrame(f); err != nil {
				return err
			}
		}
	}
}
