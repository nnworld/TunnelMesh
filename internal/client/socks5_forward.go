package client

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/proxy"
)

type SOCKS5AuthMode string

const (
	SOCKS5AuthNone     SOCKS5AuthMode = "none"
	SOCKS5AuthPassword SOCKS5AuthMode = "password"
)

type SOCKS5ForwardConfig struct {
	ListenAddr       string
	AgentID          string
	ConnectTimeout   time.Duration
	HandshakeTimeout time.Duration
	AllowRemote      bool
	AuthMode         SOCKS5AuthMode
	Username         string
	Password         string
	AuthURL          string
	RemoteValidation RemoteValidationCacheConfig
}

type SOCKS5Forward struct {
	opener    StreamOpener
	cfg       SOCKS5ForwardConfig
	validator *RemoteValidator
	ln        *TCPListener
	mu        sync.Mutex
	closed    bool
	active    map[net.Conn]struct{}
}

func NewSOCKS5Forward(opener StreamOpener, cfg SOCKS5ForwardConfig) (*SOCKS5Forward, error) {
	if opener == nil {
		return nil, errors.New("client: nil stream opener")
	}
	if cfg.ListenAddr == "" || cfg.AgentID == "" {
		return nil, errors.New("client: listen address and agent id are required")
	}
	if cfg.AuthMode == "" {
		cfg.AuthMode = SOCKS5AuthNone
	}
	if cfg.AuthMode != SOCKS5AuthNone && cfg.AuthMode != SOCKS5AuthPassword {
		return nil, errors.New("client: unsupported SOCKS5 auth mode")
	}
	if len(cfg.Username) > 255 || len(cfg.Password) > 255 {
		return nil, errors.New("client: SOCKS5 username and password must be at most 255 bytes")
	}
	if cfg.AuthMode == SOCKS5AuthPassword && (cfg.Username == "" || cfg.Password == "") {
		return nil, errors.New("client: SOCKS5 username and password are required")
	}
	host, _, err := net.SplitHostPort(cfg.ListenAddr)
	if err != nil {
		return nil, errors.New("client: invalid SOCKS5 listen address")
	}
	if !isLoopbackHost(host) {
		if !cfg.AllowRemote {
			return nil, errors.New("client: non-loopback SOCKS5 listener requires --allow-remote")
		}
		if cfg.AuthMode != SOCKS5AuthPassword {
			return nil, errors.New("client: non-loopback SOCKS5 listener requires password auth")
		}
	}
	return &SOCKS5Forward{
		opener:    opener,
		cfg:       cfg,
		validator: newForwardRemoteValidator(cfg.AuthURL, cfg.RemoteValidation),
		active:    make(map[net.Conn]struct{}),
	}, nil
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (f *SOCKS5Forward) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
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

func (f *SOCKS5Forward) Addr() net.Addr {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ln == nil {
		return nil
	}
	return f.ln.Addr()
}

func (f *SOCKS5Forward) Close() error {
	f.validator.Close()
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return nil
	}
	f.closed = true
	ln := f.ln
	active := make([]net.Conn, 0, len(f.active))
	for conn := range f.active {
		active = append(active, conn)
	}
	f.mu.Unlock()
	for _, conn := range active {
		_ = conn.Close()
	}
	if ln != nil {
		return ln.Close()
	}
	return nil
}

