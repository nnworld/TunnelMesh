package client

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

// StreamOpener is the client-side boundary to a server logical stream.
// Implementations may use a WebSocket frame multiplexer, a relay, or a fake
// transport in tests.
type StreamOpener interface {
	OpenStream(context.Context, StreamRequest) (io.ReadWriteCloser, error)
}

type ResultStreamOpener interface {
	StreamOpener
	OpenStreamResult(context.Context, StreamRequest) (io.ReadWriteCloser, protocol.OpenResultPayload, error)
}

type TCPForwardConfig struct {
	ListenAddr     string
	AgentID        string
	TargetHost     string
	TargetPort     int
	ConnectTimeout time.Duration
}

type TCPForward struct {
	opener StreamOpener
	cfg    TCPForwardConfig
	ln     *TCPListener
	mu     sync.Mutex
	closed bool
	active map[net.Conn]struct{}
}

func NewTCPForward(opener StreamOpener, cfg TCPForwardConfig) (*TCPForward, error) {
	if opener == nil {
		return nil, errors.New("client: nil stream opener")
	}
	if strings.TrimSpace(cfg.ListenAddr) == "" {
		return nil, errors.New("client: listen address required")
	}
	if strings.TrimSpace(cfg.TargetHost) == "" || cfg.TargetPort < 1 || cfg.TargetPort > 65535 {
		return nil, errors.New("client: target host and port required")
	}
	return &TCPForward{opener: opener, cfg: cfg, active: make(map[net.Conn]struct{})}, nil
}

func (f *TCPForward) Start(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return ErrListenerClosed
	}
	if f.ln != nil {
		return nil
	}
	ln, err := ListenTCP(ctx, f.cfg.ListenAddr, f.handleConn)
	if err != nil {
		return err
	}
	f.ln = ln
	go func() { _ = ln.Serve() }()
	return nil
}
func (f *TCPForward) Addr() net.Addr {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ln == nil {
		return nil
	}
	return f.ln.Addr()
}
func (f *TCPForward) Close() error {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return nil
	}
	f.closed = true
	ln := f.ln
	f.mu.Unlock()
	if ln != nil {
		err := ln.Close()
		f.mu.Lock()
		active := make([]net.Conn, 0, len(f.active))
		for conn := range f.active {
			active = append(active, conn)
		}
		f.mu.Unlock()
		for _, conn := range active {
			_ = conn.Close()
		}
		return err
	}
	return nil
}
func (f *TCPForward) handleConn(local net.Conn) {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		_ = local.Close()
		return
	}
	f.active[local] = struct{}{}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		delete(f.active, local)
		f.mu.Unlock()
	}()
	defer local.Close()
	ctx := context.Background()
	var cancel context.CancelFunc
	if f.cfg.ConnectTimeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, f.cfg.ConnectTimeout)
		defer cancel()
	}
	remote, err := f.opener.OpenStream(ctx, StreamRequest{AgentID: f.cfg.AgentID, Protocol: "tcp", TargetHost: f.cfg.TargetHost, TargetPort: f.cfg.TargetPort})
	if err != nil {
		return
	}
	defer remote.Close()
	bridge(local, remote)
}

type HTTPForwardConfig struct {
	ListenAddr string
	AgentID    string
	TargetHost string
	TargetPort int
}

type UDPForwardConfig struct {
	ListenAddr  string
	AgentID     string
	TargetHost  string
	TargetPort  int
	IdleTimeout time.Duration
	MaxDatagram int
}

type UDPForward struct {
	manager *UDPAssociationManager
	cfg     UDPForwardConfig
	conn    *net.UDPConn
	mu      sync.Mutex
	closed  bool
}

