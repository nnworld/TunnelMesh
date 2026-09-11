package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/relay"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

type fakeClientConnectionRelayClient struct {
	request relay.CloseClientConnectionRequest
	err     error
	closed  bool
}

func (c *fakeClientConnectionRelayClient) CloseClientConnection(_ context.Context, request relay.CloseClientConnectionRequest) error {
	c.request = request
	return c.err
}

func (c *fakeClientConnectionRelayClient) Close() error {
	c.closed = true
	return nil
}

func TestClusterClientConnectionCloseClosesLocalLease(t *testing.T) {
	ctx := context.Background()
	db := openClientCloseTestDB(t)
	manager, transport := registerClientCloseSession(t, db, "server-local", "client_instance_1", 7)
	leases := NewClientConnectionLeaseController(db.ClientConnections(), manager, "server-local", time.Minute)
	service := NewClusterClientConnectionCloseService(db.ClientConnections(), db.Nodes(), leases, "server-local", nil)

	if err := service.Close(ctx, ClientConnectionCloseRequest{ClientInstanceID: "client-instance-1", ConnectionID: "client_instance_1", ConnectionEpoch: 7}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-transport.closed:
	default:
		t.Fatal("local close did not close the WebSocket transport")
	}
}

func TestClusterClientConnectionCloseRejectsStaleEpoch(t *testing.T) {
	ctx := context.Background()
	db := openClientCloseTestDB(t)
	manager, _ := registerClientCloseSession(t, db, "server-local", "client_instance_1", 7)
	leases := NewClientConnectionLeaseController(db.ClientConnections(), manager, "server-local", time.Minute)
	service := NewClusterClientConnectionCloseService(db.ClientConnections(), db.Nodes(), leases, "server-local", nil)

	err := service.Close(ctx, ClientConnectionCloseRequest{ClientInstanceID: "client-instance-1", ConnectionID: "client_instance_1", ConnectionEpoch: 6})
	if !errors.Is(err, ErrEpoch) {
		t.Fatalf("Close(stale) error = %v, want ErrEpoch", err)
	}
}

func TestClusterClientConnectionCloseRejectsWrongInstance(t *testing.T) {
	ctx := context.Background()
	db := openClientCloseTestDB(t)
	manager, _ := registerClientCloseSession(t, db, "server-local", "client_instance_1", 7)
	leases := NewClientConnectionLeaseController(db.ClientConnections(), manager, "server-local", time.Minute)
	service := NewClusterClientConnectionCloseService(db.ClientConnections(), db.Nodes(), leases, "server-local", nil)

	err := service.Close(ctx, ClientConnectionCloseRequest{ClientInstanceID: "client-instance-other", ConnectionID: "client_instance_1", ConnectionEpoch: 7})
	if !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("Close(wrong instance) error = %v, want ErrSessionClosed", err)
	}
}

