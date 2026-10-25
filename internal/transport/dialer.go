// Package transport contains shared outbound network adapters used by Agent and Client.
package transport

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"golang.org/x/net/http/httpproxy"
	xproxy "golang.org/x/net/proxy"
)

// WriteTimeout bounds one WebSocket message write. A peer that stops reading
// while its TCP connection stays up would otherwise block the writer forever,
// and nothing else notices because reads on the same link may still succeed.
const WriteTimeout = 30 * time.Second

const (
	ProxyModeDirect      = "direct"
	ProxyModeHTTPConnect = "http_connect"
	ProxyModeSOCKS5      = "socks5"
	ProxyModeSystem      = "system"
)

type DialOptions struct {
	Mode         string
	URL          string
	Username     string
	PasswordEnv  string
	PasswordFile string
	Timeout      time.Duration
}

type Dialer struct {
	options   DialOptions
	password  string
	direct    *net.Dialer
	proxyFunc func(*url.URL) (*url.URL, error)
}

func FromConfig(cfg config.TransportConfig) (*Dialer, error) {
	return NewDialer(DialOptions{
		Mode:         cfg.ProxyMode,
		URL:          cfg.ProxyURL,
		Username:     cfg.ProxyUsername,
		PasswordEnv:  cfg.ProxyPasswordEnv,
		PasswordFile: cfg.ProxyPasswordFile,
	})
}

func NewDialer(options DialOptions) (*Dialer, error) {
	mode := strings.ToLower(strings.TrimSpace(options.Mode))
	if mode == "" {
		mode = ProxyModeDirect
	}
	options.Mode = mode
	if options.Timeout <= 0 {
		options.Timeout = 15 * time.Second
	}
	if strings.TrimSpace(options.PasswordEnv) != "" && strings.TrimSpace(options.PasswordFile) != "" {
		return nil, errors.New("proxy password must use either environment or file")
	}
	if strings.TrimSpace(options.URL) != "" {
		parsed, err := url.Parse(strings.TrimSpace(options.URL))
		if err != nil || parsed.Host == "" || parsed.User != nil {
			return nil, errors.New("proxy URL must not contain credentials and must include a host")
		}
		switch mode {
		case ProxyModeHTTPConnect:
			if parsed.Scheme != "http" {
				return nil, errors.New("HTTP CONNECT proxy URL must use http://")
			}
		case ProxyModeSOCKS5:
			if parsed.Scheme != "socks5" && parsed.Scheme != "socks5h" {
				return nil, errors.New("SOCKS5 proxy URL must use socks5://")
			}
		}
	}
	password, err := loadPassword(options)
	if err != nil {
		return nil, err
	}
	dialer := &Dialer{
		options:  options,
		password: password,
		direct:   &net.Dialer{Timeout: options.Timeout, KeepAlive: 30 * time.Second},
	}
	if mode == ProxyModeSystem {
		cfg := httpproxy.FromEnvironment()
		dialer.proxyFunc = cfg.ProxyFunc()
	}
	return dialer, nil
}

func (d *Dialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return d.DialContextForScheme(ctx, network, address, "")
}

