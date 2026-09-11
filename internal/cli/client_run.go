package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/tunnelmesh/tunnelmesh/internal/client"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
)

// runClientTunnels starts every configured local ingress in one process. The
// listeners are intentionally independent from WebSocket reconnects; only the
// per-Agent session pool behind their openers is re-established.
func runClientTunnels(cmd *cobra.Command, cfg config.Config) error {
	if strings.TrimSpace(cfg.Client.ServerURL) == "" || strings.TrimSpace(cfg.Client.Token) == "" {
		return fmt.Errorf("client run requires client.server_url and client.token")
	}
	if len(cfg.Client.Tunnels) == 0 {
		return fmt.Errorf("client run requires at least one client.tunnels entry")
	}

	var active []io.Closer
	closeActive := func() {
		for i := len(active) - 1; i >= 0; i-- {
			_ = active[i].Close()
		}
		active = nil
	}
	defer closeActive()

	manager := client.NewSessionPoolManager(client.SessionPoolManagerOptions{
		ServerURL: cfg.Client.ServerURL,
		Token:     cfg.Client.Token,
		Config: client.SessionPoolConfig{
			Min:                cfg.Client.Connections.Min,
			Max:                cfg.Client.Connections.Max,
			HighWatermark:      cfg.Client.Connections.HighWatermark,
			LowWatermark:       cfg.Client.Connections.LowWatermark,
			EvaluationInterval: cfg.Client.Connections.EvaluationInterval,
			Cooldown:           cfg.Client.Connections.Cooldown,
		},
		Runner: runClientSessionPool,
	})

	for _, tunnel := range cfg.Client.Tunnels {
		forward, description, err := newConfiguredClientForward(manager.Opener(tunnel.AgentID), cfg.Client, tunnel)
		if err != nil {
			return err
		}
		if err := forward.Start(cmd.Context()); err != nil {
			_ = forward.Close()
			return err
		}
		active = append(active, forward)
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), description)
	}

	return manager.Run(cmd.Context())
}

func newConfiguredClientForward(opener client.StreamOpener, cfg config.ClientConfig, tunnel config.TunnelConfig) (clientForward, string, error) {
	switch strings.ToLower(strings.TrimSpace(tunnel.Protocol)) {
	case "tcp":
		forward, err := client.NewTCPForward(opener, client.TCPForwardConfig{
			ListenAddr: tunnel.ListenAddr, AgentID: tunnel.AgentID,
			TargetHost: tunnel.TargetHost, TargetPort: tunnel.TargetPort,
		})
		return forward, fmt.Sprintf("tcp tunnel %s listening on %s", tunnelDisplayName(tunnel), tunnel.ListenAddr), err
	case "udp":
		forward, err := client.NewUDPForward(opener, client.UDPForwardConfig{
			ListenAddr: tunnel.ListenAddr, AgentID: tunnel.AgentID,
			TargetHost: tunnel.TargetHost, TargetPort: tunnel.TargetPort,
		})
		return forward, fmt.Sprintf("udp tunnel %s listening on %s", tunnelDisplayName(tunnel), tunnel.ListenAddr), err
	case "http":
		forward, err := client.NewHTTPForward(opener, client.HTTPForwardConfig{
			ListenAddr: tunnel.ListenAddr, AgentID: tunnel.AgentID,
			TargetHost: tunnel.TargetHost, TargetPort: tunnel.TargetPort,
		})
		return forward, fmt.Sprintf("http tunnel %s listening on %s", tunnelDisplayName(tunnel), tunnel.ListenAddr), err
	case "socks5":
		forward, err := newConfiguredSOCKS5Forward(opener, cfg, tunnel)
		return forward, fmt.Sprintf("socks5 tunnel %s listening on %s", tunnelDisplayName(tunnel), tunnel.ListenAddr), err
	case "http-proxy":
		forward, err := newConfiguredHTTPProxyForward(opener, cfg, tunnel)
		return forward, fmt.Sprintf("http-proxy tunnel %s listening on %s", tunnelDisplayName(tunnel), tunnel.ListenAddr), err
	default:
		return nil, "", fmt.Errorf("unsupported tunnel protocol %q", tunnel.Protocol)
	}
}

func newConfiguredSOCKS5Forward(opener client.StreamOpener, cfg config.ClientConfig, tunnel config.TunnelConfig) (*client.SOCKS5Forward, error) {
	authMode := strings.ToLower(strings.TrimSpace(tunnel.AuthMode))
	username := ""
	password := ""
	if authMode == "password" {
		username = os.Getenv("TUNNELMESH_SOCKS5_USERNAME")
		password = os.Getenv("TUNNELMESH_SOCKS5_PASSWORD")
		if username == "" || password == "" {
			return nil, fmt.Errorf("socks5 password auth requires TUNNELMESH_SOCKS5_USERNAME and TUNNELMESH_SOCKS5_PASSWORD")
		}
	}
	return client.NewSOCKS5Forward(opener, client.SOCKS5ForwardConfig{
		ListenAddr:  tunnel.ListenAddr,
		AgentID:     tunnel.AgentID,
		AllowRemote: tunnel.AllowRemote,
		AuthMode:    client.SOCKS5AuthMode(authMode),
		Username:    username,
		Password:    password,
		AuthURL:     tunnel.AuthURL,
		RemoteValidation: client.RemoteValidationCacheConfig{
			Endpoint:    tunnel.AuthURL,
			PositiveTTL: cfg.RemoteValidation.PositiveTTL,
			NegativeTTL: cfg.RemoteValidation.NegativeTTL,
			Timeout:     cfg.RemoteValidation.Timeout,
			MaxEntries:  cfg.RemoteValidation.MaxEntries,
		},
	})
}

func newConfiguredHTTPProxyForward(opener client.StreamOpener, cfg config.ClientConfig, tunnel config.TunnelConfig) (*client.HTTPProxyForward, error) {
	authMode := strings.ToLower(strings.TrimSpace(tunnel.AuthMode))
	username := ""
	password := ""
	if authMode == "basic" {
		username = os.Getenv("TUNNELMESH_HTTP_PROXY_USERNAME")
		password = os.Getenv("TUNNELMESH_HTTP_PROXY_PASSWORD")
		if username == "" || password == "" {
			return nil, fmt.Errorf("http-proxy basic auth requires TUNNELMESH_HTTP_PROXY_USERNAME and TUNNELMESH_HTTP_PROXY_PASSWORD")
		}
	}
	return client.NewHTTPProxyForward(opener, client.HTTPProxyForwardConfig{
		ListenAddr:  tunnel.ListenAddr,
		AgentID:     tunnel.AgentID,
		AllowRemote: tunnel.AllowRemote,
		AuthMode:    client.HTTPProxyAuthMode(authMode),
		Username:    username,
		Password:    password,
		AuthURL:     tunnel.AuthURL,
		RemoteValidation: client.RemoteValidationCacheConfig{
			Endpoint:    tunnel.AuthURL,
			PositiveTTL: cfg.RemoteValidation.PositiveTTL,
			NegativeTTL: cfg.RemoteValidation.NegativeTTL,
			Timeout:     cfg.RemoteValidation.Timeout,
			MaxEntries:  cfg.RemoteValidation.MaxEntries,
		},
	})
}

type clientForward interface {
	Start(context.Context) error
	Close() error
}

func tunnelDisplayName(t config.TunnelConfig) string {
	if strings.TrimSpace(t.Name) != "" {
		return strings.TrimSpace(t.Name)
	}
	return "unnamed"
}
