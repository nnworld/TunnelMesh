# Task 6 fix round 2 review package

Fix base: Task 6 fix round 1 reviewed snapshot (HEAD remained `163fe12121d2f839ea4bf4907f55a8e7dd835057`)

## Changed files

```text
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/agent/session.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/agent/session.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/agent/session_test.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/agent/session_test.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/agent/websocket.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/agent/websocket.go differ
Only in .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/agent: websocket_test.go
Only in .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/cli: agent_runtime_test.go
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/cli/root.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/cli/root.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/server/agent_relay_transport.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/server/agent_relay_transport.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/server/agent_relay_transport_test.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/server/agent_relay_transport_test.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/server/ws_client.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/server/ws_client.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/server/ws_client_test.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/server/ws_client_test.go differ
```

## Full fix-only diff

```diff
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/agent/session.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/agent/session.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/agent/session.go	2026-09-06 18:38:01
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/agent/session.go	2026-09-06 19:30:16
@@ -6,6 +6,7 @@
 	"io"
 	"math/rand"
 	"reflect"
+	"strings"
 	"sync"
 	"sync/atomic"
 	"time"
@@ -16,11 +17,17 @@
 var ErrStreamNotFound = errors.New("agent: stream not found")
 var ErrDuplicateStream = errors.New("agent: duplicate stream")
 
+const agentStreamResetMessage = "stream rejected"
+
 type StreamOpenPayload = protocol.StreamOpenPayload
 type StreamDialFunc func(context.Context, string, string, int) (io.ReadWriteCloser, error)
+type FrameSender func(protocol.Frame) error
 type streamEntry struct {
 	conn       io.ReadWriteCloser
 	generation uint64
+	protocol   string
+	localHalf  bool
+	remoteHalf bool
 }
 type StreamDispatcher struct {
 	ctx        context.Context
@@ -29,30 +36,25 @@
 	streams    map[uint32]*streamEntry
 	generation uint64
 	mu         sync.Mutex
-	onFrame    func(protocol.Frame)
+	send       FrameSender
+	readers    sync.WaitGroup
+	closed     bool
 }
 
 func NewStreamDispatcher(d Dialer, override StreamDialFunc) *StreamDispatcher {
-	if override == nil {
-		override = func(ctx context.Context, proto, host string, port int) (io.ReadWriteCloser, error) {
-			switch proto {
-			case "tcp":
-				return d.DialTCP(ctx, host, port)
-			case "udp":
-				return d.DialUDP(ctx, host, port)
-			case "http":
-				if d.HTTPStream != nil {
-					return d.HTTPStream(ctx, host, port)
-				}
-				return d.DialHTTPStream(ctx, host, port)
-			default:
-				return nil, errors.New("agent: unsupported stream protocol")
-			}
+	return NewStreamDispatcherWithSender(d, override, nil)
+}
+func NewStreamDispatcherWithCallback(d Dialer, override StreamDialFunc, cb func(protocol.Frame)) *StreamDispatcher {
+	var sender FrameSender
+	if cb != nil {
+		sender = func(frame protocol.Frame) error {
+			cb(frame)
+			return nil
 		}
 	}
-	return NewStreamDispatcherWithCallback(d, override, nil)
+	return NewStreamDispatcherWithSender(d, override, sender)
 }
-func NewStreamDispatcherWithCallback(d Dialer, override StreamDialFunc, cb func(protocol.Frame)) *StreamDispatcher {
+func NewStreamDispatcherWithSender(d Dialer, override StreamDialFunc, sender FrameSender) *StreamDispatcher {
 	if override == nil {
 		override = func(ctx context.Context, proto, host string, port int) (io.ReadWriteCloser, error) {
 			switch proto {
@@ -71,7 +73,7 @@
 		}
 	}
 	ctx, cancel := context.WithCancel(context.Background())
-	return &StreamDispatcher{ctx: ctx, cancel: cancel, dial: override, streams: make(map[uint32]*streamEntry), onFrame: cb}
+	return &StreamDispatcher{ctx: ctx, cancel: cancel, dial: override, streams: make(map[uint32]*streamEntry), send: sender}
 }
 func (d *StreamDispatcher) Handle(f protocol.Frame) error {
 	if d == nil {
@@ -91,38 +93,82 @@
 			return err
 		}
 		d.mu.Lock()
+		if d.closed {
+			d.mu.Unlock()
+			_ = c.Close()
+			return ErrStreamNotFound
+		}
 		if _, exists := d.streams[f.StreamID]; exists {
 			d.mu.Unlock()
 			_ = c.Close()
 			return ErrDuplicateStream
 		}
 		d.generation++
-		entry := &streamEntry{conn: c, generation: d.generation}
+		entry := &streamEntry{conn: c, generation: d.generation, protocol: p.Protocol}
 		d.streams[f.StreamID] = entry
+		d.readers.Add(1)
 		d.mu.Unlock()
 		go d.readBack(f.StreamID, entry)
 		return nil
 	case protocol.FrameData:
 		d.mu.Lock()
 		entry, ok := d.streams[f.StreamID]
+		localHalf := ok && entry.localHalf
 		d.mu.Unlock()
 		if !ok {
 			return ErrStreamNotFound
 		}
-		_, err := entry.conn.Write(f.Payload)
-		return err
-	case protocol.FrameHalfClose, protocol.FrameReset:
+		if localHalf {
+			return d.rejectAndClose(f.StreamID, entry, protocol.ErrInvalidFrame)
+		}
+		written, err := entry.conn.Write(f.Payload)
+		if err == nil && written != len(f.Payload) {
+			err = io.ErrShortWrite
+		}
+		if err != nil {
+			return d.rejectAndClose(f.StreamID, entry, err)
+		}
+		return nil
+	case protocol.FrameHalfClose:
 		d.mu.Lock()
 		entry, ok := d.streams[f.StreamID]
-		d.mu.Unlock()
 		if !ok {
+			d.mu.Unlock()
 			return ErrStreamNotFound
 		}
+		if entry.localHalf {
+			d.mu.Unlock()
+			return nil
+		}
+		entry.localHalf = true
+		complete := entry.remoteHalf
+		d.mu.Unlock()
+		halfCloser, ok := entry.conn.(interface{ CloseWrite() error })
+		if !ok {
+			return d.rejectAndClose(f.StreamID, entry, errors.New("agent: target stream does not support half-close"))
+		}
+		if err := halfCloser.CloseWrite(); err != nil {
+			return d.rejectAndClose(f.StreamID, entry, err)
+		}
+		if complete {
+			d.mu.Lock()
+			if current, exists := d.streams[f.StreamID]; exists && current == entry {
+				delete(d.streams, f.StreamID)
+			}
+			d.mu.Unlock()
+			return entry.conn.Close()
+		}
+		return nil
+	case protocol.FrameReset:
 		d.mu.Lock()
-		if current, exists := d.streams[f.StreamID]; exists && current == entry {
+		entry, ok := d.streams[f.StreamID]
+		if ok {
 			delete(d.streams, f.StreamID)
 		}
 		d.mu.Unlock()
+		if !ok {
+			return ErrStreamNotFound
+		}
 		return entry.conn.Close()
 	default:
 		return nil
@@ -130,37 +176,99 @@
 }
 
 func (d *StreamDispatcher) readBack(id uint32, entry *streamEntry) {
-	buf := make([]byte, 32<<10)
+	defer d.readers.Done()
+	bufferSize := 32 << 10
+	if strings.EqualFold(entry.protocol, "udp") {
+		bufferSize = protocol.MaxPayload
+	}
+	buf := make([]byte, bufferSize)
 	for {
 		n, err := entry.conn.Read(buf)
-		if n > 0 && d.onFrame != nil {
-			d.onFrame(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: id, Payload: append([]byte(nil), buf[:n]...)})
+		if d.ctx.Err() != nil {
+			return
 		}
+		if n > 0 && d.send != nil {
+			if sendErr := d.send(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: id, Payload: append([]byte(nil), buf[:n]...)}); sendErr != nil {
+				d.removeAndClose(id, entry)
+				return
+			}
+		}
 		if err != nil {
+			if !errors.Is(err, io.EOF) {
+				d.removeAndClose(id, entry)
+				_ = d.sendReset(id)
+				return
+			}
 			d.mu.Lock()
 			current, ok := d.streams[id]
 			stale := !ok || current != entry
 			if !stale {
+				entry.remoteHalf = true
+			}
+			complete := !stale && (entry.localHalf || d.send == nil)
+			if complete {
 				delete(d.streams, id)
 			}
-			cb := d.onFrame
+			sender := d.send
 			d.mu.Unlock()
-			if !stale && cb != nil {
-				cb(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: id})
+			if !stale && sender != nil {
+				if sendErr := sender(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: id}); sendErr != nil {
+					d.removeAndClose(id, entry)
+					return
+				}
 			}
+			if complete {
+				_ = entry.conn.Close()
+			}
 			return
 		}
 	}
 }
+
+func (d *StreamDispatcher) sendReset(id uint32) error {
+	if d.send == nil {
+		return nil
+	}
+	return d.send(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: id, Payload: []byte(agentStreamResetMessage)})
+}
+
+func (d *StreamDispatcher) rejectAndClose(id uint32, entry *streamEntry, reason error) error {
+	d.removeAndClose(id, entry)
+	if d.send == nil {
+		return reason
+	}
+	return d.sendReset(id)
+}
+
+func (d *StreamDispatcher) removeAndClose(id uint32, entry *streamEntry) {
+	d.mu.Lock()
+	if d.streams[id] == entry {
+		delete(d.streams, id)
+	}
+	d.mu.Unlock()
+	_ = entry.conn.Close()
+}
 func (d *StreamDispatcher) Close() error {
 	d.cancel()
 	d.mu.Lock()
-	defer d.mu.Unlock()
+	if d.closed {
+		d.mu.Unlock()
+		d.readers.Wait()
+		return nil
+	}
+	d.closed = true
+	connections := make([]io.Closer, 0, len(d.streams))
 	for id, entry := range d.streams {
-		_ = entry.conn.Close()
 		delete(d.streams, id)
+		connections = append(connections, entry.conn)
 	}
-	return nil
+	d.mu.Unlock()
+	var closeErr error
+	for _, conn := range connections {
+		closeErr = errors.Join(closeErr, conn.Close())
+	}
+	d.readers.Wait()
+	return closeErr
 }
 
 type FrameTransport interface {
@@ -279,8 +387,20 @@
 func (s *Session) SendMetadata(ctx context.Context) error { return s.ReportMetadata(ctx) }
 
 func (s *Session) send(frame protocol.Frame) error {
+	return s.Send(frame)
+}
+
+// Send serializes dispatcher and session control frames on the authenticated
+// transport and rejects writes once session shutdown begins.
+func (s *Session) Send(frame protocol.Frame) error {
+	if s == nil || s.transport == nil || s.closed.Load() {
+		return ErrAgentSessionClosed
+	}
 	s.sendMu.Lock()
 	defer s.sendMu.Unlock()
+	if s.closed.Load() {
+		return ErrAgentSessionClosed
+	}
 	return s.transport.Send(frame)
 }
 
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/agent/session_test.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/agent/session_test.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/agent/session_test.go	2026-09-06 18:38:01
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/agent/session_test.go	2026-09-06 19:30:16
@@ -1,6 +1,7 @@
 package agent
 
 import (
+	"bytes"
 	"context"
 	"encoding/json"
 	"errors"
@@ -9,6 +10,7 @@
 	"io"
 	"net/http/httptest"
 	"strings"
+	"sync"
 	"testing"
 	"time"
 )
@@ -103,14 +105,92 @@
 
 type blockingStreamConn struct {
 	*streamConn
-	release chan struct{}
+	release     chan struct{}
+	releaseOnce sync.Once
 }
 
 func (c *blockingStreamConn) Read([]byte) (int, error) {
 	<-c.release
 	return 0, io.EOF
 }
+func (c *blockingStreamConn) Close() error {
+	c.releaseOnce.Do(func() {
+		close(c.release)
+		_ = c.streamConn.Close()
+	})
+	return nil
+}
 
+type dispatcherReadResult struct {
+	payload []byte
+	err     error
+}
+
+type directionalDispatcherConn struct {
+	reads       chan dispatcherReadResult
+	writes      chan []byte
+	writeClosed chan struct{}
+	closed      chan struct{}
+	writeOnce   sync.Once
+	closeOnce   sync.Once
+}
+
+type waitableDispatcherConn struct {
+	readStarted chan struct{}
+	closeCalled chan struct{}
+	allowExit   chan struct{}
+	readOnce    sync.Once
+	closeOnce   sync.Once
+}
+
+type writeErrorDispatcherConn struct {
+	*directionalDispatcherConn
+	written int
+	err     error
+}
+
+func (c *writeErrorDispatcherConn) Write([]byte) (int, error) { return c.written, c.err }
+
+func newWaitableDispatcherConn() *waitableDispatcherConn {
+	return &waitableDispatcherConn{readStarted: make(chan struct{}), closeCalled: make(chan struct{}), allowExit: make(chan struct{})}
+}
+
+func (c *waitableDispatcherConn) Read([]byte) (int, error) {
+	c.readOnce.Do(func() { close(c.readStarted) })
+	<-c.allowExit
+	return 0, io.EOF
+}
+func (*waitableDispatcherConn) Write(payload []byte) (int, error) { return len(payload), nil }
+func (c *waitableDispatcherConn) Close() error {
+	c.closeOnce.Do(func() { close(c.closeCalled) })
+	return nil
+}
+
+func newDirectionalDispatcherConn() *directionalDispatcherConn {
+	return &directionalDispatcherConn{reads: make(chan dispatcherReadResult, 4), writes: make(chan []byte, 4), writeClosed: make(chan struct{}), closed: make(chan struct{})}
+}
+
+func (c *directionalDispatcherConn) Read(buffer []byte) (int, error) {
+	select {
+	case result := <-c.reads:
+		return copy(buffer, result.payload), result.err
+	case <-c.closed:
+		return 0, io.ErrClosedPipe
+	}
+}
+func (c *directionalDispatcherConn) Write(payload []byte) (int, error) {
+	c.writes <- append([]byte(nil), payload...)
+	return len(payload), nil
+}
+func (c *directionalDispatcherConn) CloseWrite() error {
+	c.writeOnce.Do(func() { close(c.writeClosed) })
+	return nil
+}
+func (c *directionalDispatcherConn) Close() error {
+	c.closeOnce.Do(func() { close(c.closed) })
+	return nil
+}
+
 func (c *streamConn) Read(p []byte) (int, error) {
 	if len(c.read) == 0 {
 		return 0, io.EOF
@@ -186,9 +266,239 @@
 	}
 }
 
+func TestStreamDispatcherHalfCloseKeepsTargetReadSideUntilResponseEOF(t *testing.T) {
+	conn := newDirectionalDispatcherConn()
+	sent := make(chan protocol.Frame, 4)
+	d := NewStreamDispatcherWithSender(Dialer{}, func(context.Context, string, string, int) (io.ReadWriteCloser, error) { return conn, nil }, func(frame protocol.Frame) error {
+		sent <- frame
+		return nil
+	})
+	defer d.Close()
+	payload, _ := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{Protocol: "tcp", TargetHost: "127.0.0.1", TargetPort: 22})
+	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 21, Payload: payload}); err != nil {
+		t.Fatal(err)
+	}
+	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: 21}); err != nil {
+		t.Fatal(err)
+	}
+	select {
+	case <-conn.writeClosed:
+	case <-time.After(time.Second):
+		t.Fatal("target CloseWrite was not called")
+	}
+	select {
+	case <-conn.closed:
+		t.Fatal("target was fully closed before its response")
+	default:
+	}
+	conn.reads <- dispatcherReadResult{payload: []byte("response")}
+	conn.reads <- dispatcherReadResult{err: io.EOF}
+	data := <-sent
+	if data.Type != protocol.FrameData || data.StreamID != 21 || string(data.Payload) != "response" {
+		t.Fatalf("target response frame = %+v", data)
+	}
+	halfClose := <-sent
+	if halfClose.Type != protocol.FrameHalfClose || halfClose.StreamID != 21 {
+		t.Fatalf("target EOF frame = %+v", halfClose)
+	}
+	select {
+	case <-conn.closed:
+	case <-time.After(time.Second):
+		t.Fatal("target was not closed after both directions half-closed")
+	}
+}
+
+func TestStreamDispatcherResetsHalfCloseWhenTargetLacksCloseWrite(t *testing.T) {
+	conn := &blockingStreamConn{streamConn: &streamConn{}, release: make(chan struct{})}
+	defer conn.Close()
+	sent := make(chan protocol.Frame, 2)
+	d := NewStreamDispatcherWithSender(Dialer{}, func(context.Context, string, string, int) (io.ReadWriteCloser, error) { return conn, nil }, func(frame protocol.Frame) error {
+		sent <- frame
+		return nil
+	})
+	defer d.Close()
+	payload, _ := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{Protocol: "tcp", TargetHost: "127.0.0.1", TargetPort: 22})
+	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 22, Payload: payload}); err != nil {
+		t.Fatal(err)
+	}
+	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: 22}); err != nil {
+		t.Fatalf("Handle(HALF_CLOSE) error = %v, want per-stream RESET", err)
+	}
+	frame := <-sent
+	if frame.Type != protocol.FrameReset || frame.StreamID != 22 || len(frame.Payload) == 0 || len(frame.Payload) > 128 {
+		t.Fatalf("unsupported half-close frame = %+v, want bounded RESET", frame)
+	}
+	if !conn.closed {
+		t.Fatal("unsupported half-close did not close target")
+	}
+}
+
+func TestStreamDispatcherMapsNonEOFReadErrorToBoundedReset(t *testing.T) {
+	conn := newDirectionalDispatcherConn()
+	sent := make(chan protocol.Frame, 2)
+	d := NewStreamDispatcherWithSender(Dialer{}, func(context.Context, string, string, int) (io.ReadWriteCloser, error) { return conn, nil }, func(frame protocol.Frame) error {
+		sent <- frame
+		return nil
+	})
+	defer d.Close()
+	payload, _ := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{Protocol: "tcp", TargetHost: "127.0.0.1", TargetPort: 22})
+	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 23, Payload: payload}); err != nil {
+		t.Fatal(err)
+	}
+	conn.reads <- dispatcherReadResult{err: errors.New("private target failure")}
+	frame := <-sent
+	if frame.Type != protocol.FrameReset || frame.StreamID != 23 || len(frame.Payload) == 0 || len(frame.Payload) > 128 {
+		t.Fatalf("target read error frame = %+v, want bounded RESET", frame)
+	}
+}
+
+func TestStreamDispatcherMapsTargetWriteErrorToBoundedReset(t *testing.T) {
+	conn := &writeErrorDispatcherConn{directionalDispatcherConn: newDirectionalDispatcherConn(), err: errors.New("target datagram too large")}
+	sent := make(chan protocol.Frame, 1)
+	d := NewStreamDispatcherWithSender(Dialer{}, func(context.Context, string, string, int) (io.ReadWriteCloser, error) { return conn, nil }, func(frame protocol.Frame) error {
+		sent <- frame
+		return nil
+	})
+	defer d.Close()
+	payload, _ := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{Protocol: "udp", TargetHost: "127.0.0.1", TargetPort: 53})
+	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 27, Payload: payload}); err != nil {
+		t.Fatal(err)
+	}
+	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 27, Payload: []byte("datagram")}); err != nil {
+		t.Fatalf("Handle(DATA) error = %v, want per-stream RESET", err)
+	}
+	select {
+	case frame := <-sent:
+		if frame.Type != protocol.FrameReset || frame.StreamID != 27 || len(frame.Payload) == 0 || len(frame.Payload) > 128 {
+			t.Fatalf("target write error frame = %+v, want bounded RESET", frame)
+		}
+	case <-time.After(time.Second):
+		t.Fatal("timed out waiting for target-write RESET")
+	}
+	select {
+	case <-conn.closed:
+	case <-time.After(time.Second):
+		t.Fatal("target write error did not close target")
+	}
+}
+
+func TestStreamDispatcherMapsTargetShortWriteToBoundedReset(t *testing.T) {
+	conn := &writeErrorDispatcherConn{directionalDispatcherConn: newDirectionalDispatcherConn(), written: 3}
+	sent := make(chan protocol.Frame, 1)
+	d := NewStreamDispatcherWithSender(Dialer{}, func(context.Context, string, string, int) (io.ReadWriteCloser, error) { return conn, nil }, func(frame protocol.Frame) error {
+		sent <- frame
+		return nil
+	})
+	defer d.Close()
+	payload, _ := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{Protocol: "tcp", TargetHost: "127.0.0.1", TargetPort: 22})
+	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 28, Payload: payload}); err != nil {
+		t.Fatal(err)
+	}
+	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 28, Payload: []byte("partial")}); err != nil {
+		t.Fatalf("Handle(DATA) error = %v, want per-stream RESET", err)
+	}
+	select {
+	case frame := <-sent:
+		if frame.Type != protocol.FrameReset || frame.StreamID != 28 {
+			t.Fatalf("target short-write frame = %+v, want RESET", frame)
+		}
+	case <-time.After(time.Second):
+		t.Fatal("timed out waiting for target-short-write RESET")
+	}
+}
+
+func TestStreamDispatcherRejectsDataAfterInboundHalfClose(t *testing.T) {
+	conn := newDirectionalDispatcherConn()
+	sent := make(chan protocol.Frame, 2)
+	d := NewStreamDispatcherWithSender(Dialer{}, func(context.Context, string, string, int) (io.ReadWriteCloser, error) { return conn, nil }, func(frame protocol.Frame) error {
+		sent <- frame
+		return nil
+	})
+	defer d.Close()
+	payload, _ := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{Protocol: "tcp", TargetHost: "127.0.0.1", TargetPort: 22})
+	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 24, Payload: payload}); err != nil {
+		t.Fatal(err)
+	}
+	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: 24}); err != nil {
+		t.Fatal(err)
+	}
+	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 24, Payload: []byte("after-half-close")}); err != nil {
+		t.Fatalf("Handle(DATA after HALF_CLOSE) error = %v, want per-stream RESET", err)
+	}
+	select {
+	case payload := <-conn.writes:
+		t.Fatalf("DATA after HALF_CLOSE reached target: %q", payload)
+	default:
+	}
+	select {
+	case frame := <-sent:
+		if frame.Type != protocol.FrameReset || frame.StreamID != 24 {
+			t.Fatalf("DATA-after-HALF_CLOSE response = %+v, want RESET", frame)
+		}
+	case <-time.After(time.Second):
+		t.Fatal("timed out waiting for DATA-after-HALF_CLOSE RESET")
+	}
+}
+
+func TestStreamDispatcherPreservesLargeUDPDatagramBoundary(t *testing.T) {
+	conn := newDirectionalDispatcherConn()
+	payload := bytes.Repeat([]byte("u"), 60<<10)
+	conn.reads <- dispatcherReadResult{payload: payload}
+	conn.reads <- dispatcherReadResult{err: io.EOF}
+	sent := make(chan protocol.Frame, 2)
+	d := NewStreamDispatcherWithSender(Dialer{}, func(context.Context, string, string, int) (io.ReadWriteCloser, error) { return conn, nil }, func(frame protocol.Frame) error {
+		sent <- frame
+		return nil
+	})
+	defer d.Close()
+	openPayload, _ := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{Protocol: "udp", TargetHost: "127.0.0.1", TargetPort: 53})
+	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 25, Payload: openPayload}); err != nil {
+		t.Fatal(err)
+	}
+	frame := <-sent
+	if frame.Type != protocol.FrameData || frame.StreamID != 25 || len(frame.Payload) != len(payload) {
+		t.Fatalf("UDP DATA type=%d id=%d bytes=%d, want one %d-byte frame", frame.Type, frame.StreamID, len(frame.Payload), len(payload))
+	}
+}
+
+func TestStreamDispatcherCloseWaitsForTargetReaderExit(t *testing.T) {
+	conn := newWaitableDispatcherConn()
+	d := NewStreamDispatcher(Dialer{}, func(context.Context, string, string, int) (io.ReadWriteCloser, error) { return conn, nil })
+	payload, _ := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{Protocol: "tcp", TargetHost: "127.0.0.1", TargetPort: 22})
+	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 26, Payload: payload}); err != nil {
+		t.Fatal(err)
+	}
+	select {
+	case <-conn.readStarted:
+	case <-time.After(time.Second):
+		t.Fatal("target reader did not start")
+	}
+	closed := make(chan error, 1)
+	go func() { closed <- d.Close() }()
+	select {
+	case <-conn.closeCalled:
+	case <-time.After(time.Second):
+		t.Fatal("dispatcher did not close target")
+	}
+	select {
+	case err := <-closed:
+		t.Fatalf("dispatcher Close returned before reader exit: %v", err)
+	default:
+	}
+	close(conn.allowExit)
+	select {
+	case err := <-closed:
+		if err != nil {
+			t.Fatal(err)
+		}
+	case <-time.After(time.Second):
+		t.Fatal("dispatcher Close did not finish after reader exit")
+	}
+}
+
 func TestStreamDispatcherRejectsDuplicateStreamID(t *testing.T) {
 	first := &blockingStreamConn{streamConn: &streamConn{}, release: make(chan struct{})}
-	defer close(first.release)
+	defer first.Close()
 	second := &streamConn{}
 	count := 0
 	d := NewStreamDispatcher(Dialer{}, func(context.Context, string, string, int) (io.ReadWriteCloser, error) {
@@ -220,6 +530,7 @@
 	d.streams[11] = oldEntry
 	d.generation++
 	d.streams[11] = &streamEntry{conn: newer, generation: d.generation}
+	d.readers.Add(1)
 	d.mu.Unlock()
 	d.readBack(11, oldEntry)
 	d.mu.Lock()
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/agent/websocket.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/agent/websocket.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/agent/websocket.go	2026-09-06 19:30:16
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/agent/websocket.go	2026-09-06 19:30:16
@@ -31,6 +31,15 @@
 	Rand              *rand.Rand
 }
 
+// SessionFrameHandler owns per-connection stream state. A new handler is
+// created after every successful dial and closed before reconnecting.
+type SessionFrameHandler interface {
+	Handle(protocol.Frame) error
+	Close() error
+}
+
+type SessionFrameHandlerFactory func(*Session) SessionFrameHandler
+
 // RunWebSocket dials the configured Server, authenticates with a bearer token,
 // reports metadata through the existing Session, and reconnects with bounded
 // exponential backoff until the context is cancelled.
@@ -39,6 +48,14 @@
 }
 
 func RunWebSocketWithOptions(ctx context.Context, serverURL, token, agentID, nodeID string, epoch int64, collector *MetadataCollector, onFrame func(protocol.Frame) error, options WebSocketRunOptions) error {
+	return runWebSocket(ctx, serverURL, token, agentID, nodeID, epoch, collector, onFrame, nil, options)
+}
+
+func RunWebSocketWithHandlerFactory(ctx context.Context, serverURL, token, agentID, nodeID string, epoch int64, collector *MetadataCollector, factory SessionFrameHandlerFactory, options WebSocketRunOptions) error {
+	return runWebSocket(ctx, serverURL, token, agentID, nodeID, epoch, collector, nil, factory, options)
+}
+
+func runWebSocket(ctx context.Context, serverURL, token, agentID, nodeID string, epoch int64, collector *MetadataCollector, onFrame func(protocol.Frame) error, factory SessionFrameHandlerFactory, options WebSocketRunOptions) error {
 	if strings.TrimSpace(serverURL) == "" {
 		return ErrAgentServerURLRequired
 	}
@@ -78,7 +95,18 @@
 			session.BaseBackoff, session.MaxBackoff, session.Rand = base, max, rng
 			session.HeartbeatInterval = heartbeat
 			session.SetMetadataIdentity(agentID, nodeID, currentEpoch)
-			err = session.Run(ctx, onFrame)
+			if factory == nil {
+				err = session.Run(ctx, onFrame)
+			} else {
+				handler := factory(session)
+				if handler == nil {
+					_ = session.Close()
+					err = ErrAgentSessionClosed
+				} else {
+					err = session.Run(ctx, handler.Handle)
+					err = errors.Join(err, handler.Close())
+				}
+			}
 			if ctx.Err() != nil {
 				return ctx.Err()
 			}
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/agent/websocket_test.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/agent/websocket_test.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/agent/websocket_test.go	1970-01-01 08:00:00
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/agent/websocket_test.go	2026-09-06 19:30:16
@@ -0,0 +1,76 @@
+package agent
+
+import (
+	"context"
+	"errors"
+	"net/http/httptest"
+	"strings"
+	"sync"
+	"testing"
+	"time"
+
+	"golang.org/x/net/websocket"
+
+	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
+)
+
+type lifecycleFrameHandler struct {
+	closed chan struct{}
+	once   sync.Once
+}
+
+func (*lifecycleFrameHandler) Handle(protocol.Frame) error { return nil }
+func (h *lifecycleFrameHandler) Close() error {
+	h.once.Do(func() { close(h.closed) })
+	return nil
+}
+
+func TestRunWebSocketCreatesAndClosesHandlerForEveryConnection(t *testing.T) {
+	server := httptest.NewServer(websocket.Handler(func(conn *websocket.Conn) {
+		var hello []byte
+		_ = websocket.Message.Receive(conn, &hello)
+		_ = conn.Close()
+	}))
+	defer server.Close()
+	ctx, cancel := context.WithCancel(context.Background())
+	defer cancel()
+	first := &lifecycleFrameHandler{closed: make(chan struct{})}
+	second := &lifecycleFrameHandler{closed: make(chan struct{})}
+	factoryCalls := 0
+	factoryErr := make(chan error, 1)
+	errCh := make(chan error, 1)
+	go func() {
+		errCh <- RunWebSocketWithHandlerFactory(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), "agent-token", "agent-handler", "node-handler", 1, NewMetadataCollector(nil), func(*Session) SessionFrameHandler {
+			factoryCalls++
+			switch factoryCalls {
+			case 1:
+				return first
+			case 2:
+				select {
+				case <-first.closed:
+				default:
+					factoryErr <- errors.New("first handler remained open when replacement connection started")
+				}
+				cancel()
+				return second
+			default:
+				return second
+			}
+		}, WebSocketRunOptions{BaseBackoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond, HeartbeatInterval: time.Hour})
+	}()
+	select {
+	case err := <-factoryErr:
+		t.Fatal(err)
+	case err := <-errCh:
+		if !errors.Is(err, context.Canceled) {
+			t.Fatalf("RunWebSocketWithHandlerFactory() error = %v, want context canceled", err)
+		}
+	case <-time.After(3 * time.Second):
+		t.Fatal("timed out waiting for handler reconnect lifecycle")
+	}
+	select {
+	case <-second.closed:
+	case <-time.After(time.Second):
+		t.Fatal("second handler was not closed when its session ended")
+	}
+}
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/cli/agent_runtime_test.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/cli/agent_runtime_test.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/cli/agent_runtime_test.go	1970-01-01 08:00:00
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/cli/agent_runtime_test.go	2026-09-06 19:30:16
@@ -0,0 +1,327 @@
+package cli
+
+import (
+	"bytes"
+	"context"
+	"errors"
+	"fmt"
+	"io"
+	"net"
+	"net/http"
+	"net/http/httptest"
+	"os"
+	"path/filepath"
+	"strconv"
+	"strings"
+	"testing"
+	"time"
+
+	"github.com/tunnelmesh/tunnelmesh/internal/auth"
+	"github.com/tunnelmesh/tunnelmesh/internal/client"
+	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
+	"github.com/tunnelmesh/tunnelmesh/internal/server"
+	"github.com/tunnelmesh/tunnelmesh/internal/storage"
+)
+
+func TestAgentRunCommandForwardsTCPUDPAndHTTPThroughRealDispatcher(t *testing.T) {
+	ctx := context.Background()
+	tcpListener, err := net.Listen("tcp", "127.0.0.1:0")
+	if err != nil {
+		t.Fatal(err)
+	}
+	defer tcpListener.Close()
+	tcpPort := tcpListener.Addr().(*net.TCPAddr).Port
+	udpTarget, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
+	if err != nil {
+		t.Fatal(err)
+	}
+	defer udpTarget.Close()
+	udpPort := udpTarget.LocalAddr().(*net.UDPAddr).Port
+	httpTarget := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
+		_, _ = io.WriteString(writer, "agent-http-response")
+	}))
+	defer httpTarget.Close()
+	httpHost, httpPortText, err := net.SplitHostPort(strings.TrimPrefix(httpTarget.URL, "http://"))
+	if err != nil {
+		t.Fatal(err)
+	}
+	httpPort, err := strconv.Atoi(httpPortText)
+	if err != nil {
+		t.Fatal(err)
+	}
+
+	db, err := storage.OpenSQLite(ctx, "file:agent-cli-runtime?mode=memory&cache=shared")
+	if err != nil {
+		t.Fatal(err)
+	}
+	defer db.Close()
+	authService := auth.NewAuthService(db)
+	owner, err := authService.CreateUser(ctx, "agent-cli-owner", "agent-cli-password", "user")
+	if err != nil {
+		t.Fatal(err)
+	}
+	if err := db.Agents().Create(ctx, storage.Agent{ID: "agent-cli", Name: "agent-cli", OwnerUserID: owner.ID, Enabled: true}); err != nil {
+		t.Fatal(err)
+	}
+	now := time.Now().UTC()
+	for i, policy := range []storage.AgentPolicy{
+		{ID: "agent-cli-tcp", AgentID: "agent-cli", TargetHost: "127.0.0.1", TargetPort: tcpPort, Protocol: "tcp", CreatedAt: now, UpdatedAt: now},
+		{ID: "agent-cli-udp", AgentID: "agent-cli", TargetHost: "127.0.0.1", TargetPort: udpPort, Protocol: "udp", CreatedAt: now, UpdatedAt: now},
+		{ID: "agent-cli-http", AgentID: "agent-cli", TargetHost: httpHost, TargetPort: httpPort, Protocol: "http", CreatedAt: now, UpdatedAt: now},
+	} {
+		if err := db.Policies().Create(ctx, policy); err != nil {
+			t.Fatalf("create policy %d: %v", i, err)
+		}
+	}
+	credentials := auth.NewCredentialService(db)
+	agentToken, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeAgent, OwnerUserID: owner.ID, AgentID: "agent-cli"})
+	if err != nil {
+		t.Fatal(err)
+	}
+	clientToken, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeClient, OwnerUserID: owner.ID, Scope: auth.TokenScope{AgentIDs: []string{"agent-cli"}, Protocols: []string{"tcp", "udp", "http"}, TargetPorts: []int{tcpPort, udpPort, httpPort}}})
+	if err != nil {
+		t.Fatal(err)
+	}
+	_ = credentials.Close()
+
+	runtime, err := server.NewServerRuntime(db, server.AgentSessionConfig{})
+	if err != nil {
+		t.Fatal(err)
+	}
+	defer runtime.Close()
+	runtimeListener, err := net.Listen("tcp", "127.0.0.1:0")
+	if err != nil {
+		t.Fatal(err)
+	}
+	runtimeCtx, stopRuntime := context.WithCancel(ctx)
+	defer stopRuntime()
+	runtimeDone := make(chan error, 1)
+	go func() { runtimeDone <- runtime.ServeListener(runtimeCtx, runtimeListener) }()
+
+	configPath := filepath.Join(t.TempDir(), "agent.yaml")
+	configBody := fmt.Sprintf("mode: local\nnode:\n  id: agent-cli-node\nagent:\n  server_url: ws://%s/ws/agent\n  id: agent-cli\n  token: %s\n", runtimeListener.Addr().String(), agentToken.Secret)
+	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
+		t.Fatal(err)
+	}
+	agentCtx, stopAgent := context.WithCancel(ctx)
+	agentDone := make(chan error, 1)
+	root := NewAgentRoot()
+	root.SetArgs([]string{"run", "--config", configPath})
+	root.SetOut(io.Discard)
+	root.SetErr(io.Discard)
+	go func() { agentDone <- root.ExecuteContext(agentCtx) }()
+	waitForCondition(t, 3*time.Second, func() bool {
+		_, registered := runtime.AgentSessions.Get("agent-cli")
+		return registered
+	}, "Agent CLI did not register a live session")
+
+	clientTransport, err := client.DialWebSocket(ctx, "ws://"+runtimeListener.Addr().String()+"/ws/client", clientToken.Secret)
+	if err != nil {
+		t.Fatal(err)
+	}
+	clientSession := client.NewSession(clientTransport)
+	defer clientSession.Close()
+
+	tcpResult := make(chan error, 1)
+	go serveAgentCLITCPTarget(tcpListener, tcpResult)
+	tcpStream, err := clientSession.OpenStreamConn(ctx, client.StreamRequest{AgentID: "agent-cli", Protocol: "tcp", TargetHost: "127.0.0.1", TargetPort: tcpPort})
+	if err != nil {
+		t.Fatal(err)
+	}
+	if _, err := tcpStream.Write([]byte("tcp-request")); err != nil {
+		t.Fatal(err)
+	}
+	if err := tcpStream.(interface{ CloseWrite() error }).CloseWrite(); err != nil {
+		t.Fatal(err)
+	}
+	if got := readExactWithTimeout(t, tcpStream, len("tcp-response")); string(got) != "tcp-response" {
+		t.Fatalf("TCP response = %q", got)
+	}
+	if err := waitError(t, tcpResult, "TCP target"); err != nil {
+		t.Fatal(err)
+	}
+	_ = tcpStream.Close()
+
+	// Keep the real socket test below the smallest common host UDP limit.
+	// The dispatcher unit test separately covers a 60 KiB logical datagram.
+	udpPayload := bytes.Repeat([]byte("u"), 8<<10)
+	udpResult := make(chan error, 1)
+	go serveAgentCLIUDPTarget(udpTarget, udpPayload, udpResult)
+	udpStream, err := clientSession.OpenDatagram(ctx, client.StreamRequest{AgentID: "agent-cli", Protocol: "udp", TargetHost: "127.0.0.1", TargetPort: udpPort})
+	if err != nil {
+		t.Fatal(err)
+	}
+	if err := udpStream.WriteDatagram(udpPayload); err != nil {
+		t.Fatal(err)
+	}
+	udpResponse := readDatagramWithTimeout(t, udpStream)
+	if !bytes.Equal(udpResponse, udpPayload) {
+		t.Fatalf("UDP response bytes = %d, want %d-byte datagram", len(udpResponse), len(udpPayload))
+	}
+	if err := waitError(t, udpResult, "UDP target"); err != nil {
+		t.Fatal(err)
+	}
+	_ = udpStream.Close()
+
+	httpStream, err := clientSession.OpenStreamConn(ctx, client.StreamRequest{AgentID: "agent-cli", Protocol: "http", TargetHost: httpHost, TargetPort: httpPort})
+	if err != nil {
+		t.Fatal(err)
+	}
+	request := "GET /through-agent HTTP/1.1\r\nHost: " + net.JoinHostPort(httpHost, httpPortText) + "\r\nConnection: close\r\n\r\n"
+	if _, err := httpStream.Write([]byte(request)); err != nil {
+		t.Fatal(err)
+	}
+	httpResponse := readAllWithTimeout(t, httpStream)
+	if !bytes.Contains(httpResponse, []byte("agent-http-response")) {
+		t.Fatalf("HTTP response = %q", httpResponse)
+	}
+	_ = httpStream.Close()
+
+	stopAgent()
+	if err := waitError(t, agentDone, "Agent command"); !errors.Is(err, context.Canceled) {
+		t.Fatalf("Agent command error = %v, want context canceled", err)
+	}
+	stopRuntime()
+	if err := waitError(t, runtimeDone, "Server runtime"); err != nil {
+		t.Fatal(err)
+	}
+}
+
+func serveAgentCLITCPTarget(listener net.Listener, result chan<- error) {
+	conn, err := listener.Accept()
+	if err != nil {
+		result <- err
+		return
+	}
+	defer conn.Close()
+	request := make([]byte, len("tcp-request"))
+	if _, err := io.ReadFull(conn, request); err != nil {
+		result <- err
+		return
+	}
+	if string(request) != "tcp-request" {
+		result <- fmt.Errorf("TCP request = %q", request)
+		return
+	}
+	one := make([]byte, 1)
+	if n, err := conn.Read(one); n != 0 || !errors.Is(err, io.EOF) {
+		result <- fmt.Errorf("TCP half-close read = (%d, %v), want EOF", n, err)
+		return
+	}
+	_, err = conn.Write([]byte("tcp-response"))
+	result <- err
+}
+
+func serveAgentCLIUDPTarget(conn *net.UDPConn, want []byte, result chan<- error) {
+	buffer := make([]byte, protocol.MaxPayload)
+	n, source, err := conn.ReadFromUDP(buffer)
+	if err != nil {
+		result <- err
+		return
+	}
+	if !bytes.Equal(buffer[:n], want) {
+		result <- fmt.Errorf("UDP target bytes = %d, want %d", n, len(want))
+		return
+	}
+	_, err = conn.WriteToUDP(buffer[:n], source)
+	result <- err
+}
+
+func readExactWithTimeout(t *testing.T, reader io.Reader, size int) []byte {
+	t.Helper()
+	result := make(chan struct {
+		payload []byte
+		err     error
+	}, 1)
+	go func() {
+		payload := make([]byte, size)
+		_, err := io.ReadFull(reader, payload)
+		result <- struct {
+			payload []byte
+			err     error
+		}{payload: payload, err: err}
+	}()
+	select {
+	case got := <-result:
+		if got.err != nil {
+			t.Fatal(got.err)
+		}
+		return got.payload
+	case <-time.After(3 * time.Second):
+		t.Fatal("timed out reading stream response")
+		return nil
+	}
+}
+
+func readDatagramWithTimeout(t *testing.T, stream client.DatagramStream) []byte {
+	t.Helper()
+	result := make(chan struct {
+		payload []byte
+		err     error
+	}, 1)
+	go func() {
+		payload, err := stream.ReadDatagram()
+		result <- struct {
+			payload []byte
+			err     error
+		}{payload: payload, err: err}
+	}()
+	select {
+	case got := <-result:
+		if got.err != nil {
+			t.Fatal(got.err)
+		}
+		return got.payload
+	case <-time.After(3 * time.Second):
+		t.Fatal("timed out reading UDP response")
+		return nil
+	}
+}
+
+func readAllWithTimeout(t *testing.T, reader io.Reader) []byte {
+	t.Helper()
+	result := make(chan struct {
+		payload []byte
+		err     error
+	}, 1)
+	go func() {
+		payload, err := io.ReadAll(reader)
+		result <- struct {
+			payload []byte
+			err     error
+		}{payload: payload, err: err}
+	}()
+	select {
+	case got := <-result:
+		if got.err != nil {
+			t.Fatal(got.err)
+		}
+		return got.payload
+	case <-time.After(3 * time.Second):
+		t.Fatal("timed out reading HTTP response")
+		return nil
+	}
+}
+
+func waitForCondition(t *testing.T, timeout time.Duration, condition func() bool, message string) {
+	t.Helper()
+	deadline := time.Now().Add(timeout)
+	for time.Now().Before(deadline) {
+		if condition() {
+			return
+		}
+		time.Sleep(5 * time.Millisecond)
+	}
+	t.Fatal(message)
+}
+
+func waitError(t *testing.T, result <-chan error, name string) error {
+	t.Helper()
+	select {
+	case err := <-result:
+		return err
+	case <-time.After(3 * time.Second):
+		t.Fatalf("timed out waiting for %s", name)
+		return nil
+	}
+}
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/cli/root.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/cli/root.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/cli/root.go	2026-09-06 18:38:01
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/cli/root.go	2026-09-06 19:30:16
@@ -173,7 +173,9 @@
 				nodeID = cfg.Agent.ID
 			}
 			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "agent connecting to %s in %s mode\n", cfg.Agent.ServerURL, cfg.Mode)
-			return agent.RunWebSocket(cmd.Context(), cfg.Agent.ServerURL, cfg.Agent.Token, cfg.Agent.ID, nodeID, 1, agent.NewMetadataCollector(cfg.Agent.Metadata), nil)
+			return agent.RunWebSocketWithHandlerFactory(cmd.Context(), cfg.Agent.ServerURL, cfg.Agent.Token, cfg.Agent.ID, nodeID, 1, agent.NewMetadataCollector(cfg.Agent.Metadata), func(session *agent.Session) agent.SessionFrameHandler {
+				return agent.NewStreamDispatcherWithSender(agent.Dialer{}, nil, session.Send)
+			}, agent.WebSocketRunOptions{})
 		}),
 		configCommand(opts, "register", "register this agent", func(cmd *cobra.Command, cfg config.Config) error {
 			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "agent registration requested for %s\n", effectiveAgentID(cfg))
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/server/agent_relay_transport.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/server/agent_relay_transport.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/server/agent_relay_transport.go	2026-09-06 18:38:01
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/server/agent_relay_transport.go	2026-09-06 19:30:16
@@ -4,30 +4,45 @@
 	"context"
 	"errors"
 	"io"
+	"math"
 	"sync"
-	"sync/atomic"
 
 	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
 	"github.com/tunnelmesh/tunnelmesh/internal/relay"
 )
 
-var errAgentRelayClosed = errors.New("server: local Agent relay closed")
+var (
+	errAgentRelayClosed           = errors.New("server: local Agent relay closed")
+	errAgentRelayWireIDsExhausted = errors.New("server: local Agent relay wire IDs exhausted")
+)
 
 // AgentRelayTransport multiplexes local relay streams over the currently
-// registered Agent WebSocket sessions. Agent wire IDs are process-wide and
-// independent from Client connection/stream IDs.
+// registered Agent WebSocket sessions. Agent wire IDs are monotonic and never
+// reused within one authenticated Agent epoch.
 type AgentRelayTransport struct {
-	manager *AgentSessionManager
-	nextID  atomic.Uint32
-	mu      sync.Mutex
-	streams map[uint32]*agentRelayStream
-	closed  bool
+	manager    *AgentSessionManager
+	mu         sync.Mutex
+	streams    map[agentRelayStreamKey]*agentRelayStream
+	allocators map[agentRelayGeneration]*agentRelayIDAllocator
+	closed     bool
 }
 
 func NewAgentRelayTransport(manager *AgentSessionManager) *AgentRelayTransport {
-	return &AgentRelayTransport{manager: manager, streams: make(map[uint32]*agentRelayStream)}
+	return &AgentRelayTransport{manager: manager, streams: make(map[agentRelayStreamKey]*agentRelayStream), allocators: make(map[agentRelayGeneration]*agentRelayIDAllocator)}
 }
 
+type agentRelayGeneration struct {
+	agentID string
+	epoch   int64
+}
+
+type agentRelayStreamKey struct {
+	agentRelayGeneration
+	wireID uint32
+}
+
+type agentRelayIDAllocator struct{ next uint64 }
+
 func (t *AgentRelayTransport) OpenStream(ctx context.Context, request relay.StreamRequest) (io.ReadWriteCloser, error) {
 	if t == nil || t.manager == nil || request.AgentID == "" || request.TargetHost == "" || request.TargetPort < 1 || request.TargetPort > 65535 {
 		return nil, relay.ErrNodeDisconnected
@@ -46,21 +61,24 @@
 	if err != nil {
 		return nil, err
 	}
-	stream := &agentRelayStream{transport: t, agentID: request.AgentID, epoch: session.Epoch, readCh: make(chan []byte, 16), done: make(chan struct{})}
 	t.mu.Lock()
 	if t.closed {
 		t.mu.Unlock()
 		return nil, errAgentRelayClosed
 	}
-	for {
-		stream.wireID = t.nextID.Add(1)
-		if stream.wireID != 0 {
-			if _, exists := t.streams[stream.wireID]; !exists {
-				break
-			}
-		}
+	generation := agentRelayGeneration{agentID: request.AgentID, epoch: session.Epoch}
+	allocator := t.allocators[generation]
+	if allocator == nil {
+		allocator = &agentRelayIDAllocator{}
+		t.allocators[generation] = allocator
 	}
-	t.streams[stream.wireID] = stream
+	if allocator.next >= math.MaxUint32 {
+		t.mu.Unlock()
+		return nil, errAgentRelayWireIDsExhausted
+	}
+	allocator.next++
+	stream := &agentRelayStream{transport: t, agentID: request.AgentID, epoch: session.Epoch, wireID: uint32(allocator.next), readCh: make(chan []byte, 16), done: make(chan struct{})}
+	t.streams[stream.key()] = stream
 	t.mu.Unlock()
 	if err := t.manager.SendGeneration(stream.agentID, stream.epoch, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: stream.wireID, Payload: payload}); err != nil {
 		t.detach(stream)
@@ -95,34 +113,35 @@
 		t.manager.mu.RUnlock()
 		return nil
 	}
+	key := agentRelayStreamKey{agentRelayGeneration: agentRelayGeneration{agentID: agentID, epoch: epoch}, wireID: frame.StreamID}
 	t.mu.Lock()
-	stream := t.streams[frame.StreamID]
+	stream := t.streams[key]
 	if stream == nil || stream.agentID != agentID || stream.epoch != epoch {
 		t.mu.Unlock()
 		session.mu.RUnlock()
 		t.manager.mu.RUnlock()
 		return nil
 	}
-	var resetBackpressure bool
+	var resetStream bool
 	switch frame.Type {
 	case protocol.FrameData:
-		if !stream.push(frame.Payload) {
-			delete(t.streams, frame.StreamID)
-			stream.fail(relay.ErrBackpressure)
-			resetBackpressure = true
+		if enqueueErr := stream.enqueue(frame.Payload); enqueueErr != nil {
+			delete(t.streams, key)
+			stream.fail(enqueueErr)
+			resetStream = true
 		}
 	case protocol.FrameHalfClose:
 		if stream.remoteHalfClose() {
-			delete(t.streams, frame.StreamID)
+			delete(t.streams, key)
 		}
 	case protocol.FrameReset:
-		delete(t.streams, frame.StreamID)
+		delete(t.streams, key)
 		stream.fail(protocol.ErrStreamReset)
 	}
 	t.mu.Unlock()
 	session.mu.RUnlock()
 	t.manager.mu.RUnlock()
-	if resetBackpressure {
+	if resetStream {
 		_ = t.manager.SendGeneration(agentID, epoch, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: frame.StreamID})
 	}
 	return nil
@@ -134,12 +153,14 @@
 	}
 	t.mu.Lock()
 	var failed []*agentRelayStream
-	for id, stream := range t.streams {
+	generation := agentRelayGeneration{agentID: agentID, epoch: epoch}
+	for key, stream := range t.streams {
 		if stream.agentID == agentID && stream.epoch == epoch {
-			delete(t.streams, id)
+			delete(t.streams, key)
 			failed = append(failed, stream)
 		}
 	}
+	delete(t.allocators, generation)
 	t.mu.Unlock()
 	for _, stream := range failed {
 		stream.fail(relay.ErrNodeDisconnected)
@@ -157,10 +178,11 @@
 	}
 	t.closed = true
 	streams := make([]*agentRelayStream, 0, len(t.streams))
-	for id, stream := range t.streams {
-		delete(t.streams, id)
+	for key, stream := range t.streams {
+		delete(t.streams, key)
 		streams = append(streams, stream)
 	}
+	clear(t.allocators)
 	t.mu.Unlock()
 	for _, stream := range streams {
 		stream.fail(errAgentRelayClosed)
@@ -173,8 +195,8 @@
 		return
 	}
 	t.mu.Lock()
-	if t.streams[stream.wireID] == stream {
-		delete(t.streams, stream.wireID)
+	if t.streams[stream.key()] == stream {
+		delete(t.streams, stream.key())
 	}
 	t.mu.Unlock()
 }
@@ -195,6 +217,10 @@
 	remoteHalf bool
 }
 
+func (s *agentRelayStream) key() agentRelayStreamKey {
+	return agentRelayStreamKey{agentRelayGeneration: agentRelayGeneration{agentID: s.agentID, epoch: s.epoch}, wireID: s.wireID}
+}
+
 func (s *agentRelayStream) Read(buffer []byte) (int, error) {
 	if len(buffer) == 0 {
 		return 0, nil
@@ -297,14 +323,20 @@
 	return nil
 }
 
-func (s *agentRelayStream) push(payload []byte) bool {
+func (s *agentRelayStream) enqueue(payload []byte) error {
+	s.mu.Lock()
+	defer s.mu.Unlock()
+	if s.remoteHalf {
+		return protocol.ErrInvalidFrame
+	}
+	if s.err != nil {
+		return s.err
+	}
 	select {
 	case s.readCh <- append([]byte(nil), payload...):
-		return true
-	case <-s.done:
-		return false
+		return nil
 	default:
-		return false
+		return relay.ErrBackpressure
 	}
 }
 
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/server/agent_relay_transport_test.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/server/agent_relay_transport_test.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/server/agent_relay_transport_test.go	2026-09-06 18:38:01
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/server/agent_relay_transport_test.go	2026-09-06 19:30:16
@@ -4,6 +4,7 @@
 	"context"
 	"errors"
 	"io"
+	"math"
 	"testing"
 	"time"
 
@@ -139,6 +140,71 @@
 	case frame := <-agentTransport.sent:
 		t.Fatalf("Close() echoed terminal Agent frame: %+v", frame)
 	case <-time.After(50 * time.Millisecond):
+	}
+}
+
+func TestAgentRelayTransportRejectsWireIDWrapAndResetsForNewEpoch(t *testing.T) {
+	manager := NewAgentSessionManager(AgentSessionConfig{})
+	firstTransport := newFakeTransport()
+	if _, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-exhaust", NodeID: "node-old", Epoch: 1}, firstTransport); err != nil {
+		t.Fatal(err)
+	}
+	mux := NewAgentRelayTransport(manager)
+	defer mux.Close()
+	mux.allocators[agentRelayGeneration{agentID: "agent-exhaust", epoch: 1}] = &agentRelayIDAllocator{next: math.MaxUint32 - 1}
+	last, err := mux.OpenStream(context.Background(), relay.StreamRequest{AgentID: "agent-exhaust", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22})
+	if err != nil {
+		t.Fatal(err)
+	}
+	if frame := receiveAgentRelayFrame(t, firstTransport); frame.StreamID != math.MaxUint32 {
+		t.Fatalf("last wire ID = %d, want MaxUint32", frame.StreamID)
+	}
+	_ = last.Close()
+	_ = receiveAgentRelayFrame(t, firstTransport)
+	if stream, err := mux.OpenStream(context.Background(), relay.StreamRequest{AgentID: "agent-exhaust", Protocol: "tcp", TargetHost: "10.0.0.9", TargetPort: 22}); err == nil {
+		_ = stream.Close()
+		t.Fatal("OpenStream reused a retired wire ID after MaxUint32")
+	}
+
+	secondTransport := newFakeTransport()
+	if _, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-exhaust", NodeID: "node-new", Epoch: 2}, secondTransport); err != nil {
+		t.Fatal(err)
+	}
+	mux.FailAgentGeneration("agent-exhaust", 1)
+	stream, err := mux.OpenStream(context.Background(), relay.StreamRequest{AgentID: "agent-exhaust", Protocol: "tcp", TargetHost: "10.0.0.10", TargetPort: 22})
+	if err != nil {
+		t.Fatal(err)
+	}
+	if frame := receiveAgentRelayFrame(t, secondTransport); frame.StreamID != 1 {
+		t.Fatalf("new epoch wire ID = %d, want 1", frame.StreamID)
+	}
+	_ = stream.Close()
+}
+
+func TestAgentRelayTransportRejectsDataAfterRemoteHalfClose(t *testing.T) {
+	manager := NewAgentSessionManager(AgentSessionConfig{})
+	agentTransport := newFakeTransport()
+	if _, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-half-data", NodeID: "node-half-data", Epoch: 1}, agentTransport); err != nil {
+		t.Fatal(err)
+	}
+	mux := NewAgentRelayTransport(manager)
+	defer mux.Close()
+	stream, err := mux.OpenStream(context.Background(), relay.StreamRequest{AgentID: "agent-half-data", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22})
+	if err != nil {
+		t.Fatal(err)
+	}
+	open := receiveAgentRelayFrame(t, agentTransport)
+	if err := mux.HandleAgentFrame("agent-half-data", 1, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: open.StreamID}); err != nil {
+		t.Fatal(err)
+	}
+	if err := mux.HandleAgentFrame("agent-half-data", 1, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: open.StreamID, Payload: []byte("after-eof")}); err != nil {
+		t.Fatal(err)
+	}
+	if _, err := stream.Write([]byte("must-not-send")); !errors.Is(err, protocol.ErrInvalidFrame) {
+		t.Fatalf("Write() after DATA following HALF_CLOSE error = %v, want ErrInvalidFrame", err)
+	}
+	if frame := receiveAgentRelayFrame(t, agentTransport); frame.Type != protocol.FrameReset || frame.StreamID != open.StreamID {
+		t.Fatalf("Agent protocol-error response = %+v, want RESET", frame)
 	}
 }
 
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/server/ws_client.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/server/ws_client.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/server/ws_client.go	2026-09-06 18:38:01
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/server/ws_client.go	2026-09-06 19:30:16
@@ -11,7 +11,11 @@
 	"github.com/tunnelmesh/tunnelmesh/internal/relay"
 )
 
-const clientStreamResetMessage = "stream rejected"
+const (
+	clientStreamResetMessage = "stream rejected"
+	clientOpenLimitMessage   = "open limit reached"
+	maxClientOpenAttempts    = 1 << 16
+)
 
 type ClientWSHandler struct{ Sessions *ClientSessionManager }
 
@@ -38,9 +42,16 @@
 // ServeClientSession multiplexes one authenticated Client connection onto the
 // existing relay transport. Authorization is re-evaluated for every OPEN.
 func ServeClientSession(ctx context.Context, principal ClientSessionPrincipal, tr clientReceiveTransport, authorizer StreamAuthorizer, opener relay.NodeTransport) error {
+	return serveClientSessionWithOpenLimit(ctx, principal, tr, authorizer, opener, maxClientOpenAttempts)
+}
+
+func serveClientSessionWithOpenLimit(ctx context.Context, principal ClientSessionPrincipal, tr clientReceiveTransport, authorizer StreamAuthorizer, opener relay.NodeTransport, openLimit int) error {
 	if tr == nil || principal.ConnectionID == "" {
 		return ErrSessionClosed
 	}
+	if openLimit <= 0 {
+		openLimit = maxClientOpenAttempts
+	}
 	if ctx == nil {
 		ctx = context.Background()
 	}
@@ -101,7 +112,13 @@
 		case protocol.FrameOpenStream:
 			mu.Lock()
 			_, duplicate := seen[frame.StreamID]
-			seen[frame.StreamID] = struct{}{}
+			if !duplicate && len(seen) >= openLimit {
+				mu.Unlock()
+				return tr.Send(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameGoAway, Payload: []byte(clientOpenLimitMessage)})
+			}
+			if !duplicate {
+				seen[frame.StreamID] = struct{}{}
+			}
 			mu.Unlock()
 			if duplicate {
 				_ = reset(frame.StreamID)
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/server/ws_client_test.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/server/ws_client_test.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/server/ws_client_test.go	2026-09-06 18:38:01
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/server/ws_client_test.go	2026-09-06 19:30:16
@@ -455,6 +455,34 @@
 	}
 }
 
+func TestServeClientSessionCapsUniqueOpenAttemptsBeforePayloadDecode(t *testing.T) {
+	transport := newScriptedClientTransport(
+		protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 1, Payload: []byte("{")},
+		protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 1, Payload: []byte("{")},
+		protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 2, Payload: []byte("{")},
+		protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 3, Payload: []byte("{")},
+	)
+	err := serveClientSessionWithOpenLimit(context.Background(), ClientSessionPrincipal{ConnectionID: "connection-open-limit", Identity: auth.TokenIdentity{TokenID: "token", Type: storage.TokenTypeClient}}, transport, streamAuthorizerFunc(func(context.Context, ClientSessionPrincipal, protocol.StreamOpenPayload) error {
+		t.Fatal("malformed OPEN reached authorizer")
+		return nil
+	}), &recordingNodeTransport{opened: make(chan relay.StreamRequest, 1)}, 2)
+	if err != nil {
+		t.Fatal(err)
+	}
+	if len(transport.sent) != 4 {
+		t.Fatalf("sent frame count = %d, want three RESETs and one GOAWAY", len(transport.sent))
+	}
+	for i, frame := range transport.sent[:3] {
+		if frame.Type != protocol.FrameReset {
+			t.Fatalf("frame %d = %+v, want RESET", i, frame)
+		}
+	}
+	terminal := transport.sent[3]
+	if terminal.Type != protocol.FrameGoAway || terminal.StreamID != 0 || len(terminal.Payload) == 0 || len(terminal.Payload) > 128 {
+		t.Fatalf("terminal frame = %+v, want bounded GOAWAY", terminal)
+	}
+}
+
 type streamAuthorizerFunc func(context.Context, ClientSessionPrincipal, protocol.StreamOpenPayload) error
 
 func (f streamAuthorizerFunc) Authorize(ctx context.Context, principal ClientSessionPrincipal, request protocol.StreamOpenPayload) error {
```
