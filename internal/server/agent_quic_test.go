package server

import (
	"context"
	"crypto/tls"
	"net"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/tunnelmesh/tunnelmesh/internal/agent"
	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// TestRealAgentConnectsOverQUIC runs the production agent over quic:// and checks the
// agent is registered through the single bidirectional stream.
func TestRealAgentConnectsOverQUIC(t *testing.T) {
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, "file:raw-agent-quic?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner, err := auth.NewAuthService(db).CreateUser(ctx, "quic-owner", "owner-pass", "user")
	if err != nil {
		t.Fatal(err)
	}
	agentID := "quic-agent"
	if err := db.Agents().Create(ctx, storage.Agent{ID: agentID, Name: agentID, OwnerUserID: owner.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	credentials := auth.NewCredentialService(db)
	token, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeAgent, OwnerUserID: owner.ID, AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	_ = credentials.Close()

	certFile, keyFile, roots := selfSignedForLoopback(t)
	listen := freeLoopbackUDPAddr(t)
	transports := config.ServerAgentTransportsConfig{QUIC: config.ServerAgentListenerConfig{Listen: listen, TLSCertFile: certFile, TLSKeyFile: keyFile}}
	runtime, err := NewServerRuntime(db, AgentSessionConfig{}, RuntimeConfig{AgentTransports: transports})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	httpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { _ = runtime.ServeListener(serveCtx, httpListener) }()

	agentCtx, agentCancel := context.WithCancel(ctx)
	defer agentCancel()
	agentErr := make(chan error, 1)
	go func() {
		agentErr <- agent.RunWebSocketWithOptions(agentCtx, "quic://"+listen, token.Secret, agentID, "node-quic", 1,
			agent.NewMetadataCollector(nil), nil, agent.WebSocketRunOptions{TLSRootCAs: roots})
	}()
	deadline := time.Now().Add(8 * time.Second)
	for {
		metadata, getErr := db.Metadata().Get(ctx, agentID)
		if getErr == nil && metadata.AgentID == agentID {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("agent never registered over quic: err=%v", getErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
	agentCancel()
	<-agentErr
}

// TestQUICListenerRefusesAnotherALPN checks that only the agent protocol is accepted:
// a peer offering a different application protocol never reaches the preface.
func TestQUICListenerRefusesAnotherALPN(t *testing.T) {
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, "file:raw-agent-quic-alpn?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	certFile, keyFile, roots := selfSignedForLoopback(t)
	listen := freeLoopbackUDPAddr(t)
	runtime, err := NewServerRuntime(db, AgentSessionConfig{}, RuntimeConfig{AgentTransports: config.ServerAgentTransportsConfig{
		QUIC: config.ServerAgentListenerConfig{Listen: listen, TLSCertFile: certFile, TLSKeyFile: keyFile},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	httpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { _ = runtime.ServeListener(serveCtx, httpListener) }()

	dialCtx, dialCancel := context.WithTimeout(ctx, 3*time.Second)
	defer dialCancel()
	conn, err := quic.DialAddr(dialCtx, listen, &tls.Config{ServerName: "localhost", RootCAs: roots, MinVersion: tls.VersionTLS13, NextProtos: []string{"h3"}}, nil)
	if err == nil {
		_ = conn.CloseWithError(0, "")
		t.Fatal("a peer offering a foreign ALPN must fail the handshake")
	}
}

// freeLoopbackUDPAddr reserves a UDP port the same way freeLoopbackAddr reserves TCP.
func freeLoopbackUDPAddr(t *testing.T) string {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := conn.LocalAddr().String()
	_ = conn.Close()
	return addr
}

// A QUIC agent connection carries one bidirectional stream. The limit is what keeps a
// peer from multiplying its share of the pre-authentication budget.
func TestQUICAgentConnectionAllowsOneStream(t *testing.T) {
	if got := quicAgentConfig().MaxIncomingStreams; got != 1 {
		t.Fatalf("MaxIncomingStreams = %d, want 1", got)
	}
}
