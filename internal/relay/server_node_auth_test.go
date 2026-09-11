package relay

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestAuthenticatedGRPCRelayValidMTLSAndServerNodeToken(t *testing.T) {
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, "file:relay-auth-valid?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authService := auth.NewAuthService(db)
	owner, err := authService.CreateUser(ctx, "relay-owner", "relay-password", "user")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Nodes().Create(ctx, storage.ServerNode{ID: "node-a", Epoch: 7}); err != nil {
		t.Fatal(err)
	}
	credentialsService := auth.NewCredentialService(db)
	defer credentialsService.Close()
	created, err := credentialsService.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeServerNode, OwnerUserID: owner.ID})
	if err != nil {
		t.Fatal(err)
	}

	_, serverTLS, clientTLS := testRelayCertificates(t, "node-a")
	var handlerCalls atomic.Int32
	var principal ServerNodePrincipal
	var targetRequest StreamRequest
	server := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(serverTLS)),
		grpc.ChainStreamInterceptor(NewServerNodeStreamInterceptor(credentialsService, db.Nodes())),
	)
	RegisterRelayServer(server, NewRelayServer(func(ctx context.Context, request StreamRequest) (io.ReadWriteCloser, error) {
		handlerCalls.Add(1)
		principal, _ = ServerNodePrincipalFromContext(ctx)
		targetRequest = request
		return newEchoConn(), nil
	}))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go server.Serve(ln)
	defer server.Stop()

	client, err := DialAuthenticatedGRPCNode(ctx, ln.Addr().String(), "node-a", 7, created.Secret, clientTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	stream, err := client.OpenStream(ctx, StreamRequest{NodeID: "target-node", AgentID: "agent-a", CaseInsensitiveAgentID: true, Epoch: 7, StreamID: 1, Protocol: "tcp", TargetHost: "127.0.0.1", TargetPort: 80})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if _, err := stream.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(stream, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "hello" {
		t.Fatalf("stream data = %q, want hello", buf)
	}
	if handlerCalls.Load() != 1 {
		t.Fatalf("handler calls = %d, want 1", handlerCalls.Load())
	}
	if principal.NodeID != "node-a" || principal.Epoch != 7 || targetRequest.NodeID != "target-node" {
		t.Fatalf("principal=%+v target=%+v, caller and target identities were not separated", principal, targetRequest)
	}
	if !targetRequest.CaseInsensitiveAgentID {
		t.Fatalf("target request = %+v, dynamic Agent ID mode was not propagated", targetRequest)
	}
}

func TestAuthenticatedGRPCRelayMultiNodeTokenSupportsAllowedNodes(t *testing.T) {
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, "file:relay-auth-fleet?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authService := auth.NewAuthService(db)
	owner, err := authService.CreateUser(ctx, "relay-fleet-owner", "relay-password", "user")
	if err != nil {
		t.Fatal(err)
	}
	for _, nodeID := range []string{"node-a", "node-b", "node-c"} {
		if err := db.Nodes().Create(ctx, storage.ServerNode{ID: nodeID, Epoch: 7}); err != nil {
			t.Fatal(err)
		}
	}
	credentialsService := auth.NewCredentialService(db)
	defer credentialsService.Close()
	created, err := credentialsService.Create(ctx, auth.CreateTokenInput{
		Type: storage.TokenTypeServerNode, OwnerUserID: owner.ID,
		Scope: auth.TokenScope{ServerNodeIDs: []string{"node-a", "node-b"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, serverTLS, clients := testRelayCertificatesForNodes(t, "node-a", "node-b", "node-c")
	var handlerCalls atomic.Int32
	var principalsMu sync.Mutex
	principals := make(map[string]ServerNodePrincipal)
	server := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(serverTLS)),
		grpc.ChainStreamInterceptor(NewServerNodeStreamInterceptor(credentialsService, db.Nodes())),
	)
	RegisterRelayServer(server, NewRelayServer(func(ctx context.Context, request StreamRequest) (io.ReadWriteCloser, error) {
		handlerCalls.Add(1)
		principal, _ := ServerNodePrincipalFromContext(ctx)
		principalsMu.Lock()
		principals[principal.NodeID] = principal
		principalsMu.Unlock()
		return newEchoConn(), nil
	}))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go server.Serve(ln)
	defer server.Stop()

	for _, nodeID := range []string{"node-a", "node-b"} {
		client, err := DialAuthenticatedGRPCNode(ctx, ln.Addr().String(), nodeID, 7, created.Secret, clients[nodeID])
		if err != nil {
			t.Fatalf("dial %s: %v", nodeID, err)
		}
		stream, err := client.OpenStream(ctx, StreamRequest{NodeID: "target-node", AgentID: "agent-a", Epoch: 7, StreamID: 1, Protocol: "tcp", TargetHost: "127.0.0.1", TargetPort: 80})
		if err != nil {
			t.Fatalf("open stream from %s: %v", nodeID, err)
		}
		if _, err := stream.Write([]byte("hello")); err != nil {
			t.Fatalf("write from %s: %v", nodeID, err)
		}
		buf := make([]byte, 5)
		if _, err := io.ReadFull(stream, buf); err != nil || string(buf) != "hello" {
			t.Fatalf("read from %s: %q err=%v", nodeID, buf, err)
		}
		if err := stream.Close(); err != nil {
			t.Fatalf("close stream from %s: %v", nodeID, err)
		}
		if err := client.Close(); err != nil {
			t.Fatalf("close client %s: %v", nodeID, err)
		}
	}

	rejected, err := DialAuthenticatedGRPCNode(ctx, ln.Addr().String(), "node-c", 7, created.Secret, clients["node-c"])
	if err != nil {
		t.Fatal(err)
	}
	defer rejected.Close()
	stream, err := rejected.OpenStream(ctx, StreamRequest{NodeID: "target-node", AgentID: "agent-a", Epoch: 7, StreamID: 1, Protocol: "tcp", TargetHost: "127.0.0.1", TargetPort: 80})
	if err == nil {
		defer stream.Close()
		_, err = stream.Read(make([]byte, 1))
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("OpenStream from unlisted node error = %v, code=%v, want PermissionDenied", err, status.Code(err))
	}

	principalsMu.Lock()
	defer principalsMu.Unlock()
	if len(principals) != 2 || principals["node-a"].NodeID != "node-a" || principals["node-b"].NodeID != "node-b" {
		t.Fatalf("principals = %+v, want node-a and node-b", principals)
	}
	if handlerCalls.Load() != 2 {
		t.Fatalf("handler calls = %d, want 2", handlerCalls.Load())
	}
}

func TestRelayCloseAgentConnectionRequiresServerNodeAuth(t *testing.T) {
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, "file:relay-close-unauth?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authService := auth.NewAuthService(db)
	owner, err := authService.CreateUser(ctx, "relay-close-unauth-owner", "relay-password", "user")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Nodes().Create(ctx, storage.ServerNode{ID: "node-a", Epoch: 7}); err != nil {
		t.Fatal(err)
	}
	credentialsService := auth.NewCredentialService(db)
	defer credentialsService.Close()
	if _, err := credentialsService.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeServerNode, OwnerUserID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	_, serverTLS, clientTLS := testRelayCertificates(t, "node-a")
	var handlerCalls atomic.Int32
	server := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(serverTLS)),
		grpc.ChainUnaryInterceptor(NewServerNodeUnaryInterceptor(credentialsService, db.Nodes())),
	)
	RegisterRelayServer(server, NewRelayServerWithClose(func(context.Context, StreamRequest) (io.ReadWriteCloser, error) {
		return nil, ErrNodeDisconnected
	}, func(context.Context, CloseAgentConnectionRequest) error {
		handlerCalls.Add(1)
		return nil
	}))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go server.Serve(ln)
	defer server.Stop()

	client, err := DialGRPCNode(ctx, ln.Addr().String(), clientTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	err = client.CloseAgentConnection(ctx, CloseAgentConnectionRequest{AgentID: "agent-a", ConnectionID: "conn-a", ConnectionEpoch: 9, RequestedByNodeID: "node-b"})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("CloseAgentConnection() error = %v, code=%v, want Unauthenticated", err, status.Code(err))
	}
	if handlerCalls.Load() != 0 {
		t.Fatalf("handler calls = %d, want 0", handlerCalls.Load())
	}
}

func TestRelayCloseClientConnectionAuthenticatesAndFencesEpoch(t *testing.T) {
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, "file:relay-close-client-auth?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authService := auth.NewAuthService(db)
	owner, err := authService.CreateUser(ctx, "relay-close-client-owner", "relay-password", "user")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Nodes().Create(ctx, storage.ServerNode{ID: "node-a", Epoch: 7}); err != nil {
		t.Fatal(err)
	}
	credentialsService := auth.NewCredentialService(db)
	defer credentialsService.Close()
	created, err := credentialsService.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeServerNode, OwnerUserID: owner.ID})
	if err != nil {
		t.Fatal(err)
	}
	_, serverTLS, clientTLS := testRelayCertificates(t, "node-a")
	var requests []CloseClientConnectionRequest
	var requestMu sync.Mutex
	server := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(serverTLS)),
		grpc.ChainUnaryInterceptor(NewServerNodeUnaryInterceptor(credentialsService, db.Nodes())),
	)
	RegisterRelayServer(server, NewRelayServerWithControls(nil, func(context.Context, StreamRequest) (io.ReadWriteCloser, error) {
		return nil, ErrNodeDisconnected
	}, nil, func(_ context.Context, request CloseClientConnectionRequest) error {
		requestMu.Lock()
		requests = append(requests, request)
		requestMu.Unlock()
		if request.ConnectionEpoch != 9 {
			return status.Error(codes.FailedPrecondition, "relay client connection epoch is stale")
		}
		return nil
	}))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go server.Serve(ln)
	defer server.Stop()

	unauthenticated, err := DialGRPCNode(ctx, ln.Addr().String(), clientTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer unauthenticated.Close()
	valid := CloseClientConnectionRequest{ConnectionID: "client_connection_1", ConnectionEpoch: 9, RequestedByNodeID: "node-b"}
	if err := unauthenticated.CloseClientConnection(ctx, valid); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauthenticated CloseClientConnection() error = %v, code = %v", err, status.Code(err))
	}

	client, err := DialAuthenticatedGRPCNode(ctx, ln.Addr().String(), "node-a", 7, created.Secret, clientTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.CloseClientConnection(ctx, valid); err != nil {
		t.Fatalf("CloseClientConnection(valid) error = %v", err)
	}
	stale := valid
	stale.ConnectionEpoch = 8
	if err := client.CloseClientConnection(ctx, stale); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("CloseClientConnection(stale) error = %v, code = %v, want FailedPrecondition", err, status.Code(err))
	}
	requestMu.Lock()
	defer requestMu.Unlock()
	if len(requests) != 2 || requests[0] != valid || requests[1] != stale {
		t.Fatalf("requests = %#v", requests)
	}

	legacyServer := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(serverTLS)),
		grpc.ChainUnaryInterceptor(NewServerNodeUnaryInterceptor(credentialsService, db.Nodes())),
	)
	RegisterRelayServer(legacyServer, NewRelayServerWithOpenResultAndClose(nil, nil, nil))
	legacyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer legacyLn.Close()
	go legacyServer.Serve(legacyLn)
	defer legacyServer.Stop()
	legacyClient, err := DialAuthenticatedGRPCNode(ctx, legacyLn.Addr().String(), "node-a", 7, created.Secret, clientTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer legacyClient.Close()
	if err := legacyClient.CloseClientConnection(ctx, valid); status.Code(err) != codes.Unimplemented {
		t.Fatalf("legacy CloseClientConnection() error = %v, code = %v, want Unimplemented", err, status.Code(err))
	}
}