func TestClusterClientConnectionCloseDelegatesRemoteLease(t *testing.T) {
	ctx := context.Background()
	db := openClientCloseTestDB(t)
	manager, _ := registerClientCloseSession(t, db, "server-remote", "client_instance_1", 7)
	if err := db.Nodes().Create(ctx, storage.ServerNode{ID: "server-remote", Name: "remote", Address: "10.0.0.2:9443", Epoch: 11, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	leases := NewClientConnectionLeaseController(db.ClientConnections(), manager, "server-local", time.Minute)
	client := &fakeClientConnectionRelayClient{}
	var dialedEndpoint string
	var dialedEpoch int64
	service := NewClusterClientConnectionCloseService(db.ClientConnections(), db.Nodes(), leases, "server-local", func(_ context.Context, endpoint string, epoch int64) (ClientConnectionRelayClient, error) {
		dialedEndpoint, dialedEpoch = endpoint, epoch
		return client, nil
	})

	if err := service.Close(ctx, ClientConnectionCloseRequest{ClientInstanceID: "client-instance-1", ConnectionID: "client_instance_1", ConnectionEpoch: 7}); err != nil {
		t.Fatal(err)
	}
	if dialedEndpoint != "10.0.0.2:9443" || dialedEpoch != 11 {
		t.Fatalf("dial endpoint=%q epoch=%d", dialedEndpoint, dialedEpoch)
	}
	if client.request != (relay.CloseClientConnectionRequest{ConnectionID: "client_instance_1", ConnectionEpoch: 7, RequestedByNodeID: "server-local"}) {
		t.Fatalf("relay request = %#v", client.request)
	}
	if !client.closed {
		t.Fatal("remote relay client was not closed")
	}
}

func TestClusterClientConnectionCloseKeepsLeaseWhenNodeUnavailable(t *testing.T) {
	ctx := context.Background()
	db := openClientCloseTestDB(t)
	manager, _ := registerClientCloseSession(t, db, "server-remote", "client_instance_1", 7)
	if err := db.Nodes().Create(ctx, storage.ServerNode{ID: "server-remote", Name: "remote", Address: "10.0.0.2:9443", Epoch: 11, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	leases := NewClientConnectionLeaseController(db.ClientConnections(), manager, "server-local", time.Minute)
	service := NewClusterClientConnectionCloseService(db.ClientConnections(), db.Nodes(), leases, "server-local", func(context.Context, string, int64) (ClientConnectionRelayClient, error) {
		return nil, errors.New("network unavailable")
	})

	err := service.Close(ctx, ClientConnectionCloseRequest{ClientInstanceID: "client-instance-1", ConnectionID: "client_instance_1", ConnectionEpoch: 7})
	if !errors.Is(err, ErrClientConnectionNodeUnavailable) {
		t.Fatalf("Close(unavailable) error = %v, want ErrClientConnectionNodeUnavailable", err)
	}
	if _, getErr := db.ClientConnections().Get(ctx, "client_instance_1"); getErr != nil {
		t.Fatalf("unavailable node removed durable lease: %v", getErr)
	}
}

func TestNewServerRuntimeWiresClusterClientConnectionClose(t *testing.T) {
	db, err := storage.OpenSQLite(context.Background(), "file:runtime-client-close-wiring?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	runtime, err := NewServerRuntime(db, AgentSessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if runtime.API == nil || runtime.API.clientCloser == nil {
		t.Fatal("runtime did not wire the cluster Client connection close service")
	}
	if _, ok := runtime.API.clientCloser.(*ClusterClientConnectionCloseService); !ok {
		t.Fatalf("client closer type = %T, want *ClusterClientConnectionCloseService", runtime.API.clientCloser)
	}
}

func TestRuntimeCloseClientConnectionUsesAuthenticatedCallerAndExactEpoch(t *testing.T) {
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, "file:runtime-client-close-control?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authService := auth.NewAuthService(db)
	owner, err := authService.CreateUser(ctx, "runtime-client-close-owner", "password", "user")
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

	manager, transport := registerClientCloseSession(t, db, "server-local", "client_connection_1", 42)
	runtime := &ServerRuntime{ClientConnectionLeases: NewClientConnectionLeaseController(db.ClientConnections(), manager, "server-local", time.Minute)}
	request := relay.CloseClientConnectionRequest{
		ConnectionID: "client_connection_1", ConnectionEpoch: 42, RequestedByNodeID: "node-a",
	}
	authCtx := newAuthenticatedRelayControlContext(ctx, t, "node-a", 7, created.Secret)
	interceptor := relay.NewServerNodeUnaryInterceptor(credentialsService, db.Nodes())
	invoke := func(request relay.CloseClientConnectionRequest) error {
		_, err := interceptor(authCtx, request, &grpc.UnaryServerInfo{FullMethod: "/tunnelmesh.relay.v1.Relay/CloseClientConnection"}, func(ctx context.Context, req interface{}) (interface{}, error) {
			return nil, runtime.closeClientConnection(ctx, req.(relay.CloseClientConnectionRequest))
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
		t.Fatalf("stale epoch error = %v, code = %v, want FailedPrecondition", err, status.Code(err))
	}
	select {
	case <-transport.closed:
		t.Fatal("denied or stale request closed the transport")
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

func openClientCloseTestDB(t *testing.T) *storage.DB {
	t.Helper()
	db, err := storage.OpenSQLite(context.Background(), "file:client-close-"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func registerClientCloseSession(t *testing.T, db *storage.DB, serverNodeID, connectionID string, epoch int64) (*ClientSessionManager, *fakeTransport) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	instance := storage.ClientInstance{
		ID: "client-instance-1", OwnerUserID: "owner-1", InstanceID: "client-local-1",
		Metadata: "{}", Capabilities: `["client_metadata.v1"]`, ReportedAt: now, LastSeenAt: now,
		ExpiresAt: ptrTime(now.Add(time.Minute)), UpdatedAt: now,
	}
	if _, err := db.ClientInstances().Upsert(ctx, instance); err != nil {
		t.Fatal(err)
	}
	manager := NewClientSessionManager()
	transport := newFakeTransport()
	record := ClientSessionRecord{
		ConnectionID: connectionID, ClientInstanceID: instance.ID, TokenID: "token-1", OwnerUserID: "owner-1",
		ServerNodeID: serverNodeID, ConnectionEpoch: epoch, StartedAt: now, MetadataEnabled: true,
	}
	manager.Register(record, transport)
	if _, err := db.ClientConnections().Register(ctx, storage.ClientConnectionLease{
		ConnectionID: connectionID, ClientInstanceID: instance.ID, TokenID: "token-1", OwnerUserID: "owner-1",
		ServerNodeID: serverNodeID, ConnectionEpoch: epoch, ActiveStreams: 1, HealthScore: 100,
		AcquiredAt: now, ExpiresAt: now.Add(time.Minute), UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	return manager, transport
}