func NewUDPForward(opener StreamOpener, cfg UDPForwardConfig) (*UDPForward, error) {
	if strings.TrimSpace(cfg.ListenAddr) == "" {
		return nil, errors.New("client: listen address required")
	}
	m, err := NewUDPAssociationManager(opener, UDPAssociationConfig{AgentID: cfg.AgentID, TargetHost: cfg.TargetHost, TargetPort: cfg.TargetPort, IdleTimeout: cfg.IdleTimeout, MaxDatagram: cfg.MaxDatagram})
	if err != nil {
		return nil, err
	}
	return &UDPForward{manager: m, cfg: cfg}, nil
}
func (f *UDPForward) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return ErrListenerClosed
	}
	if f.conn != nil {
		return nil
	}
	addr, err := net.ResolveUDPAddr("udp", f.cfg.ListenAddr)
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return err
	}
	f.conn = conn
	f.manager.deliver = func(dst *net.UDPAddr, payload []byte) { _, _ = conn.WriteToUDP(payload, dst) }
	go f.readLoop(ctx)
	return nil
}
func (f *UDPForward) readLoop(ctx context.Context) {
	buf := make([]byte, f.manager.cfg.MaxDatagram)
	interval := f.manager.cfg.IdleTimeout / 2
	if interval <= 0 {
		interval = time.Nanosecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = f.Close()
			return
		case <-ticker.C:
			f.manager.Expire(time.Now())
		default:
		}
		f.mu.Lock()
		conn := f.conn
		closed := f.closed
		f.mu.Unlock()
		if closed || conn == nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return
		}
		if err := f.manager.HandleDatagram(ctx, src, buf[:n]); err != nil {
			continue
		}
	}
}
func (f *UDPForward) Addr() net.Addr {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.conn == nil {
		return nil
	}
	return f.conn.LocalAddr()
}
func (f *UDPForward) Close() error {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return nil
	}
	f.closed = true
	conn := f.conn
	f.conn = nil
	f.mu.Unlock()
	_ = f.manager.Close()
	if conn != nil {
		return conn.Close()
	}
	return nil
}

type HTTPForward struct {
	opener StreamOpener
	cfg    HTTPForwardConfig
	server *http.Server
	ln     net.Listener
	mu     sync.Mutex
}

func NewHTTPForward(opener StreamOpener, cfg HTTPForwardConfig) (*HTTPForward, error) {
	if opener == nil {
		return nil, errors.New("client: nil stream opener")
	}
	if strings.TrimSpace(cfg.ListenAddr) == "" || strings.TrimSpace(cfg.TargetHost) == "" || cfg.TargetPort < 1 || cfg.TargetPort > 65535 {
		return nil, errors.New("client: invalid HTTP forward config")
	}
	return &HTTPForward{opener: opener, cfg: cfg}, nil
}
func (f *HTTPForward) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.server != nil {
		return nil
	}
	ln, err := net.Listen("tcp", f.cfg.ListenAddr)
	if err != nil {
		return err
	}
	f.ln = ln
	server := &http.Server{Handler: http.HandlerFunc(f.handleHTTP)}
	f.server = server
	go func() {
		_ = server.Serve(ln)
	}()
	if ctx != nil {
		go func() {
			<-ctx.Done()
			_ = f.Close()
		}()
	}
	return nil
}
func (f *HTTPForward) Addr() net.Addr {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ln == nil {
		return nil
	}
	return f.ln.Addr()
}
func (f *HTTPForward) Close() error {
	f.mu.Lock()
	srv := f.server
	ln := f.ln
	f.server, f.ln = nil, nil
	f.mu.Unlock()
	if srv != nil {
		_ = srv.Close()
	}
	if ln != nil {
		return ln.Close()
	}
	return nil
}
func (f *HTTPForward) handleHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	stream, err := f.opener.OpenStream(ctx, StreamRequest{AgentID: f.cfg.AgentID, Protocol: "http", TargetHost: f.cfg.TargetHost, TargetPort: f.cfg.TargetPort})
	if err != nil {
		http.Error(w, "tunnel unavailable", http.StatusBadGateway)
		return
	}
	forwardHTTP(w, r, stream)
}