func TestRelayCloseAgentConnectionUsesExactEpoch(t *testing.T) {
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, "file:relay-close-epoch?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authService := auth.NewAuthService(db)
	owner, err := authService.CreateUser(ctx, "relay-close-epoch-owner", "relay-password", "user")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Nodes().Create(ctx, storage.ServerNode{ID: "node-a", Epoch: 7}); err != nil {
		t.Fatal(err)
	}
	credentialsService := auth.NewCredentialService(db)
	defer credentialsService.Close()
	created, err := credentialsService.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeServerNode, OwnerUserID: owner.ID})
	if err != nil {
		t.Fatal(err)
	}
	_, serverTLS, clientTLS := testRelayCertificates(t, "node-a")
	var requests []CloseAgentConnectionRequest
	var requestMu sync.Mutex
	server := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(serverTLS)),
		grpc.ChainUnaryInterceptor(NewServerNodeUnaryInterceptor(credentialsService, db.Nodes())),
	)
	RegisterRelayServer(server, NewRelayServerWithClose(func(context.Context, StreamRequest) (io.ReadWriteCloser, error) {
		return nil, ErrNodeDisconnected
	}, func(_ context.Context, request CloseAgentConnectionRequest) error {
		requestMu.Lock()
		requests = append(requests, request)
		requestMu.Unlock()
		if request.ConnectionEpoch != 9 {
			return status.Error(codes.FailedPrecondition, "relay agent connection epoch is stale")
		}
		return nil
	}))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go server.Serve(ln)
	defer server.Stop()

	client, err := DialAuthenticatedGRPCNode(ctx, ln.Addr().String(), "node-a", 7, created.Secret, clientTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	valid := CloseAgentConnectionRequest{AgentID: "agent-a", ConnectionID: "conn-a", ConnectionEpoch: 9, RequestedByNodeID: "node-b"}
	if err := client.CloseAgentConnection(ctx, valid); err != nil {
		t.Fatalf("CloseAgentConnection(valid) error = %v", err)
	}
	stale := valid
	stale.ConnectionEpoch = 8
	if err := client.CloseAgentConnection(ctx, stale); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("CloseAgentConnection(stale) error = %v, code=%v, want FailedPrecondition", err, status.Code(err))
	}
	requestMu.Lock()
	defer requestMu.Unlock()
	if len(requests) != 2 || requests[0] != valid || requests[1] != stale {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestGRPCNodeTransportCloseAgentConnectionPropagatesFailure(t *testing.T) {
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, "file:relay-close-failure?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authService := auth.NewAuthService(db)
	owner, err := authService.CreateUser(ctx, "relay-close-failure-owner", "relay-password", "user")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Nodes().Create(ctx, storage.ServerNode{ID: "node-a", Epoch: 7}); err != nil {
		t.Fatal(err)
	}
	credentialsService := auth.NewCredentialService(db)
	defer credentialsService.Close()
	created, err := credentialsService.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeServerNode, OwnerUserID: owner.ID})
	if err != nil {
		t.Fatal(err)
	}
	_, serverTLS, clientTLS := testRelayCertificates(t, "node-a")
	server := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(serverTLS)),
		grpc.ChainUnaryInterceptor(NewServerNodeUnaryInterceptor(credentialsService, db.Nodes())),
	)
	RegisterRelayServer(server, NewRelayServerWithClose(func(context.Context, StreamRequest) (io.ReadWriteCloser, error) {
		return nil, ErrNodeDisconnected
	}, func(context.Context, CloseAgentConnectionRequest) error {
		return status.Error(codes.Internal, "relay close failed")
	}))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go server.Serve(ln)
	defer server.Stop()

	client, err := DialAuthenticatedGRPCNode(ctx, ln.Addr().String(), "node-a", 7, created.Secret, clientTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	err = client.CloseAgentConnection(ctx, CloseAgentConnectionRequest{AgentID: "agent-a", ConnectionID: "conn-a", ConnectionEpoch: 9, RequestedByNodeID: "node-b"})
	if status.Code(err) != codes.Internal {
		t.Fatalf("CloseAgentConnection() error = %v, code=%v, want Internal", err, status.Code(err))
	}
}

