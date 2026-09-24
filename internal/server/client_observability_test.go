package server

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// errClientLeaseWrite stands in for any durable lease failure, including the
// sql.ErrNoRows a truncated connection_epoch used to produce.
var errClientLeaseWrite = errors.New("client lease write failed")

func newClientObservabilityFixture() (*ClientObservabilityService, *fakeClientInstanceRepo, *fakeClientConnectionRepo, *ClientSessionManager) {
	instances := &fakeClientInstanceRepo{nextID: "client-instance-1"}
	connections := &fakeClientConnectionRepo{}
	manager := NewClientSessionManager()
	leases := NewClientConnectionLeaseController(connections, manager, "server-1", 90*time.Second)
	manager.Register(ClientSessionRecord{
		ConnectionID: "connection-1", TokenID: "token-1", OwnerUserID: "owner-1",
		ServerNodeID: "server-1", ConnectionEpoch: 3, StartedAt: time.Now().UTC(), MetadataEnabled: true,
	}, newFakeTransport())
	return NewClientObservabilityService(instances, leases, manager, "server-1", 5*time.Minute), instances, connections, manager
}

func TestClientObservabilityUsesMetadataTTLDefault(t *testing.T) {
	instances := &fakeClientInstanceRepo{}
	connections := &fakeClientConnectionRepo{}
	manager := NewClientSessionManager()
	leases := NewClientConnectionLeaseController(connections, manager, "server-1", 90*time.Second)
	service := NewClientObservabilityService(instances, leases, manager, "server-1", 0)
	if service.metadataTTL != DefaultClientMetadataTTL {
		t.Fatalf("metadataTTL = %v, want %v", service.metadataTTL, DefaultClientMetadataTTL)
	}
}

func TestClientObservabilityHelloPersistsInstanceAndLease(t *testing.T) {
	service, instances, connections, _ := newClientObservabilityFixture()
	principal := ClientSessionPrincipal{
		ConnectionID: "connection-1",
		Identity: auth.TokenIdentity{
			TokenID: "token-1", OwnerUserID: "owner-1", Type: storage.TokenTypeClient,
		}, MetadataEnabled: true,
	}
	payload := protocol.ClientMetadataPayload{
		InstanceID: "client-0123456789abcdef", Revision: 1, ReportedAt: time.Now().UTC(),
		Version: "v1.2.3", Capabilities: []string{"client_metadata.v1"},
	}
	ack, err := service.Hello(context.Background(), principal, payload)
	if err != nil {
		t.Fatal(err)
	}
	if !ack.Accepted || ack.ClientInstanceID != "client-instance-1" || ack.InstanceID != payload.InstanceID || ack.ConnectionID != "connection-1" || ack.Revision != 1 {
		t.Fatalf("ack = %#v", ack)
	}
	if len(instances.upserts) != 1 || instances.upserts[0].OwnerUserID != "owner-1" || instances.upserts[0].InstanceID != payload.InstanceID {
		t.Fatalf("upserts = %#v", instances.upserts)
	}
	if len(connections.registered) != 1 || connections.registered[0].ClientInstanceID != "client-instance-1" {
		t.Fatalf("leases = %#v", connections.registered)
	}
}

