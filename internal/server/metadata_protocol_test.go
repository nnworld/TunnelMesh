package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func TestMetadataManagerFencesIdentityEpochAndRevision(t *testing.T) {
	m := NewAgentSessionManager(AgentSessionConfig{})
	session, err := m.Register(context.Background(), AgentRegistration{AgentID: "agent-1", NodeID: "node-1", Epoch: 4}, newFakeTransport())
	if err != nil {
		t.Fatal(err)
	}
	accepted := protocol.AgentMetadataPayload{AgentID: "agent-1", Epoch: 4, Revision: 1}
	ack, err := session.HandleMetadata(context.Background(), accepted)
	if err != nil || !ack.Accepted {
		t.Fatalf("first update ack=%+v err=%v", ack, err)
	}
	ack, err = session.HandleMetadata(context.Background(), accepted)
	if err != nil || !ack.Accepted || !ack.Idempotent {
		t.Fatalf("replay ack=%+v err=%v", ack, err)
	}
	ack, err = session.HandleMetadata(context.Background(), protocol.AgentMetadataPayload{AgentID: "agent-1", Epoch: 4, Revision: 0})
	if err != nil || ack.Accepted || len(ack.Errors) == 0 {
		t.Fatalf("lower revision ack=%+v err=%v", ack, err)
	}
	ack, err = session.HandleMetadata(context.Background(), protocol.AgentMetadataPayload{AgentID: "other", Epoch: 4, Revision: 2})
	if err != nil || ack.Accepted || len(ack.Errors) == 0 {
		t.Fatalf("identity mismatch ack=%+v err=%v", ack, err)
	}
	ack, err = session.HandleMetadata(context.Background(), protocol.AgentMetadataPayload{AgentID: "agent-1", NodeID: "other-node", Epoch: 4, Revision: 2})
	if err != nil || ack.Accepted || len(ack.Errors) == 0 || ack.Errors[0].Name != "node_id" {
		t.Fatalf("node identity mismatch ack=%+v err=%v", ack, err)
	}
	ack, err = session.HandleMetadata(context.Background(), protocol.AgentMetadataPayload{AgentID: "agent-1", Epoch: 3, Revision: 2})
	if err != nil || ack.Accepted || len(ack.Errors) == 0 {
		t.Fatalf("epoch mismatch ack=%+v err=%v", ack, err)
	}
}

func TestMetadataManagerReturnsFieldLevelCallbackErrors(t *testing.T) {
	m := NewAgentSessionManager(AgentSessionConfig{MetadataCallback: func(_ context.Context, _ AgentRegistration, _ protocol.AgentMetadataPayload) []protocol.AgentMetadataError {
		return []protocol.AgentMetadataError{{Name: "region", Code: "rejected", Message: "not allowlisted"}}
	}})
	session, err := m.Register(context.Background(), AgentRegistration{AgentID: "agent-1", NodeID: "node-1", Epoch: 1}, newFakeTransport())
	if err != nil {
		t.Fatal(err)
	}
	ack, err := session.HandleMetadata(context.Background(), protocol.AgentMetadataPayload{AgentID: "agent-1", Epoch: 1, Revision: 1})
	if err != nil || ack.Accepted || len(ack.Errors) != 1 || ack.Errors[0].Code != "rejected" {
		t.Fatalf("ack=%+v err=%v", ack, err)
	}
	if errors.Is(err, ErrSessionClosed) {
		t.Fatal("metadata callback error closed session")
	}
}

func TestMetadataManagerRejectsZeroRevisionAndConflictingReplay(t *testing.T) {
	m := NewAgentSessionManager(AgentSessionConfig{})
	session, err := m.Register(context.Background(), AgentRegistration{AgentID: "agent-1", NodeID: "node-1", Epoch: 1}, newFakeTransport())
	if err != nil {
		t.Fatal(err)
	}
	ack, err := session.HandleMetadata(context.Background(), protocol.AgentMetadataPayload{AgentID: "agent-1", Epoch: 1})
	if err != nil || ack.Accepted || len(ack.Errors) == 0 || ack.Errors[0].Code != "invalid_revision" {
		t.Fatalf("zero revision ack=%+v err=%v", ack, err)
	}
	first := protocol.AgentMetadataPayload{AgentID: "agent-1", Epoch: 1, Revision: 1, Items: []protocol.AgentMetadataItem{{Name: "region", Source: "env", Value: "east"}}}
	if ack, err = session.HandleMetadata(context.Background(), first); err != nil || !ack.Accepted {
		t.Fatalf("first ack=%+v err=%v", ack, err)
	}
	conflict := first
	conflict.Items[0].Value = "west"
	ack, err = session.HandleMetadata(context.Background(), conflict)
	if err != nil || ack.Accepted || len(ack.Errors) == 0 || ack.Errors[0].Code != "revision_conflict" {
		t.Fatalf("conflicting replay ack=%+v err=%v", ack, err)
	}
}