func TestAuthenticatedGRPCRelayRejectsStaleEpochBeforeHandler(t *testing.T) {
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, "file:relay-auth-stale?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authService := auth.NewAuthService(db)
	owner, err := authService.CreateUser(ctx, "relay-stale-owner", "relay-password", "user")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Nodes().Create(ctx, storage.ServerNode{ID: "node-a", Epoch: 7}); err != nil {
		t.Fatal(err)
	}
	credentialsService := auth.NewCredentialService(db)
	defer credentialsService.Close()
	created, err := credentialsService.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeServerNode, OwnerUserID: owner.ID})
	if err != nil {
		t.Fatal(err)
	}
	_, serverTLS, clientTLS := testRelayCertificates(t, "node-a")
	var handlerCalls atomic.Int32
	server := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(serverTLS)),
		grpc.ChainStreamInterceptor(NewServerNodeStreamInterceptor(credentialsService, db.Nodes())),
	)
	RegisterRelayServer(server, NewRelayServer(func(context.Context, StreamRequest) (io.ReadWriteCloser, error) {
		handlerCalls.Add(1)
		return newEchoConn(), nil
	}))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go server.Serve(ln)
	defer server.Stop()

	client, err := DialAuthenticatedGRPCNode(ctx, ln.Addr().String(), "node-a", 6, created.Secret, clientTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	stream, err := client.OpenStream(ctx, StreamRequest{NodeID: "target-node", Epoch: 6})
	if err == nil {
		defer stream.Close()
		_, err = stream.Read(make([]byte, 1))
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("OpenStream error = %v, code=%v, want FailedPrecondition", err, status.Code(err))
	}
	if handlerCalls.Load() != 0 {
		t.Fatalf("handler calls = %d, want 0", handlerCalls.Load())
	}
}

