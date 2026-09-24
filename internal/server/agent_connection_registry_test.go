package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/tunnelmesh/tunnelmesh/internal/registry"
	"github.com/tunnelmesh/tunnelmesh/internal/relay"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func newAgentConnectionLeaseFixture(t *testing.T) (*AgentSessionManager, *AgentRelayTransport, *AgentSession, *AgentConnectionLeaseController) {
	t.Helper()
	manager := NewAgentSessionManager(AgentSessionConfig{})
	localRelay := NewAgentRelayTransport(manager, AgentRelayWindowConfig{})
	session, err := manager.Register(context.Background(), AgentRegistration{
		AgentID: "agent-lease", NodeID: "node-agent", Epoch: 12,
		InstanceID: "instance-a", ConnectionID: "conn-a", ConnectionEpoch: 12,
	}, newFakeTransport())
	if err != nil {
		t.Fatal(err)
	}
	controller := NewAgentConnectionLeaseController(nil, manager, localRelay, "server-local", "", time.Minute)
	return manager, localRelay, session, controller
}

func TestAgentConnectionLeaseControllerRegistersRenewsAndReleases(t *testing.T) {
	db, err := storage.Open(context.Background(), storage.DriverSQLite, "file:agent-lease-lifecycle?mode=memory&cache=shared", true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := registry.NewDatabaseRegistry(db)
	manager := NewAgentSessionManager(AgentSessionConfig{})
	localRelay := NewAgentRelayTransport(manager, AgentRelayWindowConfig{})
	session, err := manager.Register(context.Background(), AgentRegistration{
		AgentID: "agent-lease", NodeID: "node-agent", Epoch: 12,
		InstanceID: "instance-a", ConnectionID: "conn-a", ConnectionEpoch: 12,
	}, newFakeTransport())
	if err != nil {
		t.Fatal(err)
	}
	controller := NewAgentConnectionLeaseController(reg, manager, localRelay, "server-local", "127.0.0.1:9443", time.Minute)

	owner, err := controller.Register(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	if owner.AgentID != session.AgentID || owner.ConnectionID != session.ConnectionID || owner.ConnectionEpoch <= 0 || owner.ServerNodeID != "server-local" || owner.Address != "127.0.0.1:9443" {
		t.Fatalf("owner = %#v", owner)
	}
	active, err := db.Leases().ListActiveByAgent(context.Background(), session.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].ServerNodeID != "server-local" {
		t.Fatalf("active leases = %#v", active)
	}

	if err := controller.Heartbeat(context.Background(), owner, session); err != nil {
		t.Fatal(err)
	}
	if err := controller.Release(context.Background(), owner); err != nil {
		t.Fatal(err)
	}
	remaining, err := db.Leases().ListActiveByAgent(context.Background(), session.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("remaining leases = %#v", remaining)
	}
}

func TestAgentConnectionLeaseControllerHeartbeatUpdatesActiveStreams(t *testing.T) {
	_, localRelay, session, controller := newAgentConnectionLeaseFixture(t)
	controller.reg = &recordingConnectionRegistry{owner: registry.NodeOwner{
		AgentID: session.AgentID, ConnectionID: session.ConnectionID, ConnectionEpoch: session.ConnectionEpoch, ServerNodeID: "server-local",
	}}
	owner := registry.NodeOwner{AgentID: session.AgentID, ConnectionID: session.ConnectionID, ConnectionEpoch: session.ConnectionEpoch}

	if err := controller.Heartbeat(context.Background(), owner, session); err != nil {
		t.Fatal(err)
	}
	if int(controller.reg.(*recordingConnectionRegistry).stats.ActiveStreams) != localRelay.ActiveStreams(session.AgentID, session.ConnectionID) {
		t.Fatalf("active streams = %d", controller.reg.(*recordingConnectionRegistry).stats.ActiveStreams)
	}
}

func TestAgentConnectionLeaseControllerReleaseIsFenced(t *testing.T) {
	_, _, session, controller := newAgentConnectionLeaseFixture(t)
	controller.reg = &recordingConnectionRegistry{releaseErr: registry.ErrFencing}
	err := controller.Release(context.Background(), registry.NodeOwner{AgentID: session.AgentID, ConnectionID: session.ConnectionID, ConnectionEpoch: session.ConnectionEpoch - 1})
	if !errors.Is(err, registry.ErrFencing) {
		t.Fatalf("Release() error = %v, want %v", err, registry.ErrFencing)
	}
}

func TestAgentConnectionLeaseControllerCloseConnectionUsesLeaseEpoch(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	transport := newFakeTransport()
	session, err := manager.Register(context.Background(), AgentRegistration{
		AgentID: "agent-close-lease", NodeID: "node-agent", Epoch: 12,
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

	staleErr := controller.CloseConnection(context.Background(), session.AgentID, session.ConnectionID, 41)
	if !errors.Is(staleErr, ErrEpoch) {
		t.Fatalf("CloseConnection(stale) error = %v, want %v", staleErr, ErrEpoch)
	}
	select {
	case <-transport.closed:
		t.Fatal("stale lease epoch closed the live transport")
	default:
	}

	if err := controller.CloseConnection(context.Background(), session.AgentID, session.ConnectionID, 42); err != nil {
		t.Fatalf("CloseConnection() error = %v", err)
	}
	select {
	case <-transport.closed:
	default:
		t.Fatal("exact lease epoch did not close the transport")
	}
}

func TestRuntimeCloseAgentConnectionRequiresAuthenticatedServerNode(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	transport := newFakeTransport()
	session, err := manager.Register(context.Background(), AgentRegistration{
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

	err = runtime.closeAgentConnection(context.Background(), relay.CloseAgentConnectionRequest{
		AgentID: session.AgentID, ConnectionID: session.ConnectionID,
		ConnectionEpoch: 42, RequestedByNodeID: "node-b",
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("closeAgentConnection() error = %v, code = %v, want PermissionDenied", err, status.Code(err))
	}
	select {
	case <-transport.closed:
		t.Fatal("unauthenticated close request closed the transport")
	default:
	}
}

type recordingConnectionRegistry struct {
	owner       registry.NodeOwner
	stats       registry.NodeOwner
	registerErr error
	releaseErr  error
}

func (r *recordingConnectionRegistry) Register(context.Context, registry.NodeRegistration) (registry.NodeOwner, error) {
	if r.registerErr != nil {
		return registry.NodeOwner{}, r.registerErr
	}
	return r.owner, nil
}
func (r *recordingConnectionRegistry) KeepAlive(context.Context, registry.NodeOwner, time.Duration) (registry.NodeOwner, error) {
	return r.owner, nil
}
func (r *recordingConnectionRegistry) UpdateConnectionStats(_ context.Context, owner registry.NodeOwner) error {
	r.stats = owner
	return nil
}
func (r *recordingConnectionRegistry) ResolveAgent(context.Context, string) (registry.NodeOwner, error) {
	return r.owner, nil
}
func (r *recordingConnectionRegistry) ListAgentConnections(context.Context, string) ([]registry.NodeOwner, error) {
	return []registry.NodeOwner{r.owner}, nil
}
func (r *recordingConnectionRegistry) Watch(context.Context, string) (<-chan registry.RegistryEvent, error) {
	return nil, nil
}
func (r *recordingConnectionRegistry) Revoke(context.Context, registry.NodeOwner) error {
	return r.releaseErr
}
func (r *recordingConnectionRegistry) Close() error { return nil }
