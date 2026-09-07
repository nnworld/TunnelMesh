package relay

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

const (
	serverNodeAuthorizationMetadata = "authorization"
	serverNodeIDMetadata            = "x-tunnelmesh-node-id"
	serverNodeEpochMetadata         = "x-tunnelmesh-node-epoch"
)

// ServerNodePrincipal is the authenticated caller identity for an inter-node
// relay stream. It is deliberately separate from StreamRequest.NodeID, which
// remains the target route selected by the caller.
type ServerNodePrincipal struct {
	NodeID  string
	Epoch   int64
	TokenID string
}

type serverNodePrincipalContextKey struct{}

// ServerNodePrincipalFromContext returns the identity established by the
// server stream interceptor. Handlers must not derive caller identity from the
// first application message.
func ServerNodePrincipalFromContext(ctx context.Context) (ServerNodePrincipal, bool) {
	principal, ok := ctx.Value(serverNodePrincipalContextKey{}).(ServerNodePrincipal)
	return principal, ok
}

// NewServerNodeStreamInterceptor authenticates every relay stream before its
// handler can receive the first target-routing message. TLS peer verification
// is performed by grpc credentials; this interceptor additionally checks that
// the verified leaf certificate contains the exact caller node SAN.
func NewServerNodeStreamInterceptor(credentialsService *auth.CredentialService, nodes storage.NodeRepository) grpc.StreamServerInterceptor {
	return func(srv interface{}, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		principal, err := authenticateServerNode(stream.Context(), credentialsService, nodes)
		if err != nil {
			return err
		}
		return handler(srv, &serverNodeContextStream{ServerStream: stream, ctx: context.WithValue(stream.Context(), serverNodePrincipalContextKey{}, principal)})
	}
}

type serverNodeContextStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *serverNodeContextStream) Context() context.Context { return s.ctx }

func authenticateServerNode(ctx context.Context, credentialsService *auth.CredentialService, nodes storage.NodeRepository) (ServerNodePrincipal, error) {
	callerNodeID, callerEpoch, rawToken, err := parseServerNodeMetadata(ctx)
	if err != nil {
		return ServerNodePrincipal{}, err
	}
	cert, err := verifiedPeerCertificate(ctx)
	if err != nil {
		return ServerNodePrincipal{}, err
	}
	if !certificateHasExactSAN(cert, callerNodeID) {
		return ServerNodePrincipal{}, status.Error(codes.PermissionDenied, "relay peer identity denied")
	}
	identity, err := credentialsService.ValidateAs(ctx, rawToken, storage.TokenTypeServerNode)
	if err != nil {
		return ServerNodePrincipal{}, status.Error(codes.Unauthenticated, "relay authentication failed")
	}
	if identity.NodeID != callerNodeID {
		return ServerNodePrincipal{}, status.Error(codes.PermissionDenied, "relay credential denied")
	}
	if nodes == nil {
		return ServerNodePrincipal{}, status.Error(codes.Unauthenticated, "relay authentication failed")
	}
	node, err := nodes.Get(ctx, callerNodeID)
	if err != nil {
		return ServerNodePrincipal{}, status.Error(codes.Unauthenticated, "relay authentication failed")
	}
	if node.ExpiresAt != nil && !node.ExpiresAt.After(nowUTC()) {
		return ServerNodePrincipal{}, status.Error(codes.FailedPrecondition, "relay node is expired")
	}
	if node.Epoch != callerEpoch {
		return ServerNodePrincipal{}, status.Error(codes.FailedPrecondition, "relay node epoch is stale")
	}
	return ServerNodePrincipal{NodeID: callerNodeID, Epoch: callerEpoch, TokenID: identity.TokenID}, nil
}

// nowUTC is a variable to keep expiration checks deterministic in focused
// tests without exposing clock policy to relay callers.
var nowUTC = func() time.Time { return time.Now().UTC() }