func TestAuthenticatedGRPCRelayRejectsSANAndTokenFailures(t *testing.T) {
	tests := []struct {
		name      string
		certNode  string
		tokenType storage.TokenType
		revoke    bool
		expired   bool
		wantCode  codes.Code
	}{
		{name: "certificate SAN mismatch", certNode: "node-b", tokenType: storage.TokenTypeServerNode, wantCode: codes.PermissionDenied},
		{name: "wrong token type", certNode: "node-a", tokenType: storage.TokenTypeClient, wantCode: codes.Unauthenticated},
		{name: "revoked token", certNode: "node-a", tokenType: storage.TokenTypeServerNode, revoke: true, wantCode: codes.Unauthenticated},
		{name: "expired node", certNode: "node-a", tokenType: storage.TokenTypeServerNode, expired: true, wantCode: codes.FailedPrecondition},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			db, err := storage.OpenSQLite(ctx, "file:relay-auth-failure-"+strings.ReplaceAll(tt.name, " ", "-")+"?mode=memory&cache=shared")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			authService := auth.NewAuthService(db)
			owner, err := authService.CreateUser(ctx, "owner-"+strings.ReplaceAll(tt.name, " ", "-"), "password", "user")
			if err != nil {
				t.Fatal(err)
			}
			node := storage.ServerNode{ID: "node-a", Epoch: 7, Enabled: true}
			if err := db.Nodes().Create(ctx, node); err != nil {
				t.Fatal(err)
			}
			credentialsService := auth.NewCredentialService(db)
			defer credentialsService.Close()
			input := auth.CreateTokenInput{Type: tt.tokenType, OwnerUserID: owner.ID}
			if tt.tokenType == storage.TokenTypeServerNode {
				input.Scope = auth.TokenScope{}
			}
			created, err := credentialsService.Create(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			if tt.expired {
				expired := time.Now().Add(-time.Minute)
				node.ExpiresAt = &expired
				if err := db.Nodes().Update(ctx, node); err != nil {
					t.Fatal(err)
				}
			}
			if tt.revoke {
				if err := credentialsService.Revoke(ctx, created.TokenID); err != nil {
					t.Fatal(err)
				}
			}
			_, serverTLS, clientTLS := testRelayCertificates(t, tt.certNode)
			var handlerCalls atomic.Int32
			grpcServer := grpc.NewServer(grpc.Creds(credentials.NewTLS(serverTLS)), grpc.ChainStreamInterceptor(NewServerNodeStreamInterceptor(credentialsService, db.Nodes())))
			RegisterRelayServer(grpcServer, NewRelayServer(func(context.Context, StreamRequest) (io.ReadWriteCloser, error) {
				handlerCalls.Add(1)
				return newEchoConn(), nil
			}))
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			go grpcServer.Serve(ln)
			defer grpcServer.Stop()
			client, err := DialAuthenticatedGRPCNode(ctx, ln.Addr().String(), "node-a", 7, created.Secret, clientTLS)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			stream, err := client.OpenStream(ctx, StreamRequest{NodeID: "target-node", Epoch: 7})
			if err == nil {
				defer stream.Close()
				if _, writeErr := stream.Write([]byte("x")); writeErr == nil {
					_, err = stream.Read(make([]byte, 1))
				} else {
					err = writeErr
				}
			}
			if status.Code(err) != tt.wantCode {
				t.Fatalf("OpenStream error = %v, code=%v, want %v", err, status.Code(err), tt.wantCode)
			}
			if handlerCalls.Load() != 0 {
				t.Fatalf("handler calls = %d, want 0", handlerCalls.Load())
			}
		})
	}
}