func TestMetadataManagerRejectsStorageOverflowWithAckError(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{MetadataService: NewAgentMetadataService(&fakeMetadataRepo{})})
	session, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-overflow", NodeID: "node-1", Epoch: 1}, newFakeTransport())
	if err != nil {
		t.Fatal(err)
	}
	ack, err := session.HandleMetadata(context.Background(), protocol.AgentMetadataPayload{AgentID: "agent-overflow", Epoch: 1, Revision: ^uint64(0)})
	if err != nil || ack.Accepted || len(ack.Errors) != 1 || ack.Errors[0].Code != "invalid_revision" {
		t.Fatalf("ack=%+v err=%v", ack, err)
	}
}

func TestMetadataManagerScopesLeasesToAgentInstances(t *testing.T) {
	db, err := storage.OpenSQLite(context.Background(), "file:metadata-instance-leases?mode=memory&cache=shared", true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := NewAgentMetadataService(db.Metadata())
	manager := NewAgentSessionManager(AgentSessionConfig{MetadataService: service, MetadataTTL: time.Minute})

	first, err := manager.Register(context.Background(), AgentRegistration{
		AgentID: "agent-instance-lease", NodeID: "node-a", Epoch: 1,
		InstanceID: "instance-a", ConnectionID: "conn-a", ConnectionEpoch: 1,
	}, newFakeTransport())
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Register(context.Background(), AgentRegistration{
		AgentID: "agent-instance-lease", NodeID: "node-b", Epoch: 1,
		InstanceID: "instance-b", ConnectionID: "conn-b", ConnectionEpoch: 1,
	}, newFakeTransport())
	if err != nil {
		t.Fatal(err)
	}
	for _, session := range []*AgentSession{first, second} {
		payload := protocol.AgentMetadataPayload{
			AgentID: session.AgentID, InstanceID: session.InstanceID, NodeID: session.NodeID,
			Epoch: session.Epoch, Revision: 1,
			Items: []protocol.AgentMetadataItem{{Name: "region", Source: "env", Value: session.InstanceID}},
		}
		if ack, err := session.HandleMetadata(context.Background(), payload); err != nil || !ack.Accepted {
			t.Fatalf("metadata ack=%+v err=%v", ack, err)
		}
	}

	view, err := service.GetView(context.Background(), "agent-instance-lease")
	if err != nil || len(view.Instances) != 2 {
		t.Fatalf("instances=%+v err=%v", view.Instances, err)
	}
	manager.RemoveSession("agent-instance-lease", first)
	if _, err := service.Get(context.Background(), "agent-instance-lease"); err != nil || len(view.Instances) != 2 {
		t.Fatalf("healthy view after disconnect=%+v err=%v", view, err)
	}
	metadataRepo, ok := db.Metadata().(storage.AgentInstanceMetadataRepository)
	if !ok {
		t.Fatal("metadata repository does not implement instance operations")
	}
	firstMetadata, err := metadataRepo.GetInstance(context.Background(), "agent-instance-lease", "instance-a")
	if err != nil || !firstMetadata.Stale {
		t.Fatalf("closed instance metadata=%+v err=%v", firstMetadata, err)
	}
	secondMetadata, err := metadataRepo.GetInstance(context.Background(), "agent-instance-lease", "instance-b")
	if err != nil || secondMetadata.Stale {
		t.Fatalf("live instance metadata=%+v err=%v", secondMetadata, err)
	}
	if err := manager.RefreshSessionMetadataLease(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	secondMetadata, err = metadataRepo.GetInstance(context.Background(), "agent-instance-lease", "instance-b")
	if err != nil || secondMetadata.Stale || secondMetadata.ExpiresAt == nil || !secondMetadata.ExpiresAt.After(time.Now().UTC()) {
		t.Fatalf("refreshed instance metadata=%+v err=%v", secondMetadata, err)
	}
}

func TestRefreshSessionMetadataLeaseHandlesConnectionPool(t *testing.T) {
	db, err := storage.OpenSQLite(context.Background(), "file:metadata-connection-pool-lease?mode=memory&cache=shared", true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	expired := time.Now().UTC().Add(-time.Minute)
	if err := db.Metadata().Upsert(context.Background(), storage.AgentRuntimeMetadata{
		AgentID:    "agent-pool-heartbeat",
		InstanceID: "instance-pool",
		NodeID:     "node-a",
		Epoch:      7,
		Revision:   1,
		Metadata:   `{"items":[]}`,
		ReportedAt: time.Now().UTC().Add(-2 * time.Minute),
		LastSeenAt: time.Now().UTC().Add(-2 * time.Minute),
		ExpiresAt:  &expired,
		Stale:      true,
		UpdatedAt:  time.Now().UTC().Add(-2 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	manager := NewAgentSessionManager(AgentSessionConfig{
		MetadataService: NewAgentMetadataService(db.Metadata()),
		MetadataTTL:     time.Minute,
	})
	if _, err := manager.Register(context.Background(), AgentRegistration{
		AgentID: "agent-pool-heartbeat", NodeID: "node-a", Epoch: 7,
		InstanceID: "instance-pool", ConnectionID: "conn-a", ConnectionEpoch: 1,
	}, newFakeTransport()); err != nil {
		t.Fatal(err)
	}
	second, err := manager.Register(context.Background(), AgentRegistration{
		AgentID: "agent-pool-heartbeat", NodeID: "node-a", Epoch: 7,
		InstanceID: "instance-pool", ConnectionID: "conn-b", ConnectionEpoch: 1,
	}, newFakeTransport())
	if err != nil {
		t.Fatal(err)
	}
	before := second.LastHeartbeat()

	if err := manager.RefreshSessionMetadataLease(context.Background(), second); err != nil {
		t.Fatalf("refresh pooled connection lease: %v", err)
	}
	if second.LastHeartbeat().Before(before) {
		t.Fatal("pooled connection heartbeat timestamp was not refreshed")
	}

	repo, ok := db.Metadata().(storage.AgentInstanceMetadataRepository)
	if !ok {
		t.Fatal("metadata repository does not implement instance operations")
	}
	metadata, err := repo.GetInstance(context.Background(), "agent-pool-heartbeat", "instance-pool")
	if err != nil || metadata.Stale || metadata.ExpiresAt == nil || !metadata.ExpiresAt.After(time.Now().UTC()) {
		t.Fatalf("refreshed pooled instance metadata=%+v err=%v", metadata, err)
	}
}

type metadataSessionWSConn struct {
	mu         sync.Mutex
	reads      [][]byte
	writes     [][]byte
	closed     bool
	written    chan struct{}
	afterReads <-chan struct{}
}

func (c *metadataSessionWSConn) ReadMessage() (int, []byte, error) {
	c.mu.Lock()
	if len(c.reads) == 0 {
		wait := c.afterReads
		c.mu.Unlock()
		if wait != nil {
			<-wait
		}
		return 0, nil, io.EOF
	}
	payload := c.reads[0]
	c.reads = c.reads[1:]
	c.mu.Unlock()
	return 2, payload, nil
}
func (c *metadataSessionWSConn) WriteMessage(_ int, payload []byte) error {
	c.mu.Lock()
	c.writes = append(c.writes, append([]byte(nil), payload...))
	written := c.written
	c.mu.Unlock()
	if written != nil {
		select {
		case written <- struct{}{}:
		default:
		}
	}
	return nil
}
func (c *metadataSessionWSConn) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return nil
}

func TestServeAgentSessionRemovesSessionAfterTransportEOF(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	conn := &metadataSessionWSConn{}
	tr := NewWSFrameTransport(conn)
	registration := AgentRegistration{AgentID: "agent-eof", NodeID: "node-1", Epoch: 1}
	if err := ServeAgentSession(context.Background(), manager, registration, tr, nil); err != nil {
		t.Fatalf("ServeAgentSession() error = %v", err)
	}
	if _, ok := manager.Get(registration.AgentID); ok {
		t.Fatal("session remained registered after transport EOF")
	}
	if len(conn.writes) != 0 {
		t.Fatalf("unexpected writes: %d", len(conn.writes))
	}
}

func TestServeAgentSessionPersistsMetadataAndFencesStaleLifecycle(t *testing.T) {
	db, err := storage.OpenSQLite(context.Background(), "file:metadata-session-integration?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := NewAgentMetadataService(db.Metadata())
	manager := NewAgentSessionManager(AgentSessionConfig{MetadataService: service})
	payload, err := protocol.EncodeAgentMetadataPayload(protocol.AgentMetadataPayload{
		AgentID: "agent-integrated", NodeID: "node-a", Epoch: 1, Revision: 1,
		Items: []protocol.AgentMetadataItem{{Name: "region", Source: "env", Value: "east"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var frame bytes.Buffer
	if err := protocol.NewEncoder(&frame).WriteFrame(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameAgentMetadataUpdate, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	conn := &metadataSessionWSConn{reads: [][]byte{frame.Bytes()}}
	if err := ServeAgentSession(context.Background(), manager, AgentRegistration{AgentID: "agent-integrated", NodeID: "node-a", Epoch: 1}, NewWSFrameTransport(conn), nil); err != nil {
		t.Fatal(err)
	}
	view, err := service.GetView(context.Background(), "agent-integrated")
	if err != nil || len(view.Items) != 1 || view.Items[0].Value != "east" {
		t.Fatalf("persisted metadata=%+v err=%v", view, err)
	}
	authService := auth.NewAuthService(db)
	user, err := authService.CreateUser(context.Background(), "metadata-owner", "metadata-pass", "user")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Agents().Create(context.Background(), storage.Agent{ID: "agent-integrated", Name: "integrated", OwnerUserID: user.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	login, err := authService.Login(context.Background(), user.Username, "metadata-pass")
	if err != nil {
		t.Fatal(err)
	}
	response := apiJSON(t, NewAPI(db, authService).Handler(), "GET", "/api/v1/agents/agent-integrated/metadata?includeStale=true", login.Token, "", nil)
	if response.Code != 200 || !bytes.Contains(response.Body.Bytes(), []byte(`"region"`)) {
		t.Fatalf("API metadata response status=%d body=%s", response.Code, response.Body.String())
	}

	oldTransport := newFakeTransport()
	old, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-integrated", NodeID: "node-a", Epoch: 1}, oldTransport)
	if err != nil {
		t.Fatal(err)
	}
	if ack, err := old.HandleMetadata(context.Background(), protocol.AgentMetadataPayload{AgentID: "agent-integrated", NodeID: "node-a", Epoch: 1, Revision: 2, Items: []protocol.AgentMetadataItem{{Name: "region", Source: "env", Value: "east"}}}); err != nil || !ack.Accepted {
		t.Fatalf("refresh ack=%+v err=%v", ack, err)
	}
	// A newer registration replaces the previous owner. Its cleanup must not
	// mark the replacement's metadata stale.
	newSession, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-integrated", NodeID: "node-b", Epoch: 2}, newFakeTransport())
	if err != nil {
		t.Fatal(err)
	}
	_ = old
	current, err := service.Get(context.Background(), "agent-integrated")
	if err != nil || current.Stale {
		t.Fatalf("replacement unexpectedly stale: %+v err=%v", current, err)
	}
	manager.RemoveSession("agent-integrated", newSession)
	current, err = service.Get(context.Background(), "agent-integrated")
	if err != nil || !current.Stale {
		t.Fatalf("disconnect did not mark stale: %+v err=%v", current, err)
	}
}

func TestServeAgentSessionHeartbeatRefreshesMetadataLease(t *testing.T) {
	repo := &fakeMetadataRepo{}
	service := NewAgentMetadataService(repo)
	expired := time.Now().UTC().Add(-time.Minute)
	repo.value = storage.AgentRuntimeMetadata{
		AgentID: "agent-heartbeat", NodeID: "node-a", Epoch: 1, Revision: 1,
		Metadata: `{"items":[]}`, ExpiresAt: &expired, Stale: true,
	}
	manager := NewAgentSessionManager(AgentSessionConfig{MetadataService: service, MetadataTTL: time.Minute})
	var ping bytes.Buffer
	if err := protocol.NewEncoder(&ping).WriteFrame(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePing}); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	written := make(chan struct{}, 1)
	conn := &metadataSessionWSConn{reads: [][]byte{ping.Bytes()}, written: written, afterReads: release}
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- ServeAgentSession(context.Background(), manager, AgentRegistration{AgentID: "agent-heartbeat", NodeID: "node-a", Epoch: 1}, NewWSFrameTransport(conn), nil)
	}()
	select {
	case <-written:
	case <-time.After(time.Second):
		t.Fatal("heartbeat pong was not sent")
	}
	conn.mu.Lock()
	writes := append([][]byte(nil), conn.writes...)
	conn.mu.Unlock()
	if len(writes) != 1 {
		t.Fatalf("writes=%d, want pong", len(writes))
	}
	frame, err := protocol.NewDecoder(bytes.NewReader(writes[0])).ReadFrame()
	if err != nil || frame.Type != protocol.FramePong {
		t.Fatalf("pong frame=%+v err=%v", frame, err)
	}
	if repo.value.Stale || repo.value.ExpiresAt == nil || !repo.value.ExpiresAt.After(time.Now().UTC()) {
		t.Fatalf("metadata lease was not refreshed: %+v", repo.value)
	}
	close(release)
	select {
	case <-serveDone:
	case <-time.After(time.Second):
		t.Fatal("server session did not stop")
	}
}
