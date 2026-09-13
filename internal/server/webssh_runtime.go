package server

import (
	"net/http"
	"sync"
	"time"

	"golang.org/x/net/websocket"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
)

// xNetWebSSHUpgrader adapts golang.org/x/net/websocket to the small broker
// contract. Origin policy is enforced during the handshake, before any ticket
// is authenticated or an Agent stream is opened.
type xNetWebSSHUpgrader struct {
	security        config.SecurityConfig
	maxPayloadBytes int
}

func newXNetWebSSHUpgrader(security config.SecurityConfig, maxPayloadBytes int) xNetWebSSHUpgrader {
	if maxPayloadBytes <= 0 {
		maxPayloadBytes = 64 << 10
	}
	return xNetWebSSHUpgrader{security: security, maxPayloadBytes: maxPayloadBytes}
}

func (u xNetWebSSHUpgrader) Upgrade(w http.ResponseWriter, r *http.Request) (WSConn, error) {
	connection := make(chan *websocket.Conn, 1)
	release := make(chan struct{})
	serveDone := make(chan struct{})
	server := websocket.Server{
		Handshake: webSSHWebSocketHandshake(u.security),
		Handler: func(conn *websocket.Conn) {
			conn.MaxPayloadBytes = u.maxPayloadBytes
			connection <- conn
			<-release
		},
	}
	go func() {
		defer close(serveDone)
		server.ServeHTTP(w, r)
	}()
	select {
	case conn := <-connection:
		return &xNetWebSSHConn{conn: conn, release: release}, nil
	case <-serveDone:
		return nil, ErrWebSSHOriginNotAllowed
	}
}

func webSSHWebSocketHandshake(security config.SecurityConfig) func(*websocket.Config, *http.Request) error {
	return func(wsConfig *websocket.Config, r *http.Request) error {
		if err := validateWebSSHOrigin(security, r); err != nil {
			return err
		}
		origin, _, ok := normalizeOrigin(r.Header.Get("Origin"))
		if !ok {
			return ErrWebSSHOriginNotAllowed
		}
		wsConfig.Origin = origin
		return nil
	}
}

type webSSHWireMessage struct {
	payloadType byte
	data        []byte
}

var webSSHCodec = websocket.Codec{
	Marshal: func(v any) ([]byte, byte, error) {
		message, ok := v.(webSSHWireMessage)
		if !ok {
			return nil, websocket.UnknownFrame, ErrWebSSHMessageInvalid
		}
		return message.data, message.payloadType, nil
	},
	Unmarshal: func(data []byte, payloadType byte, v any) error {
		message, ok := v.(*webSSHWireMessage)
		if !ok {
			return ErrWebSSHMessageInvalid
		}
		message.payloadType = payloadType
		message.data = append([]byte(nil), data...)
		return nil
	},
}

type xNetWebSSHConn struct {
	conn     *websocket.Conn
	release  chan struct{}
	closeOne sync.Once
}

func (c *xNetWebSSHConn) ReadMessage() (int, []byte, error) {
	var message webSSHWireMessage
	if err := webSSHCodec.Receive(c.conn, &message); err != nil {
		return 0, nil, err
	}
	return int(message.payloadType), message.data, nil
}

func (c *xNetWebSSHConn) WriteMessage(_ int, payload []byte) error {
	return webSSHCodec.Send(c.conn, webSSHWireMessage{payloadType: websocket.BinaryFrame, data: payload})
}

func (c *xNetWebSSHConn) SetReadDeadline(t time.Time) error {
	return c.conn.SetReadDeadline(t)
}

func (c *xNetWebSSHConn) Close() error {
	c.closeOne.Do(func() {
		close(c.release)
		_ = c.conn.Close()
	})
	return nil
}

var _ deadlineWSConn = (*xNetWebSSHConn)(nil)

type deadlineWSConn interface {
	WSConn
	SetReadDeadline(time.Time) error
}