func TestAuthenticatedGRPCRelayRejectsDisabledAndDeletedNodes(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(storage.ServerNode) storage.ServerNode
	}{
		{name: "disabled", mutate: func(node storage.ServerNode) storage.ServerNode {
			node.Enabled = false
			return node
		}},
		{name: "deleted", mutate: func(node storage.ServerNode) storage.ServerNode {
			deletedAt := time.Now().UTC()
			node.DeletedAt = &deletedAt
			return node
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			db, err := storage.OpenSQLite(ctx, "file:relay-auth-"+strings.ReplaceAll(tt.name, " ", "-")+"?mode=memory&cache=shared")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			authService := auth.NewAuthService(db)
			owner, err := authService.CreateUser(ctx, "relay-"+strings.ReplaceAll(tt.name, " ", "-")+"-owner", "relay-password", "user")
			if err != nil {
				t.Fatal(err)
			}
			node := storage.ServerNode{ID: "node-a", Epoch: 7, Enabled: true}
			if err := db.Nodes().Create(ctx, node); err != nil {
				t.Fatal(err)
			}
			credentialsService := auth.NewCredentialService(db)
			defer credentialsService.Close()
			created, err := credentialsService.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeServerNode, OwnerUserID: owner.ID})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Nodes().Update(ctx, tt.mutate(node)); err != nil {
				t.Fatal(err)
			}
			_, serverTLS, clientTLS := testRelayCertificates(t, "node-a")
			var handlerCalls atomic.Int32
			server := grpc.NewServer(
				grpc.Creds(credentials.NewTLS(serverTLS)),
				grpc.ChainStreamInterceptor(NewServerNodeStreamInterceptor(credentialsService, db.Nodes())),
			)
			RegisterRelayServer(server, NewRelayServer(func(context.Context, StreamRequest) (io.ReadWriteCloser, error) {
				handlerCalls.Add(1)
				return newEchoConn(), nil
			}))
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			go server.Serve(ln)
			defer server.Stop()
			client, err := DialAuthenticatedGRPCNode(ctx, ln.Addr().String(), "node-a", 7, created.Secret, clientTLS)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			stream, err := client.OpenStream(ctx, StreamRequest{NodeID: "target-node", Epoch: 7})
			if err == nil {
				defer stream.Close()
				_, err = stream.Read(make([]byte, 1))
			}
			if status.Code(err) != codes.PermissionDenied {
				t.Fatalf("OpenStream error = %v, code=%v, want PermissionDenied", err, status.Code(err))
			}
			if handlerCalls.Load() != 0 {
				t.Fatalf("handler calls = %d, want 0", handlerCalls.Load())
			}
		})
	}
}

