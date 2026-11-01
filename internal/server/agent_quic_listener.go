package server

import (
	"context"
	"time"

	"github.com/quic-go/quic-go"
)

// QUIC settings for agent connections. The idle timeout is above the keepalive so a
// healthy agent never looks idle, and a dead one is dropped within a minute.
const (
	quicAgentIdleTimeout = 60 * time.Second
	quicAgentKeepAlive   = 20 * time.Second
	quicAgentMaxStreams  = 1
)

// quicAgentConfig is the server's QUIC transport policy. One bidirectional stream per
// connection carries the whole agent protocol, so a peer cannot open a second stream to
// multiply its share of the pre-authentication budget.
func quicAgentConfig() *quic.Config {
	return &quic.Config{
		MaxIncomingStreams: quicAgentMaxStreams,
		MaxIdleTimeout:     quicAgentIdleTimeout,
		KeepAlivePeriod:    quicAgentKeepAlive,
	}
}

// quicConnCloser adapts a QUIC connection to io.Closer for the listener's tracking.
type quicConnCloser struct{ conn *quic.Conn }

func (c quicConnCloser) Close() error { return c.conn.CloseWithError(0, "server closed") }

// serveQUIC accepts QUIC connections on ln. The QUIC handshake, including the agent
// ALPN check, completes inside Accept, so a foreign client never reaches this loop.
func (l *agentStreamListener) serveQUIC(ctx context.Context, ln *quic.Listener) error {
	if !l.addCloser(ln) {
		_ = ln.Close()
		return nil
	}
	for {
		conn, err := ln.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil || l.isClosed() {
				return nil
			}
			return err
		}
		ip := remoteIPOf(conn.RemoteAddr())
		release, ok := l.budget.acquire(ip)
		if !ok {
			_ = conn.CloseWithError(0, "busy")
			l.observe("rejected", "budget")
			continue
		}
		closer := quicConnCloser{conn: conn}
		if !l.track(closer) {
			release()
			_ = conn.CloseWithError(0, "server closed")
			continue
		}
		l.wg.Add(1)
		go func() {
			defer l.wg.Done()
			defer l.untrack(closer)
			defer release()
			defer conn.CloseWithError(0, "agent session ended")
			l.handleQUICConn(ctx, conn, ip, release)
		}()
	}
}

// handleQUICConn waits for the one stream that carries the preface and protocol. The
// wait is bounded by the preface timeout, so a connection that never opens a stream is
// dropped on the same budget as a slow TCP preface.
func (l *agentStreamListener) handleQUICConn(ctx context.Context, conn *quic.Conn, ip string, release func()) {
	streamCtx, cancel := context.WithTimeout(ctx, l.limits.PrefaceTimeout)
	stream, err := conn.AcceptStream(streamCtx)
	cancel()
	if err != nil {
		l.budget.recordFailure(ip)
		l.observe("failed", "no_stream")
		return
	}
	// Closing the agent stream closes the connection too: the protocol uses no other
	// stream, so nothing else would release it.
	l.handleStream(ctx, quicAgentStream{Stream: stream, conn: conn}, ip, release)
}

// quicAgentStream is the agent side of one QUIC connection. Closing it closes the whole
// connection, because the agent protocol uses no other stream.
type quicAgentStream struct {
	*quic.Stream
	conn *quic.Conn
}

func (s quicAgentStream) Close() error {
	_ = s.Stream.Close()
	return s.conn.CloseWithError(0, "agent session closed")
}