// DialContextForScheme uses the URL scheme when selecting an environment proxy.
// HTTP proxy conventions are scheme-based, so inferring HTTPS from port 443 would
// misroute valid wss:// endpoints on alternate TLS ports.
func (d *Dialer) DialContextForScheme(ctx context.Context, network, address, scheme string) (net.Conn, error) {
	if d == nil {
		return nil, errors.New("transport dialer is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch d.options.Mode {
	case ProxyModeDirect:
		return d.direct.DialContext(ctx, network, address)
	case ProxyModeHTTPConnect:
		return d.dialHTTPConnect(ctx, network, address, d.options.URL)
	case ProxyModeSOCKS5:
		return d.dialSOCKS5(ctx, network, address, d.options.URL)
	case ProxyModeSystem:
		return d.dialSystem(ctx, network, address, scheme)
	default:
		return nil, fmt.Errorf("unsupported proxy mode %q", d.options.Mode)
	}
}

func (d *Dialer) dialSystem(ctx context.Context, network, address, scheme string) (net.Conn, error) {
	if _, _, err := net.SplitHostPort(address); err != nil {
		return nil, err
	}
	if scheme == "" {
		scheme = "http"
	}
	switch strings.ToLower(scheme) {
	case "ws":
		scheme = "http"
	case "wss":
		scheme = "https"
	}
	target := &url.URL{Scheme: scheme, Host: address}
	proxyURL, err := d.proxyFunc(target)
	if err != nil {
		return nil, err
	}
	if proxyURL == nil {
		return d.direct.DialContext(ctx, network, address)
	}
	switch proxyURL.Scheme {
	case "http":
		return d.dialHTTPConnect(ctx, network, address, proxyURL.String())
	case "socks5", "socks5h":
		return d.dialSOCKS5(ctx, network, address, proxyURL.String())
	default:
		return nil, fmt.Errorf("unsupported system proxy scheme %q", proxyURL.Scheme)
	}
}

func (d *Dialer) dialSOCKS5(ctx context.Context, network, address, rawURL string) (net.Conn, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return nil, errors.New("SOCKS5 proxy URL must include a host")
	}
	username, password := d.options.Username, d.password
	if parsed.User != nil {
		username = parsed.User.Username()
		if value, ok := parsed.User.Password(); ok {
			password = value
		}
	}
	auth := &xproxy.Auth{User: username, Password: password}
	var authPtr *xproxy.Auth
	if auth.User != "" || auth.Password != "" {
		authPtr = auth
	}
	base := netDialerAdapter{dialer: d.direct}
	socks, err := xproxy.SOCKS5(network, parsed.Host, authPtr, base)
	if err != nil {
		return nil, err
	}
	if contextual, ok := socks.(xproxy.ContextDialer); ok {
		return contextual.DialContext(ctx, network, address)
	}
	return dialWithContext(ctx, socks, network, address)
}

func (d *Dialer) dialHTTPConnect(ctx context.Context, network, address, rawURL string) (net.Conn, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return nil, errors.New("HTTP CONNECT proxy URL must include a host")
	}
	conn, err := d.direct.DialContext(ctx, network, parsed.Host)
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			_ = conn.Close()
		}
	}()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	request := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: address},
		Host:   address,
		Header: make(http.Header),
	}
	username, password := d.options.Username, d.password
	if parsed.User != nil {
		username = parsed.User.Username()
		if value, ok := parsed.User.Password(); ok {
			password = value
		}
	}
	if username != "" || password != "" {
		credentials := username + ":" + password
		request.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(credentials)))
	}
	if err := request.Write(conn); err != nil {
		return nil, err
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), request)
	if err != nil {
		return nil, err
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP CONNECT proxy returned %s", response.Status)
	}
	_ = conn.SetDeadline(time.Time{})
	success = true
	return conn, nil
}

type netDialerAdapter struct{ dialer *net.Dialer }

func (d netDialerAdapter) Dial(network, address string) (net.Conn, error) {
	return d.dialer.Dial(network, address)
}

func loadPassword(options DialOptions) (string, error) {
	if name := strings.TrimSpace(options.PasswordEnv); name != "" {
		return os.Getenv(name), nil
	}
	if path := strings.TrimSpace(options.PasswordFile); path != "" {
		info, err := os.Stat(path)
		if err != nil {
			return "", fmt.Errorf("read proxy password file: %w", err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			return "", fmt.Errorf("proxy password file %q must not be accessible by group or other users", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read proxy password file: %w", err)
		}
		return strings.TrimSpace(string(data)), nil
	}
	return "", nil
}

func dialWithContext(ctx context.Context, dialer xproxy.Dialer, network, address string) (net.Conn, error) {
	result := make(chan struct {
		conn net.Conn
		err  error
	}, 1)
	go func() {
		conn, err := dialer.Dial(network, address)
		result <- struct {
			conn net.Conn
			err  error
		}{conn: conn, err: err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-result:
		return result.conn, result.err
	}
}
