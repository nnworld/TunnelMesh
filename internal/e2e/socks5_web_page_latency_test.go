package e2e

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/tunnelmesh/tunnelmesh/internal/agent"
	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/client"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/relay"
	"github.com/tunnelmesh/tunnelmesh/internal/server"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

const (
	latencyAgentID = "socks5-latency-agent"
	legacyAgentID  = "socks5-legacy-agent"
	nodeAID        = "socks5-node-a"
	nodeBID        = "socks5-node-b"
)

type latencyFixture struct {
	ctx          context.Context
	cancel       context.CancelFunc
	db           *storage.DB
	nodeA        *server.ServerRuntime
	nodeB        *server.ServerRuntime
	httpA        net.Listener
	httpB        net.Listener
	serveA       chan error
	serveB       chan error
	agentCancel  context.CancelFunc
	agentDone    chan error
	legacyCancel context.CancelFunc
	legacyDone   chan error
	relayClient  *relay.GRPCNodeTransport
	clientA      *client.Session
	clientB      *client.Session
	socksA       *client.SOCKS5Forward
	socksB       *client.SOCKS5Forward
	recordB      *recordingClientTransport
	fastPort     int
	bulkPort     int
	refusedPort  int
	blockedHost  string
	blockedPort  int
	clientToken  auth.CreatedToken
	legacyToken  string
	credentials  *auth.CredentialService
	closed       bool
	remoteRelay  *relay.RelayService
	targets      []io.Closer
}

type recordingClientTransport struct {
	inner    client.ReceiveTransport
	received chan protocol.Frame
}

func (t *recordingClientTransport) Send(frame protocol.Frame) error { return t.inner.Send(frame) }

func (t *recordingClientTransport) Receive() (protocol.Frame, error) {
	frame, err := t.inner.Receive()
	if err == nil {
		select {
		case t.received <- frame:
		default:
		}
	}
	return frame, err
}

func (t *recordingClientTransport) Close() error { return t.inner.Close() }

type rawWSFrameTransport struct {
	conn *websocket.Conn
}

func (t *rawWSFrameTransport) Send(frame protocol.Frame) error {
	var buffer bytes.Buffer
	if err := protocol.Encode(&buffer, frame); err != nil {
		return err
	}
	return websocket.Message.Send(t.conn, buffer.Bytes())
}

func (t *rawWSFrameTransport) Receive() (protocol.Frame, error) {
	var raw []byte
	if err := websocket.Message.Receive(t.conn, &raw); err != nil {
		return protocol.Frame{}, err
	}
	return protocol.NewDecoder(bytes.NewReader(raw)).ReadFrame()
}

func (t *rawWSFrameTransport) Close() error { return t.conn.Close() }