// forwardHTTP writes one HTTP request to a logical stream and copies the
// response back to the local client. It is shared by fixed-target forwards and
// the standard HTTP proxy so upgrade and error behavior cannot drift.
func forwardHTTP(w http.ResponseWriter, r *http.Request, stream io.ReadWriteCloser) {
	defer stream.Close()
	// The local request is serialized directly so upgrades and arbitrary HTTP
	// headers remain transparent to the remote HTTP service.
	if err := r.Write(stream); err != nil {
		http.Error(w, "tunnel write failed", http.StatusBadGateway)
		return
	}
	br := bufio.NewReader(stream)
	resp, err := http.ReadResponse(br, r)
	if err != nil {
		http.Error(w, "tunnel read failed", http.StatusBadGateway)
		return
	}
	if resp.StatusCode == http.StatusSwitchingProtocols {
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "upgrade unsupported", http.StatusHTTPVersionNotSupported)
			return
		}
		conn, rw, err := hj.Hijack()
		if err != nil {
			return
		}
		if err := resp.Write(rw); err != nil {
			_ = conn.Close()
			return
		}
		_ = rw.Flush()
		if n := br.Buffered(); n > 0 {
			buffered, readErr := br.Peek(n)
			if readErr == nil {
				if _, writeErr := rw.Write(buffered); writeErr != nil {
					_ = conn.Close()
					return
				}
				_, _ = br.Discard(n)
				_ = rw.Flush()
			}
		}
		remote := &bufferedStream{Reader: br, Writer: stream, closer: stream}
		_ = bridge(conn, remote)
		return
	}
	defer resp.Body.Close()
	for k, values := range resp.Header {
		for _, v := range values {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// bridge uses independent bounded buffers so a slow upload does not prevent
// the remote response from being drained. Closing both endpoints after one
// side reaches EOF provides deterministic cleanup for SSH/raw TCP clients.
func bridge(a, b io.ReadWriteCloser) error {
	results := make(chan copyResult, 2)
	copyOne := func(dst io.Writer, src io.Reader, halfClose io.Writer) {
		buf := make([]byte, 32<<10)
		_, err := io.CopyBuffer(dst, src, buf)
		halfClosed := false
		if c, ok := halfClose.(interface{ CloseWrite() error }); ok {
			halfClosed = c.CloseWrite() == nil
		}
		results <- copyResult{err: err, halfClosed: halfClosed}
	}
	go copyOne(a, b, a)
	go copyOne(b, a, b)
	first := <-results
	if first.err != nil && !errors.Is(first.err, io.EOF) {
		_ = a.Close()
		_ = b.Close()
		select {
		case <-results:
		case <-time.After(time.Second):
		}
		return first.err
	}
	if !first.halfClosed {
		_ = a.Close()
		_ = b.Close()
		select {
		case <-results:
		case <-time.After(time.Second):
		}
		return first.err
	}
	var second copyResult
	select {
	case second = <-results:
	case <-time.After(time.Second):
		_ = a.Close()
		_ = b.Close()
		return first.err
	}
	_ = a.Close()
	_ = b.Close()
	if errors.Is(first.err, io.EOF) {
		return second.err
	}
	return first.err
}

type copyResult struct {
	err        error
	halfClosed bool
}

type bufferedStream struct {
	io.Reader
	io.Writer
	closer io.Closer
}

func (s *bufferedStream) Close() error { return s.closer.Close() }

type RetryConfig struct {
	MaxAttempts int
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
}

type RetryingOpener struct {
	base StreamOpener
	cfg  RetryConfig
}

func NewRetryingOpener(base StreamOpener, cfg RetryConfig) *RetryingOpener {
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 3
	}
	if cfg.BaseBackoff <= 0 {
		cfg.BaseBackoff = 100 * time.Millisecond
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = 5 * time.Second
	}
	return &RetryingOpener{base: base, cfg: cfg}
}
func (r *RetryingOpener) OpenStream(ctx context.Context, req StreamRequest) (io.ReadWriteCloser, error) {
	if r == nil || r.base == nil {
		return nil, errors.New("client: nil retry opener")
	}
	var err error
	for attempt := 0; attempt < r.cfg.MaxAttempts; attempt++ {
		var stream io.ReadWriteCloser
		stream, err = r.base.OpenStream(ctx, req)
		if err == nil {
			return stream, nil
		}
		if attempt+1 == r.cfg.MaxAttempts {
			break
		}
		delay := r.cfg.BaseBackoff
		for i := 0; i < attempt && delay < r.cfg.MaxBackoff; i++ {
			delay *= 2
		}
		if delay > r.cfg.MaxBackoff {
			delay = r.cfg.MaxBackoff
		}
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-t.C:
		}
	}
	return nil, fmt.Errorf("client: open stream after %d attempts: %w", r.cfg.MaxAttempts, err)
}
