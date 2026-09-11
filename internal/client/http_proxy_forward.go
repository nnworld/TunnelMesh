package client

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

type HTTPProxyAuthMode string

const (
	HTTPProxyAuthNone  HTTPProxyAuthMode = "none"
	HTTPProxyAuthBasic HTTPProxyAuthMode = "basic"
)

type HTTPProxyForwardConfig struct {
	ListenAddr       string
	AgentID          string
	AllowRemote      bool
	AuthMode         HTTPProxyAuthMode
	Username         string
	Password         string
	AuthURL          string
	RemoteValidation RemoteValidationCacheConfig
}

type HTTPProxyForward struct {
	opener    StreamOpener
	cfg       HTTPProxyForwardConfig
	validator *RemoteValidator
	server    *http.Server
	ln        net.Listener
	mu        sync.Mutex
}

func NewHTTPProxyForward(opener StreamOpener, cfg HTTPProxyForwardConfig) (*HTTPProxyForward, error) {
	if opener == nil {
		return nil, errors.New("client: nil stream opener")
	}
	if cfg.ListenAddr == "" || cfg.AgentID == "" {
		return nil, errors.New("client: listen address and agent id are required")
	}
	if cfg.AuthMode == "" {
		cfg.AuthMode = HTTPProxyAuthNone
	}
	if cfg.AuthMode != HTTPProxyAuthNone && cfg.AuthMode != HTTPProxyAuthBasic {
		return nil, errors.New("client: unsupported HTTP proxy auth mode")
	}
	if cfg.AuthMode == HTTPProxyAuthBasic && (cfg.Username == "" || cfg.Password == "") {
		return nil, errors.New("client: HTTP proxy username and password are required")
	}
	host, _, err := net.SplitHostPort(cfg.ListenAddr)
	if err != nil {
		return nil, errors.New("client: invalid HTTP proxy listen address")
	}
	if !isLoopbackHost(host) {
		if !cfg.AllowRemote {
			return nil, errors.New("client: non-loopback HTTP proxy listener requires --allow-remote")
		}
		if cfg.AuthMode != HTTPProxyAuthBasic {
			return nil, errors.New("client: non-loopback HTTP proxy listener requires basic auth")
		}
	}
	return &HTTPProxyForward{opener: opener, cfg: cfg, validator: newForwardRemoteValidator(cfg.AuthURL, cfg.RemoteValidation)}, nil
}

func (f *HTTPProxyForward) Start(ctx context.Context) error {
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
	server := &http.Server{Handler: http.HandlerFunc(f.handleProxy)}
	f.server = server
	go func() { _ = server.Serve(ln) }()
	go func() {
		<-ctx.Done()
		_ = f.Close()
	}()
	return nil
}

func (f *HTTPProxyForward) Addr() net.Addr {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ln == nil {
		return nil
	}
	return f.ln.Addr()
}

