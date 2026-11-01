package server

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"

	"github.com/quic-go/quic-go"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
)

// agentStreamBinding pairs a raw listener with the socket it was bound to. serve runs
// the accept loop and closer releases the socket; TCP and QUIC differ only here.
type agentStreamBinding struct {
	listener *agentStreamListener
	serve    func(ctx context.Context) error
	closer   io.Closer
}

// bindAgentStreams binds every configured raw listener. An empty address leaves that
// transport off, so a deployment that does not set agent_transports changes nothing.
func (r *ServerRuntime) bindAgentStreams(cfg RuntimeConfig) error {
	transports := cfg.AgentTransports
	limits := agentStreamLimits{
		MaxUnauthTotal: transports.MaxUnauthTotal,
		MaxUnauthPerIP: transports.MaxUnauthPerIP,
		PrefaceTimeout: transports.PrefaceTimeout,
	}
	if addr := strings.TrimSpace(transports.TCP.Listen); addr != "" {
		if err := r.bindAgentTCP(addr, transports.TCP, cfg.TLS, limits); err != nil {
			return err
		}
	}
	if addr := strings.TrimSpace(transports.QUIC.Listen); addr != "" {
		if err := r.bindAgentQUIC(addr, transports.QUIC, cfg.TLS, limits); err != nil {
			return err
		}
	}
	return nil
}

func (r *ServerRuntime) bindAgentQUIC(addr string, listener config.ServerAgentListenerConfig, native config.TLSConfig, limits agentStreamLimits) error {
	tlsConfig, err := loadAgentTLS(listener, native, true)
	if err != nil {
		return fmt.Errorf("server runtime: agent quic TLS certificate: %w", err)
	}
	if tlsConfig == nil {
		return fmt.Errorf("server runtime: agent quic transport on %s requires a TLS certificate", addr)
	}
	ln, err := quic.ListenAddr(addr, tlsConfig, quicAgentConfig())
	if err != nil {
		return fmt.Errorf("server runtime: listen for agent quic on %s: %w", addr, err)
	}
	stream := newAgentStreamListener("quic", tlsConfig, limits, r.preauthenticateAgentConnection, r.serveAgentConnection)
	stream.metrics = r.metrics
	r.agentStreams = append(r.agentStreams, agentStreamBinding{
		listener: stream,
		serve:    func(ctx context.Context) error { return stream.serveQUIC(ctx, ln) },
		closer:   ln,
	})
	slog.Info("agent_quic_listener_enabled", "listen", ln.Addr().String())
	return nil
}

func (r *ServerRuntime) bindAgentTCP(addr string, listener config.ServerAgentListenerConfig, native config.TLSConfig, limits agentStreamLimits) error {
	tlsConfig, err := loadAgentTLS(listener, native, false)
	if err != nil {
		return fmt.Errorf("server runtime: agent tcp TLS certificate: %w", err)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("server runtime: listen for agent tcp on %s: %w", addr, err)
	}
	stream := newAgentStreamListener("tcp", tlsConfig, limits, r.preauthenticateAgentConnection, r.serveAgentConnection)
	stream.metrics = r.metrics
	r.agentStreams = append(r.agentStreams, agentStreamBinding{
		listener: stream,
		serve:    func(ctx context.Context) error { return stream.Serve(ctx, ln) },
		closer:   ln,
	})
	slog.Info("agent_tcp_listener_enabled", "listen", ln.Addr().String(), "tls", tlsConfig != nil)
	return nil
}

// closeAgentStreams stops every raw listener and its live connections. It is safe to
// call from both shutdown and Close.
func (r *ServerRuntime) closeAgentStreams() {
	if r == nil {
		return
	}
	for _, binding := range r.agentStreams {
		_ = binding.listener.Close()
		_ = binding.closer.Close()
	}
}