func (f *SOCKS5Forward) handleConn(local net.Conn) {
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

	timeout := f.cfg.HandshakeTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if err := local.SetDeadline(time.Now().Add(timeout)); err != nil {
		return
	}
	if !f.negotiateAndAuthenticate(local) {
		return
	}
	request, err := proxy.ReadSOCKS5Request(local)
	if err != nil {
		return
	}
	if request.Command != proxy.SOCKS5Connect {
		_, _ = local.Write(proxy.EncodeSOCKS5Reply(proxy.SOCKS5ReplyCommandUnsupported))
		return
	}
	if !f.validator.Validate(context.Background(), RemoteValidationRequest{
		Protocol: "socks5", AgentID: f.cfg.AgentID, TargetHost: request.Host, TargetPort: request.Port,
		Username: f.cfg.Username, Password: f.cfg.Password,
	}) {
		_, _ = local.Write(proxy.EncodeSOCKS5Reply(proxy.SOCKS5ReplyConnectionNotAllowed))
		return
	}
	remote, result, err := f.openStreamResult(request)
	if err != nil || remote == nil || !result.Accepted {
		reply := proxy.SOCKS5ReplyGeneralFailure
		if err == nil && result.Code != "" {
			reply = proxy.SOCKS5Reply(proxy.SOCKS5ReplyForResult(result))
		}
		_, _ = local.Write(proxy.EncodeSOCKS5Reply(reply))
		return
	}
	defer remote.Close()
	if err := local.SetDeadline(time.Time{}); err != nil {
		return
	}
	if _, err := local.Write(proxy.EncodeSOCKS5Reply(proxy.SOCKS5ReplySucceeded)); err != nil {
		return
	}
	_ = bridge(local, remote)
}

func (f *SOCKS5Forward) negotiateAndAuthenticate(local net.Conn) bool {
	methods, err := proxy.ReadSOCKS5Methods(local)
	if err != nil {
		return false
	}
	if f.cfg.AuthMode == SOCKS5AuthPassword {
		if !proxy.SupportsSOCKS5UsernamePassword(methods) {
			_, _ = local.Write(proxy.EncodeSOCKS5MethodSelection(0xff))
			return false
		}
		if _, err := local.Write(proxy.EncodeSOCKS5MethodSelection(proxy.SOCKS5MethodUsernamePassword)); err != nil {
			return false
		}
		credentials, err := proxy.ReadSOCKS5UsernamePassword(local)
		if err != nil {
			return false
		}
		valid := socks5CredentialsEqual(credentials, proxy.SOCKS5Credentials{
			Username: f.cfg.Username,
			Password: f.cfg.Password,
		})
		if _, err := local.Write(proxy.EncodeSOCKS5UsernamePasswordReply(valid)); err != nil || !valid {
			return false
		}
		return true
	}
	if !proxy.SupportsSOCKS5NoAuth(methods) {
		_, _ = local.Write(proxy.EncodeSOCKS5MethodSelection(0xff))
		return false
	}
	_, err = local.Write(proxy.EncodeSOCKS5MethodSelection(proxy.SOCKS5MethodNoAuth))
	return err == nil
}

func (f *SOCKS5Forward) openStreamResult(request proxy.SOCKS5Request) (io.ReadWriteCloser, protocol.OpenResultPayload, error) {
	ctx := context.Background()
	var cancel context.CancelFunc
	if f.cfg.ConnectTimeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, f.cfg.ConnectTimeout)
		defer cancel()
	}
	req := StreamRequest{
		AgentID:    f.cfg.AgentID,
		Protocol:   "tcp",
		TargetHost: request.Host,
		TargetPort: request.Port,
	}
	if resultOpener, ok := f.opener.(ResultStreamOpener); ok {
		return resultOpener.OpenStreamResult(ctx, req)
	}
	stream, err := f.opener.OpenStream(ctx, req)
	if err != nil || stream == nil {
		return nil, protocol.OpenResultPayload{Accepted: false, Stage: protocol.OpenResultStageConnect, Code: protocol.OpenResultCodeInternalError}, err
	}
	return stream, protocol.OpenResultPayload{Accepted: true, Stage: protocol.OpenResultStageConnect, Code: protocol.OpenResultCodeOK}, nil
}

func socks5CredentialsEqual(provided, expected proxy.SOCKS5Credentials) bool {
	providedDigest := socks5CredentialDigest(provided)
	expectedDigest := socks5CredentialDigest(expected)
	return subtle.ConstantTimeCompare(providedDigest[:], expectedDigest[:]) == 1
}

func socks5CredentialDigest(credentials proxy.SOCKS5Credentials) [sha256.Size]byte {
	digest := sha256.New()
	_, _ = digest.Write([]byte{byte(len(credentials.Username))})
	_, _ = digest.Write([]byte(credentials.Username))
	_, _ = digest.Write([]byte{byte(len(credentials.Password))})
	_, _ = digest.Write([]byte(credentials.Password))
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return result
}