func parseServerNodeMetadata(ctx context.Context) (string, int64, string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", 0, "", status.Error(codes.Unauthenticated, "relay metadata missing")
	}
	one := func(key string) (string, error) {
		values := md.Get(key)
		if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
			return "", status.Error(codes.Unauthenticated, "relay metadata invalid")
		}
		return values[0], nil
	}
	authorization, err := one(serverNodeAuthorizationMetadata)
	if err != nil {
		return "", 0, "", err
	}
	if !strings.HasPrefix(authorization, "Bearer ") || strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer ")) == "" || strings.ContainsAny(strings.TrimPrefix(authorization, "Bearer "), " \t\r\n") {
		return "", 0, "", status.Error(codes.Unauthenticated, "relay metadata invalid")
	}
	rawToken := strings.TrimPrefix(authorization, "Bearer ")
	callerNodeID, err := one(serverNodeIDMetadata)
	if err != nil || strings.TrimSpace(callerNodeID) != callerNodeID {
		return "", 0, "", status.Error(codes.Unauthenticated, "relay metadata invalid")
	}
	epochRaw, err := one(serverNodeEpochMetadata)
	if err != nil {
		return "", 0, "", err
	}
	epoch, err := strconv.ParseInt(epochRaw, 10, 64)
	if err != nil || epoch <= 0 {
		return "", 0, "", status.Error(codes.Unauthenticated, "relay metadata invalid")
	}
	return callerNodeID, epoch, rawToken, nil
}

func verifiedPeerCertificate(ctx context.Context) (*x509.Certificate, error) {
	p, ok := peer.FromContext(ctx)
	if !ok || p.AuthInfo == nil {
		return nil, status.Error(codes.Unauthenticated, "relay peer certificate missing")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || tlsInfo.State.Version < tls.VersionTLS12 || len(tlsInfo.State.PeerCertificates) == 0 || len(tlsInfo.State.VerifiedChains) == 0 {
		return nil, status.Error(codes.Unauthenticated, "relay peer certificate invalid")
	}
	return tlsInfo.State.PeerCertificates[0], nil
}

func certificateHasExactSAN(cert *x509.Certificate, nodeID string) bool {
	if cert == nil || nodeID == "" {
		return false
	}
	for _, dnsName := range cert.DNSNames {
		if strings.Contains(dnsName, "*") {
			continue
		}
		if strings.EqualFold(dnsName, nodeID) {
			return true
		}
	}
	if ip := net.ParseIP(nodeID); ip != nil {
		for _, candidate := range cert.IPAddresses {
			if candidate.Equal(ip) {
				return true
			}
		}
	}
	for _, rawURI := range cert.URIs {
		if rawURI != nil && rawURI.String() == nodeID {
			return true
		}
	}
	return false
}

// NewServerNodeStreamClientInterceptor injects the caller identity on every
// stream. The raw token remains in outgoing metadata only and is never copied
// into request payloads or errors.
func NewServerNodeStreamClientInterceptor(nodeID string, epoch int64, rawToken string) grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		if strings.TrimSpace(nodeID) == "" || epoch <= 0 || strings.TrimSpace(rawToken) == "" || strings.ContainsAny(rawToken, " \t\r\n") {
			return nil, status.Error(codes.Unauthenticated, "relay client identity is invalid")
		}
		ctx = metadata.AppendToOutgoingContext(ctx, serverNodeAuthorizationMetadata, "Bearer "+rawToken, serverNodeIDMetadata, nodeID, serverNodeEpochMetadata, strconv.FormatInt(epoch, 10))
		return streamer(ctx, desc, cc, method, opts...)
	}
}

// NewRelayServerTLSConfig applies the relay-specific mTLS policy. Public HTTP
// TLS settings must not be reused for this listener.
func NewRelayServerTLSConfig(cert tls.Certificate, roots *x509.CertPool) (*tls.Config, error) {
	if len(cert.Certificate) == 0 || cert.PrivateKey == nil || roots == nil {
		return nil, errors.New("relay: server certificate, private key, and client CA are required")
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots, MinVersion: tls.VersionTLS12}, nil
}

// NewRelayClientTLSConfig applies the relay client certificate and strict
// server verification policy.
func NewRelayClientTLSConfig(cert tls.Certificate, roots *x509.CertPool, serverName string) (*tls.Config, error) {
	if len(cert.Certificate) == 0 || cert.PrivateKey == nil || roots == nil || strings.TrimSpace(serverName) == "" {
		return nil, errors.New("relay: client certificate, root CAs, and server name are required")
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, RootCAs: roots, ServerName: serverName, MinVersion: tls.VersionTLS12, InsecureSkipVerify: false}, nil
}