func TestClientObservabilityRejectsNonClientToken(t *testing.T) {
	service, _, connections, _ := newClientObservabilityFixture()
	principal := ClientSessionPrincipal{
		ConnectionID: "connection-1",
		Identity: auth.TokenIdentity{
			TokenID: "token-1", OwnerUserID: "owner-1", Type: storage.TokenTypeAgent,
		}, MetadataEnabled: true,
	}
	ack, err := service.Hello(context.Background(), principal, protocol.ClientMetadataPayload{
		InstanceID: "client-0123456789abcdef", Revision: 1, ReportedAt: time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("expected non-client token to fail")
	}
	if ack.Accepted {
		t.Fatalf("ack = %#v, want rejected", ack)
	}
	if len(connections.registered) != 0 {
		t.Fatalf("leases = %#v", connections.registered)
	}
}

func TestClientObservabilityRejectsCommonSensitiveMetadataNames(t *testing.T) {
	service, _, _, _ := newClientObservabilityFixture()
	principal := ClientSessionPrincipal{
		ConnectionID: "connection-1",
		Identity: auth.TokenIdentity{
			TokenID: "token-1", OwnerUserID: "owner-1", Type: storage.TokenTypeClient,
		}, MetadataEnabled: true,
	}
	for _, name := range []string{"api_key", "credential", "authorization"} {
		payload := protocol.ClientMetadataPayload{
			InstanceID: "client-0123456789abcdef", Revision: 1, ReportedAt: time.Now().UTC(),
			Items: []protocol.ClientMetadataItem{{Name: name, Value: "sensitive"}},
		}
		if ack, err := service.Hello(context.Background(), principal, payload); err == nil || ack.Accepted {
			t.Fatalf("metadata name %q was accepted: ack=%#v err=%v", name, ack, err)
		}
	}
}

func TestClientObservabilityUpdateRequiresHello(t *testing.T) {
	service, instances, connections, _ := newClientObservabilityFixture()
	principal := ClientSessionPrincipal{
		ConnectionID: "connection-1",
		Identity: auth.TokenIdentity{
			TokenID: "token-1", OwnerUserID: "owner-1", Type: storage.TokenTypeClient,
		}, MetadataEnabled: true,
	}
	payload := protocol.ClientMetadataPayload{
		InstanceID: "client-0123456789abcdef", Revision: 1, ReportedAt: time.Now().UTC(),
	}

	ack, err := service.Update(context.Background(), principal, payload)
	if err == nil || ack.Accepted {
		t.Fatalf("update before hello was accepted: ack=%#v err=%v", ack, err)
	}
	if len(instances.upserts) != 0 {
		t.Fatalf("update before hello persisted instances: %#v", instances.upserts)
	}
	if len(connections.registered) != 0 {
		t.Fatalf("update before hello registered leases: %#v", connections.registered)
	}
}

func TestClientObservabilityRejectsUnboundedOrDuplicateMetadataItems(t *testing.T) {
	service, instances, _, _ := newClientObservabilityFixture()
	principal := ClientSessionPrincipal{
		ConnectionID: "connection-1",
		Identity: auth.TokenIdentity{
			TokenID: "token-1", OwnerUserID: "owner-1", Type: storage.TokenTypeClient,
		}, MetadataEnabled: true,
	}
	tooMany := make([]protocol.ClientMetadataItem, 33)
	for i := range tooMany {
		tooMany[i] = protocol.ClientMetadataItem{Name: fmt.Sprintf("field_%d", i), Value: "value"}
	}
	payloads := []protocol.ClientMetadataPayload{
		{InstanceID: "client-too-many", Revision: 1, ReportedAt: time.Now().UTC(), Items: tooMany},
		{InstanceID: "client-duplicate", Revision: 1, ReportedAt: time.Now().UTC(), Items: []protocol.ClientMetadataItem{
			{Name: "environment", Value: "production"}, {Name: "environment", Value: "staging"},
		}},
	}
	for _, payload := range payloads {
		ack, err := service.Hello(context.Background(), principal, payload)
		if err == nil || ack.Accepted {
			t.Fatalf("metadata payload was accepted: instance=%q ack=%#v err=%v", payload.InstanceID, ack, err)
		}
	}
	if len(instances.upserts) != 0 {
		t.Fatalf("invalid metadata items were persisted: %#v", instances.upserts)
	}
}

func TestClientObservabilityRegistersLegacyClient(t *testing.T) {
	service, instances, connections, _ := newClientObservabilityFixture()
	principal := ClientSessionPrincipal{
		ConnectionID: "connection-1",
		Identity: auth.TokenIdentity{
			TokenID: "token-1", OwnerUserID: "owner-1", Type: storage.TokenTypeClient,
		},
	}
	clientInstanceID, err := service.RegisterLegacy(context.Background(), principal)
	if err != nil {
		t.Fatal(err)
	}
	if clientInstanceID != "client-instance-1" {
		t.Fatalf("client instance id = %q", clientInstanceID)
	}
	if len(instances.upserts) != 1 || instances.upserts[0].InstanceID != "legacy-connection-1" || instances.upserts[0].Capabilities != "" {
		t.Fatalf("legacy upsert = %#v", instances.upserts)
	}
	if len(connections.registered) != 1 {
		t.Fatalf("legacy leases = %#v", connections.registered)
	}
}

func TestClientObservabilityReleaseCleansConnectionState(t *testing.T) {
	service, _, _, _ := newClientObservabilityFixture()
	principal := ClientSessionPrincipal{
		ConnectionID: "connection-1",
		Identity: auth.TokenIdentity{
			TokenID: "token-1", OwnerUserID: "owner-1", Type: storage.TokenTypeClient,
		}, MetadataEnabled: true,
	}
	payload := protocol.ClientMetadataPayload{
		InstanceID: "client-0123456789abcdef", Revision: 1, ReportedAt: time.Now().UTC(),
	}
	if _, err := service.Hello(context.Background(), principal, payload); err != nil {
		t.Fatal(err)
	}
	if err := service.Release(context.Background(), principal); err != nil {
		t.Fatal(err)
	}
	if len(service.revisions) != 0 || len(service.instanceIDs) != 0 {
		t.Fatalf("revisions=%d instanceIDs=%d, want both empty", len(service.revisions), len(service.instanceIDs))
	}
}

func TestClientObservabilityHeartbeatTouchesInstanceAndLease(t *testing.T) {
	service, instances, connections, manager := newClientObservabilityFixture()
	principal := ClientSessionPrincipal{
		ConnectionID: "connection-1",
		Identity: auth.TokenIdentity{
			TokenID: "token-1", OwnerUserID: "owner-1", Type: storage.TokenTypeClient,
		},
	}
	if _, err := service.RegisterLegacy(context.Background(), principal); err != nil {
		t.Fatal(err)
	}
	manager.ObserveStreamOpened("connection-1")
	if err := service.Heartbeat(context.Background(), principal); err != nil {
		t.Fatal(err)
	}
	if len(instances.touched) != 1 {
		t.Fatalf("touched = %#v", instances.touched)
	}
	if instances.touchedIDs[0] != [2]string{"owner-1", "legacy-connection-1"} {
		t.Fatalf("touched ids = %#v", instances.touchedIDs)
	}
	if len(connections.renewed) != 1 || len(connections.stats) != 1 || connections.stats[0].ActiveStreams != 1 {
		t.Fatalf("leases renewed=%v stats=%#v", connections.renewed, connections.stats)
	}
}

// TestClientObservabilityHeartbeatTouchesMetadataWhenLeaseFails pins the
// decoupling between the two durable writes a heartbeat performs. A live
// physical WebSocket is itself proof that the client instance is alive, so a
// lease-table failure must not stop the metadata refresh. Before this, the
// lease error returned early and TouchInstance never ran: the metadata TTL
// lapsed, the sweeper marked the instance stale, and the console showed
// "metadata expired" for a client that was actively exchanging frames.
func TestClientObservabilityHeartbeatTouchesMetadataWhenLeaseFails(t *testing.T) {
	service, instances, connections, manager := newClientObservabilityFixture()
	principal := ClientSessionPrincipal{
		ConnectionID: "connection-1",
		Identity: auth.TokenIdentity{
			TokenID: "token-1", OwnerUserID: "owner-1", Type: storage.TokenTypeClient,
		},
	}
	if _, err := service.RegisterLegacy(context.Background(), principal); err != nil {
		t.Fatal(err)
	}
	// Fail both lease writes so the heartbeat cannot recover by re-registering.
	connections.renewErr = errClientLeaseWrite
	connections.err = errClientLeaseWrite
	instances.touched = nil
	instances.touchedIDs = nil

	err := service.Heartbeat(context.Background(), principal)
	if !errors.Is(err, errClientLeaseWrite) {
		t.Fatalf("heartbeat error = %v, want the lease failure to stay observable", err)
	}
	if len(instances.touched) != 1 {
		t.Fatalf("metadata must still be refreshed while the lease write fails; touched = %#v", instances.touched)
	}
	if instances.touchedIDs[0] != [2]string{"owner-1", "legacy-connection-1"} {
		t.Fatalf("touched ids = %#v", instances.touchedIDs)
	}
	if len(connections.renewed) != 1 {
		t.Fatalf("renewed = %#v", connections.renewed)
	}
	_ = manager
}

// A Client that reconnects without CLIENT_HELLO leaves a per-connection row
// behind. Without this cleanup those rows accumulate forever as
// "metadata 已过期" ghosts for a machine that is already listed as online.
func TestClientObservabilityReleaseDropsUnreportedRow(t *testing.T) {
	service, instances, _, _ := newClientObservabilityFixture()
	principal := ClientSessionPrincipal{
		ConnectionID: "connection-1",
		Identity: auth.TokenIdentity{
			TokenID: "token-1", OwnerUserID: "owner-1", Type: storage.TokenTypeClient,
		},
	}
	if _, err := service.RegisterLegacy(context.Background(), principal); err != nil {
		t.Fatal(err)
	}
	if err := service.Release(context.Background(), principal); err != nil {
		t.Fatal(err)
	}
	if len(instances.deletedUnreported) != 1 || instances.deletedUnreported[0] != "client-instance-1" {
		t.Fatalf("deleted unreported rows = %#v", instances.deletedUnreported)
	}
}

// A row that gained a CLIENT_HELLO after the connection started must survive the
// release of one physical connection out of a pooled Client.
func TestClientObservabilityKeepsReportedRowOnRelease(t *testing.T) {
	service, instances, _, _ := newClientObservabilityFixture()
	principal := ClientSessionPrincipal{
		ConnectionID: "connection-1",
		Identity: auth.TokenIdentity{
			TokenID: "token-1", OwnerUserID: "owner-1", Type: storage.TokenTypeClient,
		}, MetadataEnabled: true,
	}
	if _, err := service.Hello(context.Background(), principal, protocol.ClientMetadataPayload{
		InstanceID: "client-0123456789abcdef", Revision: 1, ReportedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	// The repository, not the service, owns the decision: it reports no affected
	// row for a CLIENT_HELLO instance, and that must not fail the release.
	instances.reportedRowKept = true
	if err := service.Release(context.Background(), principal); err != nil {
		t.Fatalf("Release() error = %v, want the no-match delete tolerated", err)
	}
	if len(instances.deletedUnreported) != 1 || instances.deletedUnreported[0] != "client-instance-1" {
		t.Fatalf("deleted unreported rows = %#v", instances.deletedUnreported)
	}
}

// The hello payload is the durable "reported" marker, so it can never collapse to
// the empty object that RegisterLegacy writes for never-reported rows.
func TestPersistedClientHelloMetadataIsNeverEmptyObject(t *testing.T) {
	service, instances, _, _ := newClientObservabilityFixture()
	principal := ClientSessionPrincipal{
		ConnectionID: "connection-1",
		Identity: auth.TokenIdentity{
			TokenID: "token-1", OwnerUserID: "owner-1", Type: storage.TokenTypeClient,
		}, MetadataEnabled: true,
	}
	if _, err := service.Hello(context.Background(), principal, protocol.ClientMetadataPayload{
		InstanceID: "client-0123456789abcdef", Revision: 1, ReportedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if len(instances.upserts) == 0 {
		t.Fatal("no metadata persisted")
	}
	if got := instances.upserts[0].Metadata; got == "{}" || got == "" {
		t.Fatalf("CLIENT_HELLO metadata = %q, must never equal the legacy marker", got)
	}
	if _, err := service.RegisterLegacy(context.Background(), principal); err != nil {
		t.Fatal(err)
	}
	if got := instances.upserts[len(instances.upserts)-1].Metadata; got != "{}" {
		t.Fatalf("legacy metadata = %q, want {}", got)
	}
}