func TestSOCKS5WebPageLatency(t *testing.T) {
	baselineGoroutines := runtime.NumGoroutine()
	fixture, err := newLatencyFixture(t)
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.close(t)

	t.Run("strict local and cross-node relay paths", func(t *testing.T) {
		localConn, reply := socks5Connect(t, fixture.socksA.Addr().String(), "127.0.0.1", fixture.fastPort)
		defer localConn.Close()
		if reply != 0 {
			t.Fatalf("local SOCKS5 reply=%d, want success", reply)
		}
		assertEcho(t, localConn, "local-strict")

		crossConn, reply := socks5Connect(t, fixture.socksB.Addr().String(), "127.0.0.1", fixture.fastPort)
		defer crossConn.Close()
		if reply != 0 {
			t.Fatalf("cross-node SOCKS5 reply=%d, want success", reply)
		}
		assertEcho(t, crossConn, "cross-strict")
		if err := waitOpenResult(t, fixture.recordB.received); err != nil {
			t.Fatal(err)
		}
		if !fixture.remoteRelayActive(t) {
			t.Fatal("cross-node stream did not use the node-A Agent relay")
		}
	})

	t.Run("one blocked dial does not delay other opens", func(t *testing.T) {
		blocked := make(chan error, 1)
		go func() {
			conn, reply := socks5Connect(t, fixture.socksB.Addr().String(), fixture.blockedHost, fixture.blockedPort)
			if conn != nil {
				_ = conn.Close()
			}
			if reply == 0 {
				blocked <- errors.New("unreachable target unexpectedly succeeded")
				return
			}
			blocked <- nil
		}()

		const fastCount = 31
		results := make(chan error, fastCount)
		for i := 0; i < fastCount; i++ {
			go func() {
				conn, reply := socks5Connect(t, fixture.socksB.Addr().String(), "127.0.0.1", fixture.fastPort)
				if conn == nil {
					results <- fmt.Errorf("SOCKS5 reply=%d", reply)
					return
				}
				defer conn.Close()
				results <- assertEchoResult(conn, fmt.Sprintf("fast-%d", time.Now().UnixNano()))
			}()
		}
		deadline := time.After(3 * time.Second)
		for i := 0; i < fastCount; i++ {
			select {
			case err := <-results:
				if err != nil {
					t.Fatal(err)
				}
			case <-deadline:
				t.Fatal("fast opens were delayed by one blocked target dial")
			}
		}
		select {
		case err := <-blocked:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(9 * time.Second):
			t.Fatal("blocked dial did not terminate within the bounded open timeout")
		}
	})

	t.Run("refused target does not reconnect agent session", func(t *testing.T) {
		before := fixture.agentConnectionEpoch(t)
		conn, reply := socks5Connect(t, fixture.socksB.Addr().String(), "127.0.0.1", fixture.refusedPort)
		if conn != nil {
			_ = conn.Close()
		}
		if reply == 0 {
			t.Fatal("refused target returned SOCKS5 success")
		}
		time.Sleep(100 * time.Millisecond)
		if after := fixture.agentConnectionEpoch(t); after != before {
			t.Fatalf("Agent connection epoch changed after refused dial: before=%d after=%d", before, after)
		}
		goodConn, reply := socks5Connect(t, fixture.socksB.Addr().String(), "127.0.0.1", fixture.fastPort)
		defer goodConn.Close()
		if reply != 0 {
			t.Fatalf("subsequent successful target reply=%d, want 0", reply)
		}
		assertEcho(t, goodConn, "after-refused")
	})

	t.Run("bulk stream does not starve small stream first byte", func(t *testing.T) {
		bulkConn, reply := socks5Connect(t, fixture.socksA.Addr().String(), "127.0.0.1", fixture.bulkPort)
		defer bulkConn.Close()
		if reply != 0 {
			t.Fatalf("bulk SOCKS5 reply=%d, want 0", reply)
		}
		bulkDone := make(chan error, 1)
		go func() {
			_, err := io.Copy(io.Discard, bulkConn)
			bulkDone <- err
		}()

		started := time.Now()
		smallConn, reply := socks5Connect(t, fixture.socksA.Addr().String(), "127.0.0.1", fixture.fastPort)
		defer smallConn.Close()
		if reply != 0 {
			t.Fatalf("small SOCKS5 reply=%d, want 0", reply)
		}
		if err := assertEchoResult(smallConn, "small-first-byte"); err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(started); elapsed > 2*time.Second {
			t.Fatalf("small stream completed after %v, want <2s during bulk transfer", elapsed)
		}
		if _, ok := fixture.nodeA.AgentSessions.Get(latencyAgentID); !ok {
			t.Fatal("bulk stream backpressure disconnected the Agent session")
		}
		_ = bulkConn.Close()
		select {
		case err := <-bulkDone:
			if err != nil && !errors.Is(err, net.ErrClosed) {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("bulk reader did not stop after close")
		}
	})

	t.Run("local tcp forward delivers large fixed-length response", func(t *testing.T) {
		body := bytes.Repeat([]byte("j"), 1382571)
		target, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer target.Close()
		go func() {
			for {
				conn, err := target.Accept()
				if err != nil {
					return
				}
				go func(conn net.Conn) {
					defer conn.Close()
					request := make([]byte, 4096)
					_ = conn.SetReadDeadline(time.Now().Add(time.Second))
					if _, err := conn.Read(request); err != nil {
						return
					}
					_, _ = fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n", len(body))
					_, _ = io.Copy(conn, bytes.NewReader(body))
				}(conn)
			}
		}()
		_, portText, _ := net.SplitHostPort(target.Addr().String())
		var targetPort int
		if _, err := fmt.Sscanf(portText, "%d", &targetPort); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		if err := fixture.db.Policies().Create(fixture.ctx, storage.AgentPolicy{
			ID: "latency-local-http", AgentID: latencyAgentID, TargetHost: "127.0.0.1", TargetPort: targetPort,
			Protocol: "tcp", CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		localToken, err := fixture.credentials.Create(fixture.ctx, auth.CreateTokenInput{
			Type: storage.TokenTypeClient, OwnerUserID: fixture.clientToken.OwnerUserID,
			Scope: auth.TokenScope{
				AgentIDs: []string{latencyAgentID}, Protocols: []string{"tcp"},
				TargetCIDRs: []string{"127.0.0.0/8"}, TargetPorts: []int{targetPort},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		localTransport, err := client.DialWebSocket(fixture.ctx, "ws://"+fixture.httpA.Addr().String()+"/ws/client", localToken.Secret)
		if err != nil {
			t.Fatal(err)
		}
		localSession := client.NewSessionWithOpenMode(localTransport, client.SessionOpenFlowControl)
		defer localSession.Close()
		forward, err := client.NewTCPForward(client.NewSessionOpener(localSession), client.TCPForwardConfig{
			ListenAddr: "127.0.0.1:0", AgentID: latencyAgentID,
			TargetHost: "127.0.0.1", TargetPort: targetPort, ConnectTimeout: 4 * time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := forward.Start(fixture.ctx); err != nil {
			t.Fatal(err)
		}
		defer forward.Close()

		conn, err := net.Dial("tcp", forward.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if _, err := fmt.Fprintf(conn, "GET /assets/mode.js HTTP/1.1\r\nHost: local\r\nConnection: close\r\n\r\n"); err != nil {
			t.Fatal(err)
		}
		received, err := io.ReadAll(conn)
		if err != nil {
			t.Fatalf("read large HTTP response: %v", err)
		}
		headerEnd := bytes.Index(received, []byte("\r\n\r\n"))
		if headerEnd < 0 {
			t.Fatalf("response headers missing: %q", received[:min(len(received), 128)])
		}
		headers := received[:headerEnd]
		if !bytes.Contains(headers, []byte(fmt.Sprintf("Content-Length: %d", len(body)))) {
			t.Fatalf("response headers=%q, want Content-Length %d", headers, len(body))
		}
		if got := received[headerEnd+4:]; !bytes.Equal(got, body) {
			t.Fatalf("response body length=%d, want byte-for-byte %d bytes", len(got), len(body))
		}
	})

	t.Run("legacy client with strict agent", func(t *testing.T) {
		transport, err := dialRawClientWebSocket(fixture.ctx, fixture.httpA.Addr().String(), fixture.clientToken.Secret)
		if err != nil {
			t.Fatal(err)
		}
		session := client.NewSession(transport)
		defer session.Close()
		stream, err := session.OpenStreamConn(fixture.ctx, client.StreamRequest{
			AgentID: latencyAgentID, Protocol: "tcp", TargetHost: "127.0.0.1", TargetPort: fixture.fastPort,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer stream.Close()
		if _, err := stream.Write([]byte("legacy-client")); err != nil {
			t.Fatal(err)
		}
		buffer := make([]byte, len("legacy-client"))
		if _, err := readFullWithTimeout(stream, buffer, 3*time.Second); err != nil {
			t.Fatal(err)
		}
		if string(buffer) != "legacy-client" {
			t.Fatalf("legacy client echo=%q", buffer)
		}
	})

	t.Run("legacy client with legacy agent", func(t *testing.T) {
		if err := fixture.startLegacyAgent(t); err != nil {
			t.Fatal(err)
		}
		transport, err := dialRawClientWebSocket(fixture.ctx, fixture.httpA.Addr().String(), fixture.clientToken.Secret)
		if err != nil {
			t.Fatal(err)
		}
		session := client.NewSession(transport)
		defer session.Close()
		stream, err := session.OpenStreamConn(fixture.ctx, client.StreamRequest{
			AgentID: legacyAgentID, Protocol: "tcp", TargetHost: "127.0.0.1", TargetPort: fixture.fastPort,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer stream.Close()
		if _, err := stream.Write([]byte("legacy-agent")); err != nil {
			t.Fatal(err)
		}
		buffer := make([]byte, len("legacy-agent"))
		if _, err := readFullWithTimeout(stream, buffer, 3*time.Second); err != nil {
			t.Fatal(err)
		}
		if string(buffer) != "legacy-agent" {
			t.Fatalf("legacy agent echo=%q", buffer)
		}
	})

	t.Run("multi-server revocation blocks within three seconds", func(t *testing.T) {
		if err := fixture.nodeA.Credentials.Revoke(fixture.ctx, fixture.clientToken.TokenID); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			conn, reply := socks5Connect(t, fixture.socksB.Addr().String(), "127.0.0.1", fixture.fastPort)
			if conn != nil {
				_ = conn.Close()
			}
			if reply != 0 {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("revoked token still opened a new stream within 3 seconds")
	})

	fixture.close(t)
	waitGoroutinesNearBaseline(t, baselineGoroutines)
}

func newLatencyFixture(t *testing.T) (*latencyFixture, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	fixture := &latencyFixture{
		ctx: ctx, cancel: cancel,
		blockedHost: "10.255.255.1", blockedPort: 18081,
		serveA: make(chan error, 1), serveB: make(chan error, 1),
		agentDone: make(chan error, 1), legacyDone: make(chan error, 1),
	}
	dsn := fmt.Sprintf("file:e2e-socks5-latency-%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := storage.OpenSQLite(ctx, dsn)
	if err != nil {
		cancel()
		return nil, err
	}
	fixture.db = db

	fastListener, err := startEchoTarget()
	if err != nil {
		fixture.closeWithError(t, err)
		return nil, err
	}
	fixture.targets = append(fixture.targets, fastListener)
	_, fastPortText, _ := net.SplitHostPort(fastListener.Addr().String())
	if _, err := fmt.Sscanf(fastPortText, "%d", &fixture.fastPort); err != nil {
		fixture.closeWithError(t, err)
		return nil, err
	}
	bulkListener, err := startBulkTarget()
	if err != nil {
		fixture.closeWithError(t, err)
		return nil, err
	}
	fixture.targets = append(fixture.targets, bulkListener)
	_, bulkPortText, _ := net.SplitHostPort(bulkListener.Addr().String())
	if _, err := fmt.Sscanf(bulkPortText, "%d", &fixture.bulkPort); err != nil {
		fixture.closeWithError(t, err)
		return nil, err
	}
	refusedListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fixture.closeWithError(t, err)
		return nil, err
	}
	_, refusedPortText, _ := net.SplitHostPort(refusedListener.Addr().String())
	if _, err := fmt.Sscanf(refusedPortText, "%d", &fixture.refusedPort); err != nil {
		_ = refusedListener.Close()
		fixture.closeWithError(t, err)
		return nil, err
	}
	_ = refusedListener.Close()

	authService := auth.NewAuthService(db)
	owner, err := authService.CreateUser(ctx, "socks5-latency-owner", "socks5-latency-password", "user")
	if err != nil {
		fixture.closeWithError(t, err)
		return nil, err
	}
	for _, agentID := range []string{latencyAgentID, legacyAgentID} {
		if err := db.Agents().Create(ctx, storage.Agent{ID: agentID, Name: agentID, OwnerUserID: owner.ID, Enabled: true}); err != nil {
			fixture.closeWithError(t, err)
			return nil, err
		}
	}
	now := time.Now().UTC()
	for _, nodeID := range []string{nodeAID, nodeBID} {
		if err := db.Nodes().Create(ctx, storage.ServerNode{
			ID: nodeID, Name: nodeID, Address: "127.0.0.1:1", Epoch: 1,
			Enabled: true, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			fixture.closeWithError(t, err)
			return nil, err
		}
	}
	policies := []storage.AgentPolicy{
		{ID: "latency-fast", AgentID: latencyAgentID, TargetHost: "127.0.0.1", TargetPort: fixture.fastPort, Protocol: "tcp", CreatedAt: now, UpdatedAt: now},
		{ID: "latency-bulk", AgentID: latencyAgentID, TargetHost: "127.0.0.1", TargetPort: fixture.bulkPort, Protocol: "tcp", CreatedAt: now, UpdatedAt: now},
		{ID: "latency-refused", AgentID: latencyAgentID, TargetHost: "127.0.0.1", TargetPort: fixture.refusedPort, Protocol: "tcp", CreatedAt: now, UpdatedAt: now},
		{ID: "latency-blocked", AgentID: latencyAgentID, TargetHost: fixture.blockedHost, TargetPort: fixture.blockedPort, Protocol: "tcp", CreatedAt: now, UpdatedAt: now},
		{ID: "legacy-fast", AgentID: legacyAgentID, TargetHost: "127.0.0.1", TargetPort: fixture.fastPort, Protocol: "tcp", CreatedAt: now, UpdatedAt: now},
	}
	for _, policy := range policies {
		if err := db.Policies().Create(ctx, policy); err != nil {
			fixture.closeWithError(t, err)
			return nil, err
		}
	}

	credentials := auth.NewCredentialService(db)
	fixture.credentials = credentials
	nodeToken, err := credentials.Create(ctx, auth.CreateTokenInput{
		Type: storage.TokenTypeServerNode, OwnerUserID: owner.ID,
		Scope: auth.TokenScope{ServerNodeIDs: []string{nodeAID, nodeBID}},
	})
	if err != nil {
		fixture.closeWithError(t, err)
		return nil, err
	}
	latencyAgentToken, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeAgent, OwnerUserID: owner.ID, AgentID: latencyAgentID})
	if err != nil {
		fixture.closeWithError(t, err)
		return nil, err
	}
	legacyAgentToken, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeAgent, OwnerUserID: owner.ID, AgentID: legacyAgentID})
	if err != nil {
		fixture.closeWithError(t, err)
		return nil, err
	}
	fixture.legacyToken = legacyAgentToken.Secret
	clientToken, err := credentials.Create(ctx, auth.CreateTokenInput{
		Type: storage.TokenTypeClient, OwnerUserID: owner.ID,
		Scope: auth.TokenScope{
			AgentIDs:  []string{latencyAgentID, legacyAgentID},
			Protocols: []string{"tcp"},
			TargetCIDRs: []string{
				"127.0.0.0/8", "10.255.255.0/24",
			},
			TargetPorts: []int{fixture.fastPort, fixture.bulkPort, fixture.refusedPort, fixture.blockedPort},
		},
	})
	if err != nil {
		fixture.closeWithError(t, err)
		return nil, err
	}
	fixture.clientToken = clientToken

	relayAAddress, err := reserveLocalAddress()
	if err != nil {
		fixture.closeWithError(t, err)
		return nil, err
	}
	relayBAddress, err := reserveLocalAddress()
	if err != nil {
		fixture.closeWithError(t, err)
		return nil, err
	}
	fixture.nodeA, err = server.NewServerRuntime(db, server.AgentSessionConfig{}, server.RuntimeConfig{
		NodeID: nodeAID,
		Relay:  config.RelayConfig{Enabled: true, Listen: relayAAddress, Endpoint: relayAAddress, NodeToken: nodeToken.Secret},
		Stream: config.ServerStreamConfig{MaxConcurrentOpens: 64, MaxPendingOpens: 128, InitialWindow: 262144, WindowUpdateThreshold: 131072, MaxFramePayload: 32768},
	})
	if err != nil {
		fixture.closeWithError(t, err)
		return nil, err
	}
	fixture.httpA, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fixture.closeWithError(t, err)
		return nil, err
	}
	go func() { fixture.serveA <- fixture.nodeA.ServeListener(fixture.ctx, fixture.httpA) }()

	remoteRelay := relay.NewRelayService()
	fixture.remoteRelay = remoteRelay
	fixture.nodeB, err = server.NewServerRuntime(db, server.AgentSessionConfig{}, server.RuntimeConfig{
		NodeID: nodeBID, RemoteRelay: remoteRelay,
		Relay:  config.RelayConfig{Enabled: true, Listen: relayBAddress, Endpoint: relayBAddress, NodeToken: nodeToken.Secret},
		Stream: config.ServerStreamConfig{MaxConcurrentOpens: 64, MaxPendingOpens: 128, InitialWindow: 262144, WindowUpdateThreshold: 131072, MaxFramePayload: 32768},
		AuthorizationCache: config.AuthorizationCacheConfig{
			Enabled: true, LocalPositiveTTL: time.Hour, ClusterPositiveTTL: time.Hour,
			NegativeTTL: time.Second, RevisionPollInterval: 20 * time.Millisecond,
			MaxStaleOnPollError: time.Second, MaxEntries: 1000,
		},
	})
	if err != nil {
		fixture.closeWithError(t, err)
		return nil, err
	}
	fixture.httpB, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fixture.closeWithError(t, err)
		return nil, err
	}
	go func() { fixture.serveB <- fixture.nodeB.ServeListener(fixture.ctx, fixture.httpB) }()

	nodeA, err := db.Nodes().Get(ctx, nodeAID)
	if err != nil {
		fixture.closeWithError(t, err)
		return nil, err
	}
	fixture.relayClient, err = fixture.nodeB.DialRelayNode(ctx, relayAAddress, nodeA.Epoch)
	if err != nil {
		fixture.closeWithError(t, err)
		return nil, err
	}
	remoteRelay.RegisterNode(nodeAID, nodeA.Epoch, fixture.relayClient)

	agentCtx, agentCancel := context.WithCancel(ctx)
	fixture.agentCancel = agentCancel
	agentStreams := config.AgentStreamConfig{
		MaxConcurrentDials: 32, MaxPendingDials: 128,
		ConnectTimeout: 4 * time.Second, OpenTimeout: 4 * time.Second,
	}
	go func() {
		fixture.agentDone <- agent.RunConnectionPool(agentCtx, agent.WebSocketPoolOptions{
			ServerURL: "ws://" + fixture.httpA.Addr().String() + "/ws/agent",
			Token:     latencyAgentToken.Secret, AgentID: latencyAgentID, NodeID: "latency-agent-node",
			InstanceID: "latency-agent-instance", Epoch: 1,
			Collector: agent.NewMetadataCollector(nil),
			Factory: func(session *agent.Session, _ string) agent.SessionFrameHandler {
				session.SetCapabilities(agent.AgentStreamCapabilities(agentStreams))
				return agent.NewStreamDispatcherWithConfig(agent.Dialer{}, nil, session.Send, agent.DialExecutorConfig{
					MaxConcurrent: agentStreams.MaxConcurrentDials, MaxPending: agentStreams.MaxPendingDials,
					ConnectTimeout: agentStreams.ConnectTimeout, OpenTimeout: agentStreams.OpenTimeout,
				}, nil)
			},
			Min: 1, Max: 1,
		})
	}()
	if err := waitForCondition(5*time.Second, func() bool {
		_, ok := fixture.nodeA.AgentSessions.Get(latencyAgentID)
		return ok
	}); err != nil {
		fixture.closeWithError(t, err)
		return nil, err
	}

	transportA, err := client.DialWebSocket(ctx, "ws://"+fixture.httpA.Addr().String()+"/ws/client", clientToken.Secret)
	if err != nil {
		fixture.closeWithError(t, err)
		return nil, err
	}
	fixture.clientA = client.NewSessionWithOpenMode(transportA, client.SessionOpenFlowControl)
	fixture.socksA, err = client.NewSOCKS5Forward(client.NewSessionOpener(fixture.clientA), client.SOCKS5ForwardConfig{
		ListenAddr: "127.0.0.1:0", AgentID: latencyAgentID,
		ConnectTimeout: 4 * time.Second, HandshakeTimeout: 3 * time.Second,
	})
	if err != nil {
		fixture.closeWithError(t, err)
		return nil, err
	}
	if err := fixture.socksA.Start(ctx); err != nil {
		fixture.closeWithError(t, err)
		return nil, err
	}

	transportB, err := client.DialWebSocket(ctx, "ws://"+fixture.httpB.Addr().String()+"/ws/client", clientToken.Secret)
	if err != nil {
		fixture.closeWithError(t, err)
		return nil, err
	}
	fixture.recordB = &recordingClientTransport{inner: transportB, received: make(chan protocol.Frame, 256)}
	fixture.clientB = client.NewSessionWithOpenMode(fixture.recordB, client.SessionOpenFlowControl)
	fixture.socksB, err = client.NewSOCKS5Forward(client.NewSessionOpener(fixture.clientB), client.SOCKS5ForwardConfig{
		ListenAddr: "127.0.0.1:0", AgentID: latencyAgentID,
		ConnectTimeout: 4 * time.Second, HandshakeTimeout: 3 * time.Second,
	})
	if err != nil {
		fixture.closeWithError(t, err)
		return nil, err
	}
	if err := fixture.socksB.Start(ctx); err != nil {
		fixture.closeWithError(t, err)
		return nil, err
	}
	return fixture, nil
}

func (f *latencyFixture) startLegacyAgent(t *testing.T) error {
	t.Helper()
	if f.legacyCancel != nil {
		return nil
	}
	legacyCtx, cancel := context.WithCancel(f.ctx)
	f.legacyCancel = cancel
	configWebSocket, err := websocket.NewConfig("ws://"+f.httpA.Addr().String()+"/ws/agent", "http://"+f.httpA.Addr().String())
	if err != nil {
		cancel()
		return err
	}
	configWebSocket.Header.Set("Authorization", "Bearer "+f.legacyToken)
	conn, err := websocket.DialConfig(configWebSocket)
	if err != nil {
		cancel()
		return err
	}
	transport := &rawWSFrameTransport{conn: conn}
	session := agent.NewSessionWithMetadata(transport, agent.NewMetadataCollector(nil))
	session.SetMetadataIdentity(legacyAgentID, "legacy-agent-node", 1)
	dispatcher := agent.NewStreamDispatcherWithConfig(agent.Dialer{}, nil, session.Send, agent.DialExecutorConfig{}, nil)
	go func() { f.legacyDone <- session.Run(legacyCtx, dispatcher.Handle) }()
	return waitForCondition(3*time.Second, func() bool {
		_, ok := f.nodeA.AgentSessions.Get(legacyAgentID)
		return ok
	})
}

func (f *latencyFixture) remoteRelayActive(t *testing.T) bool {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, session := range f.nodeA.AgentSessions.List(latencyAgentID) {
			if f.nodeA.LocalAgentRelay.ActiveStreams(latencyAgentID, session.ConnectionID) > 0 {
				return true
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func (f *latencyFixture) agentConnectionEpoch(t *testing.T) int64 {
	t.Helper()
	session, ok := f.nodeA.AgentSessions.Get(latencyAgentID)
	if !ok {
		t.Fatal("latency Agent is offline")
	}
	return session.ConnectionEpoch
}

func (f *latencyFixture) closeWithError(t *testing.T, cause error) {
	t.Helper()
	_ = cause
	f.close(t)
}

func (f *latencyFixture) close(t *testing.T) {
	t.Helper()
	if f.closed {
		return
	}
	f.closed = true
	if f.socksA != nil {
		_ = f.socksA.Close()
	}
	if f.socksB != nil {
		_ = f.socksB.Close()
	}
	if f.clientA != nil {
		_ = f.clientA.Close()
	}
	if f.clientB != nil {
		_ = f.clientB.Close()
	}
	if f.agentCancel != nil {
		f.agentCancel()
	}
	if f.legacyCancel != nil {
		f.legacyCancel()
	}
	if f.relayClient != nil {
		_ = f.relayClient.Close()
	}
	if f.httpA != nil {
		_ = f.httpA.Close()
	}
	if f.httpB != nil {
		_ = f.httpB.Close()
	}
	f.cancel()
	for _, done := range []chan error{f.serveA, f.serveB} {
		if done == nil {
			continue
		}
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, net.ErrClosed) {
				t.Logf("fixture component cleanup: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("fixture component cleanup timed out")
		}
	}
	if f.agentCancel != nil {
		select {
		case err := <-f.agentDone:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Logf("Agent cleanup: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("Agent cleanup timed out")
		}
	}
	if f.legacyCancel != nil {
		select {
		case err := <-f.legacyDone:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Logf("legacy Agent cleanup: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("legacy Agent cleanup timed out")
		}
	}
	for _, target := range f.targets {
		_ = target.Close()
	}
	if f.credentials != nil {
		_ = f.credentials.Close()
	}
	if f.nodeA != nil {
		_ = f.nodeA.Close()
	}
	if f.nodeB != nil {
		_ = f.nodeB.Close()
	}
	if f.db != nil {
		_ = f.db.Close()
	}
}

func startEchoTarget() (net.Listener, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}(conn)
		}
	}()
	return listener, nil
}

func startBulkTarget() (net.Listener, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				chunk := bytes.Repeat([]byte("b"), 32<<10)
				for {
					if _, err := conn.Write(chunk); err != nil {
						return
					}
				}
			}(conn)
		}
	}()
	return listener, nil
}

func reserveLocalAddress() (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		return "", err
	}
	return address, nil
}

func dialRawClientWebSocket(ctx context.Context, address, token string) (client.ReceiveTransport, error) {
	wsConfig, err := websocket.NewConfig("ws://"+address+"/ws/client", "http://"+address)
	if err != nil {
		return nil, err
	}
	wsConfig.Header.Set("Authorization", "Bearer "+token)
	conn, err := wsConfig.DialContext(ctx)
	if err != nil {
		return nil, err
	}
	conn.MaxPayloadBytes = protocol.MaxPayload + 16
	return &rawWSFrameTransport{conn: conn}, nil
}

func socks5Connect(t *testing.T, address, host string, port int) (net.Conn, byte) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, 2*time.Second)
	if err != nil {
		return nil, 255
	}
	_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		_ = conn.Close()
		return nil, 255
	}
	selection := make([]byte, 2)
	if _, err := io.ReadFull(conn, selection); err != nil || selection[1] != 0 {
		_ = conn.Close()
		return nil, 255
	}
	if ip := net.ParseIP(host); ip == nil || ip.To4() == nil {
		_ = conn.Close()
		return nil, 255
	}
	request := []byte{5, 1, 0, 1}
	request = append(request, net.ParseIP(host).To4()...)
	request = binaryAppendPort(request, port)
	if _, err := conn.Write(request); err != nil {
		_ = conn.Close()
		return nil, 255
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil {
		_ = conn.Close()
		return nil, 255
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, reply[1]
}

func binaryAppendPort(buffer []byte, port int) []byte {
	return append(buffer, byte(port>>8), byte(port))
}

func assertEcho(t *testing.T, conn net.Conn, payload string) {
	t.Helper()
	if err := assertEchoResult(conn, payload); err != nil {
		t.Fatal(err)
	}
}

func assertEchoResult(conn net.Conn, payload string) error {
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return err
	}
	if _, err := conn.Write([]byte(payload)); err != nil {
		return err
	}
	buffer := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, buffer); err != nil {
		return err
	}
	if string(buffer) != payload {
		return fmt.Errorf("echo=%q, want %q", buffer, payload)
	}
	return conn.SetDeadline(time.Time{})
}

func readFullWithTimeout(reader io.Reader, buffer []byte, timeout time.Duration) (int, error) {
	type readResult struct {
		n   int
		err error
	}
	result := make(chan readResult, 1)
	go func() {
		n, err := io.ReadFull(reader, buffer)
		result <- readResult{n: n, err: err}
	}()
	select {
	case got := <-result:
		return got.n, got.err
	case <-time.After(timeout):
		return 0, errors.New("read timed out")
	}
}

func waitOpenResult(t *testing.T, frames <-chan protocol.Frame) error {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case frame := <-frames:
			if frame.Type != protocol.FrameOpenResult {
				continue
			}
			payload, err := protocol.DecodeOpenResultPayload(frame.Payload)
			if err != nil {
				return err
			}
			if !payload.Accepted || payload.Code != protocol.OpenResultCodeOK {
				return fmt.Errorf("OPEN_RESULT=%+v", payload)
			}
			return nil
		case <-deadline:
			return errors.New("timed out waiting for successful OPEN_RESULT")
		}
	}
}

func waitForCondition(timeout time.Duration, condition func() bool) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	return errors.New("condition timed out")
}

func waitGoroutinesNearBaseline(t *testing.T, baseline int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= baseline+2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("goroutines after cleanup=%d, baseline=%d", runtime.NumGoroutine(), baseline)
}