func (f *HTTPProxyForward) Close() error {
	f.validator.Close()
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

func (f *HTTPProxyForward) handleProxy(w http.ResponseWriter, r *http.Request) {
	if !f.authorize(r) {
		w.Header().Set("Proxy-Authenticate", `Basic realm="TunnelMesh"`)
		http.Error(w, "proxy authentication required", http.StatusProxyAuthRequired)
		return
	}
	if r.Method == http.MethodConnect {
		f.handleConnect(w, r)
		return
	}
	f.handleAbsoluteForm(w, r)
}

func (f *HTTPProxyForward) handleConnect(w http.ResponseWriter, r *http.Request) {
	host, port, err := parseConnectTarget(r.Host)
	if err != nil {
		http.Error(w, "invalid CONNECT target", http.StatusBadRequest)
		return
	}
	if !f.validator.Validate(r.Context(), RemoteValidationRequest{
		Protocol: "http-proxy", AgentID: f.cfg.AgentID, TargetHost: host, TargetPort: port,
		Username: f.cfg.Username, Password: f.cfg.Password,
	}) {
		http.Error(w, "remote validation denied", http.StatusForbidden)
		return
	}
	stream, err := f.opener.OpenStream(r.Context(), StreamRequest{
		AgentID: f.cfg.AgentID, Protocol: "tcp", TargetHost: host, TargetPort: port,
	})
	if err != nil {
		http.Error(w, "tunnel unavailable", http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		_ = stream.Close()
		http.Error(w, "CONNECT hijacking unsupported", http.StatusHTTPVersionNotSupported)
		return
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		_ = stream.Close()
		return
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		_ = stream.Close()
		return
	}
	if rw != nil && rw.Reader != nil {
		if buffered := rw.Reader.Buffered(); buffered > 0 {
			data, peekErr := rw.Reader.Peek(buffered)
			if peekErr == nil {
				if _, writeErr := stream.Write(data); writeErr != nil {
					_ = stream.Close()
					return
				}
			}
		}
	}
	_ = bridge(conn, stream)
}

func (f *HTTPProxyForward) handleAbsoluteForm(w http.ResponseWriter, r *http.Request) {
	if r.URL.Host == "" {
		http.Error(w, "absolute-form HTTP request required", http.StatusBadRequest)
		return
	}
	if !strings.EqualFold(r.URL.Scheme, "http") {
		http.Error(w, "HTTPS requires CONNECT", http.StatusBadRequest)
		return
	}
	host, port, err := parseAbsoluteFormTarget(r.URL.Host)
	if err != nil {
		http.Error(w, "invalid proxy target", http.StatusBadRequest)
		return
	}
	if !f.validator.Validate(r.Context(), RemoteValidationRequest{
		Protocol: "http-proxy", AgentID: f.cfg.AgentID, TargetHost: host, TargetPort: port,
		Username: f.cfg.Username, Password: f.cfg.Password,
	}) {
		http.Error(w, "remote validation denied", http.StatusForbidden)
		return
	}
	stream, err := f.opener.OpenStream(r.Context(), StreamRequest{
		AgentID: f.cfg.AgentID, Protocol: "http", TargetHost: host, TargetPort: port,
	})
	if err != nil {
		http.Error(w, "tunnel unavailable", http.StatusBadGateway)
		return
	}
	upstream := normalizeProxyRequest(r, host, port)
	forwardHTTP(w, upstream, stream)
}

func (f *HTTPProxyForward) authorize(r *http.Request) bool {
	if f.cfg.AuthMode != HTTPProxyAuthBasic {
		return true
	}
	header := r.Header.Get("Proxy-Authorization")
	scheme, value, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(strings.TrimSpace(scheme), "Basic") {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return false
	}
	username, password, ok := strings.Cut(string(decoded), ":")
	if !ok {
		return false
	}
	return httpProxyCredentialsEqual(username, password, f.cfg.Username, f.cfg.Password)
}

func parseConnectTarget(target string) (string, int, error) {
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return "", 0, err
	}
	if portText == "" {
		portText = "443"
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 || host == "" {
		return "", 0, errors.New("client: invalid CONNECT target")
	}
	return host, port, nil
}

func parseAbsoluteFormTarget(target string) (string, int, error) {
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		host = target
		portText = ""
	}
	if portText == "" {
		portText = "80"
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 || host == "" {
		return "", 0, errors.New("client: invalid proxy target")
	}
	return host, port, nil
}

func normalizeProxyRequest(r *http.Request, host string, port int) *http.Request {
	upstream := r.Clone(r.Context())
	upstream.RequestURI = ""
	upstream.URL.Scheme = ""
	upstream.URL.Host = ""
	upstream.Host = net.JoinHostPort(host, strconv.Itoa(port))
	upstream.Header.Del("Proxy-Authorization")
	upstream.Header.Del("Proxy-Connection")
	return upstream
}

func httpProxyCredentialsEqual(providedUsername, providedPassword, expectedUsername, expectedPassword string) bool {
	providedDigest := httpProxyCredentialDigest(providedUsername, providedPassword)
	expectedDigest := httpProxyCredentialDigest(expectedUsername, expectedPassword)
	return subtle.ConstantTimeCompare(providedDigest[:], expectedDigest[:]) == 1
}

func httpProxyCredentialDigest(username, password string) [sha256.Size]byte {
	digest := sha256.New()
	_, _ = digest.Write([]byte(username))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write([]byte(password))
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return result
}