func TestAuthenticatedGRPCRelayPlaintextStillValidatesTokenNodeAndEpoch(t *testing.T) {
	tests := []struct {
		name      string
		tokenType storage.TokenType
		scope     auth.TokenScope
		epoch     int64
		disabled  bool
		wantCode  codes.Code
	}{
		{name: "valid fleet token", tokenType: storage.TokenTypeServerNode, epoch: 7, wantCode: codes.OK},
		{name: "wrong token type", tokenType: storage.TokenTypeClient, epoch: 7, wantCode: codes.Unauthenticated},
		{name: "scope excludes node", tokenType: storage.TokenTypeServerNode, scope: auth.TokenScope{ServerNodeIDs: []string{"node-b"}}, epoch: 7, wantCode: codes.PermissionDenied},
		{name: "stale epoch", tokenType: storage.TokenTypeServerNode, epoch: 6, wantCode: codes.FailedPrecondition},
		{name: "disabled node", tokenType: storage.TokenTypeServerNode, epoch: 7, disabled: true, wantCode: codes.PermissionDenied},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			db, err := storage.OpenSQLite(ctx, "file:relay-plaintext-"+strings.ReplaceAll(tt.name, " ", "-")+"?mode=memory&cache=shared")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			authService := auth.NewAuthService(db)
			owner, err := authService.CreateUser(ctx, "relay-plaintext-"+strings.ReplaceAll(tt.name, " ", "-"), "password", "user")
			if err != nil {
				t.Fatal(err)
			}
			node := storage.ServerNode{ID: "node-a", Epoch: 7, Enabled: true}
			if err := db.Nodes().Create(ctx, node); err != nil {
				t.Fatal(err)
			}
			if len(tt.scope.ServerNodeIDs) > 0 {
				for _, nodeID := range tt.scope.ServerNodeIDs {
					if err := db.Nodes().Create(ctx, storage.ServerNode{ID: nodeID, Epoch: 7, Enabled: true}); err != nil {
						t.Fatal(err)
					}
				}
			}
			credentialsService := auth.NewCredentialService(db)
			defer credentialsService.Close()
			created, err := credentialsService.Create(ctx, auth.CreateTokenInput{
				Type: tt.tokenType, OwnerUserID: owner.ID, Scope: tt.scope,
			})
			if err != nil {
				t.Fatal(err)
			}
			if tt.disabled {
				node.Enabled = false
				if err := db.Nodes().Update(ctx, node); err != nil {
					t.Fatal(err)
				}
			}

			var handlerCalls atomic.Int32
			server := grpc.NewServer(grpc.ChainStreamInterceptor(
				NewServerNodeStreamInterceptorWithMode(credentialsService, db.Nodes(), nil, false),
			))
			RegisterRelayServer(server, NewRelayServer(func(context.Context, StreamRequest) (io.ReadWriteCloser, error) {
				handlerCalls.Add(1)
				return newEchoConn(), nil
			}))
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			go server.Serve(ln)
			defer server.Stop()

			client, err := DialAuthenticatedGRPCNode(ctx, ln.Addr().String(), "node-a", tt.epoch, created.Secret, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			stream, err := client.OpenStream(ctx, StreamRequest{NodeID: "target-node", Epoch: 7})
			if tt.wantCode == codes.OK {
				if err != nil {
					t.Fatal(err)
				}
				defer stream.Close()
				if _, err := stream.Write([]byte("hello")); err != nil {
					t.Fatal(err)
				}
				buf := make([]byte, 5)
				if _, err := io.ReadFull(stream, buf); err != nil {
					t.Fatal(err)
				}
				if string(buf) != "hello" || handlerCalls.Load() != 1 {
					t.Fatalf("data=%q handlerCalls=%d, want plaintext relay echo", buf, handlerCalls.Load())
				}
				return
			}
			if err == nil {
				defer stream.Close()
				if _, writeErr := stream.Write([]byte("x")); writeErr == nil {
					_, err = stream.Read(make([]byte, 1))
				} else {
					err = writeErr
				}
			}
			if status.Code(err) != tt.wantCode {
				t.Fatalf("OpenStream error = %v, code=%v, want %v", err, status.Code(err), tt.wantCode)
			}
			if handlerCalls.Load() != 0 {
				t.Fatalf("handler calls = %d, want 0", handlerCalls.Load())
			}
		})
	}
}

