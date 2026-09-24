package server

import (
	"errors"
	"testing"
	"time"
)

// recordingWSConn records the deadlines the wrapper asks for, so the idle-read budget can
// be verified without a browser, a socket, or a sleeping Agent.
type recordingWSConn struct {
	deadlines  []time.Time
	readErr    error
	readCalls  int
	payload    []byte
	writeCalls int
	closed     bool
}

func (c *recordingWSConn) ReceiveMessage() ([]byte, error) {
	c.readCalls++
	if c.readErr != nil {
		return nil, c.readErr
	}
	return c.payload, nil
}

func (c *recordingWSConn) SendMessage([]byte) error {
	c.writeCalls++
	return nil
}

func (c *recordingWSConn) SetReadDeadline(deadline time.Time) error {
	c.deadlines = append(c.deadlines, deadline)
	return nil
}

func (c *recordingWSConn) Close() error {
	c.closed = true
	return nil
}

// TestIdleReadDeadlineReArmsBeforeEveryRead is the half-open fix: after the hello
// the Server used to clear the read deadline forever, so a silently dead Agent
// connection stayed registered until something else noticed.
func TestIdleReadDeadlineReArmsBeforeEveryRead(t *testing.T) {
	conn := &recordingWSConn{payload: []byte("frame")}
	wrapped := &xNetWSFrameConn{conn: conn, idleReadTimeout: 90 * time.Second}

	for round := 0; round < 2; round++ {
		before := time.Now()
		if _, _, err := wrapped.ReadMessage(); err != nil {
			t.Fatalf("ReadMessage() %d error = %v", round, err)
		}
		if len(conn.deadlines) != round+1 {
			t.Fatalf("deadlines set = %d, want one per read", len(conn.deadlines))
		}
		arm := conn.deadlines[round]
		if arm.Before(before.Add(80*time.Second)) || arm.After(time.Now().Add(95*time.Second)) {
			t.Fatalf("deadline %d = %v, want about %v ahead", round, arm.Sub(before), 90*time.Second)
		}
	}
}

// TestIdleReadDeadlineDisabledLeavesHandshakeBudgetAlone protects the unauthenticated
// path: the hello timeout is armed by the caller and the wrapper must not overwrite
// it with the longer idle budget.
func TestIdleReadDeadlineDisabledLeavesHandshakeBudgetAlone(t *testing.T) {
	conn := &recordingWSConn{payload: []byte("frame")}
	wrapped := &xNetWSFrameConn{conn: conn}
	if _, _, err := wrapped.ReadMessage(); err != nil {
		t.Fatalf("ReadMessage() error = %v", err)
	}
	if len(conn.deadlines) != 0 {
		t.Fatalf("deadlines set = %v, want none when the idle budget is unset", conn.deadlines)
	}
}

func TestIdleReadDeadlinePropagatesReadErrors(t *testing.T) {
	want := errors.New("websocket: read: i/o timeout")
	conn := &recordingWSConn{readErr: want}
	wrapped := &xNetWSFrameConn{conn: conn, idleReadTimeout: time.Second}
	if _, _, err := wrapped.ReadMessage(); !errors.Is(err, want) {
		t.Fatalf("ReadMessage() error = %v, want %v", err, want)
	}
}

// TestWebsocketIdleReadTimeoutToleratesMissedHeartbeats documents why the constant is
// three times the interval both Agent and Client keep alive on: a single lost frame,
// a GC pause or one slow ping must never drop a healthy connection.
func TestWebsocketIdleReadTimeoutToleratesMissedHeartbeats(t *testing.T) {
	const heartbeatInterval = 30 * time.Second
	if websocketIdleReadTimeout < 3*heartbeatInterval {
		t.Fatalf("websocketIdleReadTimeout = %v, want at least three heartbeats", websocketIdleReadTimeout)
	}
	if websocketIdleReadTimeout > 5*heartbeatInterval {
		t.Fatalf("websocketIdleReadTimeout = %v, want a half-open connection to die within a few minutes", websocketIdleReadTimeout)
	}
}
