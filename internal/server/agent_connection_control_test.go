package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"strconv"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/registry"
	"github.com/tunnelmesh/tunnelmesh/internal/relay"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func TestRuntimeCloseAgentConnectionUsesAuthenticatedCallerAndExactLeaseEpoch(t *testing.T) {
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, "file:runtime-agent-close-control?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authService := auth.NewAuthService(db)
	owner, err := authService.CreateUser(ctx, "runtime-close-owner", "password", "user")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Nodes().Create(ctx, storage.ServerNode{ID: "node-a", Epoch: 7}); err != nil {
		t.Fatal(err)
	}
	credentialsService := auth.NewCredentialService(db)
	defer credentialsService.Close()
	created, err := credentialsService.Create(ctx, auth.CreateTokenInput{
		Type: storage.TokenTypeServerNode, OwnerUserID: owner.ID,
		Scope: auth.TokenScope{ServerNodeIDs: []string{"node-a"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	manager := NewAgentSessionManager(AgentSessionConfig{})
	transport := newFakeTransport()
	session, err := manager.Register(ctx, AgentRegistration{
		AgentID: "agent-runtime-close", NodeID: "node-agent", Epoch: 12,
		InstanceID: "instance-a", ConnectionID: "conn-a", ConnectionEpoch: 12,
	}, transport)
	if err != nil {
		t.Fatal(err)
	}
	controller := NewAgentConnectionLeaseController(nil, manager, nil, "server-local", "", time.Minute)
	controller.owners[session] = registry.NodeOwner{
		AgentID: session.AgentID, ConnectionID: session.ConnectionID,
		ConnectionEpoch: 42, ServerNodeID: "server-local",
	}
	runtime := &ServerRuntime{AgentConnectionLeases: controller}
	request := relay.CloseAgentConnectionRequest{
		AgentID: session.AgentID, ConnectionID: session.ConnectionID,
		ConnectionEpoch: 42, RequestedByNodeID: "node-a",
	}

	authCtx := newAuthenticatedRelayControlContext(ctx, t, "node-a", 7, created.Secret)
	interceptor := relay.NewServerNodeUnaryInterceptor(credentialsService, db.Nodes())
	invoke := func(request relay.CloseAgentConnectionRequest) error {
		_, err := interceptor(authCtx, request, &grpc.UnaryServerInfo{FullMethod: "/tunnelmesh.relay.v1.Relay/CloseAgentConnection"}, func(ctx context.Context, req interface{}) (interface{}, error) {
			return nil, runtime.closeAgentConnection(ctx, req.(relay.CloseAgentConnectionRequest))
		})
		return err
	}

	mismatched := request
	mismatched.RequestedByNodeID = "node-b"
	if err := invoke(mismatched); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("mismatched caller error = %v, code = %v, want PermissionDenied", err, status.Code(err))
	}
	stale := request
	stale.ConnectionEpoch = 41
	if err := invoke(stale); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("stale lease error = %v, code = %v, want FailedPrecondition", err, status.Code(err))
	}
	select {
	case <-transport.closed:
		t.Fatal("denied or stale close request closed the transport")
	default:
	}
	if err := invoke(request); err != nil {
		t.Fatalf("exact close error = %v", err)
	}
	select {
	case <-transport.closed:
	default:
		t.Fatal("exact close request did not close the transport")
	}
}

func newAuthenticatedRelayControlContext(ctx context.Context, t *testing.T, nodeID string, epoch int64, token string) context.Context {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: nodeID},
		DNSNames: []string{nodeID}, NotBefore: time.Now().Add(-time.Minute),
		NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	ctx = metadata.NewIncomingContext(ctx, metadata.Pairs(
		"authorization", "Bearer "+token,
		"x-tunnelmesh-node-id", nodeID,
		"x-tunnelmesh-node-epoch", strconv.FormatInt(epoch, 10),
	))
	return peer.NewContext(ctx, &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{
		Version: tls.VersionTLS12, PeerCertificates: []*x509.Certificate{cert},
		VerifiedChains: [][]*x509.Certificate{{cert}},
	}}})
}