func TestDialAuthenticatedGRPCNodeRejectsIncompleteTLSConfig(t *testing.T) {
	_, err := DialAuthenticatedGRPCNode(
		context.Background(), "127.0.0.1:1", "node-a", 1, "secret",
		&tls.Config{ServerName: "relay.local"},
	)
	if err == nil || !strings.Contains(err.Error(), "mTLS") {
		t.Fatalf("DialAuthenticatedGRPCNode() error = %v, want incomplete mTLS error", err)
	}
}

func TestServerNodeMetadataRejectsMissingAndDuplicateValues(t *testing.T) {
	base := metadata.Pairs(serverNodeAuthorizationMetadata, "Bearer secret", serverNodeIDMetadata, "node-a", serverNodeEpochMetadata, "7")
	if _, _, _, err := parseServerNodeMetadata(metadata.NewIncomingContext(context.Background(), base)); err != nil {
		t.Fatalf("valid metadata rejected: %v", err)
	}
	duplicate := metadata.Join(base, metadata.Pairs(serverNodeEpochMetadata, "8"))
	if _, _, _, err := parseServerNodeMetadata(metadata.NewIncomingContext(context.Background(), duplicate)); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("duplicate metadata error = %v, want Unauthenticated", err)
	}
	missing := metadata.NewIncomingContext(context.Background(), metadata.Pairs(serverNodeAuthorizationMetadata, "Bearer secret", serverNodeIDMetadata, "node-a"))
	if _, _, _, err := parseServerNodeMetadata(missing); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing metadata error = %v, want Unauthenticated", err)
	}
}

func TestServerNodeClientInterceptorInjectsCallerMetadata(t *testing.T) {
	interceptor := NewServerNodeStreamClientInterceptor("node-a", 7, "raw-secret")
	var got metadata.MD
	streamer := func(ctx context.Context, _ *grpc.StreamDesc, _ *grpc.ClientConn, _ string, _ ...grpc.CallOption) (grpc.ClientStream, error) {
		got, _ = metadata.FromOutgoingContext(ctx)
		return nil, nil
	}
	_, err := interceptor(context.Background(), &grpc.StreamDesc{}, nil, "/relay", streamer)
	if err != nil {
		t.Fatal(err)
	}
	if got.Get(serverNodeAuthorizationMetadata)[0] != "Bearer raw-secret" || got.Get(serverNodeIDMetadata)[0] != "node-a" || got.Get(serverNodeEpochMetadata)[0] != "7" {
		t.Fatalf("injected metadata = %v", got)
	}
}

func TestRelayTLSHelpersRejectMissingClientCertificateAndCA(t *testing.T) {
	if _, err := NewRelayServerTLSConfig(tls.Certificate{}, x509.NewCertPool()); err == nil {
		t.Fatal("server TLS accepted missing certificate")
	}
	if _, err := NewRelayClientTLSConfig(tls.Certificate{}, x509.NewCertPool(), "relay.local"); err == nil {
		t.Fatal("client TLS accepted missing certificate")
	}
	_, serverTLS, clientTLS := testRelayCertificates(t, "node-a")
	if _, err := NewRelayServerTLSConfig(serverTLS.Certificates[0], nil); err == nil {
		t.Fatal("server TLS accepted missing client CA")
	}
	if _, err := NewRelayClientTLSConfig(clientTLS.Certificates[0], nil, "relay.local"); err == nil {
		t.Fatal("client TLS accepted missing root CA")
	}
}

func TestCertificateSANMatchingIsExactAndDoesNotFallbackToCN(t *testing.T) {
	for _, tc := range []struct {
		name string
		cert *x509.Certificate
		node string
		want bool
	}{
		{name: "exact DNS", cert: &x509.Certificate{DNSNames: []string{"Node-A"}}, node: "node-a", want: true},
		{name: "wildcard denied", cert: &x509.Certificate{DNSNames: []string{"*.example"}}, node: "node.example", want: false},
		{name: "CN fallback denied", cert: &x509.Certificate{Subject: pkix.Name{CommonName: "node-a"}}, node: "node-a", want: false},
		{name: "URI exact", cert: &x509.Certificate{URIs: []*url.URL{{Scheme: "spiffe", Host: "tunnelmesh", Path: "/node-a"}}}, node: "spiffe://tunnelmesh/node-a", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := certificateHasExactSAN(tc.cert, tc.node); got != tc.want {
				t.Fatalf("certificateHasExactSAN() = %v, want %v", got, tc.want)
			}
		})
	}
}

func testRelayCertificates(t *testing.T, nodeID string) (*x509.CertPool, *tls.Config, *tls.Config) {
	pool, serverTLS, clients := testRelayCertificatesForNodes(t, nodeID)
	return pool, serverTLS, clients[nodeID]
}

func testRelayCertificatesForNodes(t *testing.T, nodeIDs ...string) (*x509.CertPool, *tls.Config, map[string]*tls.Config) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "TunnelMesh Test CA"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	newLeaf := func(serial int64, dns string, server bool) tls.Certificate {
		key, keyErr := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if keyErr != nil {
			t.Fatal(keyErr)
		}
		leaf := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "ignored"}, DNSNames: []string{dns}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
		if server {
			leaf.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		}
		der, certErr := x509.CreateCertificate(rand.Reader, leaf, caCert, &key.PublicKey, caKey)
		if certErr != nil {
			t.Fatal(certErr)
		}
		return tls.Certificate{Certificate: [][]byte{der, caDER}, PrivateKey: key}
	}
	serverCert := newLeaf(2, "relay.local", true)
	clients := make(map[string]*tls.Config, len(nodeIDs))
	for i, nodeID := range nodeIDs {
		clientCert := newLeaf(int64(3+i), nodeID, false)
		clients[nodeID] = &tls.Config{Certificates: []tls.Certificate{clientCert}, RootCAs: pool, ServerName: "relay.local", MinVersion: tls.VersionTLS12}
	}
	return pool, &tls.Config{Certificates: []tls.Certificate{serverCert}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool, MinVersion: tls.VersionTLS12}, clients
}

type echoConn struct {
	mu     sync.Mutex
	closed bool
	data   chan []byte
}

func newEchoConn() *echoConn { return &echoConn{data: make(chan []byte, 4)} }
func (c *echoConn) Read(p []byte) (int, error) {
	b, ok := <-c.data
	if !ok {
		return 0, io.EOF
	}
	n := copy(p, b)
	return n, nil
}
func (c *echoConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return 0, errors.New("closed")
	}
	c.data <- append([]byte(nil), p...)
	return len(p), nil
}
func (c *echoConn) Close() error {
	c.mu.Lock()
	if !c.closed {
		c.closed = true
		close(c.data)
	}
	c.mu.Unlock()
	return nil
}
