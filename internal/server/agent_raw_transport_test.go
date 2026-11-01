package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/agent"
	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// selfSignedForLoopback writes a certificate valid for 127.0.0.1 and returns its PEM
// paths plus a pool that trusts it, so the agent verifies the server as it would a
// private CA.
func selfSignedForLoopback(t *testing.T) (certFile, keyFile string, roots *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "tunnelmesh test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	roots = x509.NewCertPool()
	roots.AddCert(mustParseCert(t, der))
	return certFile, keyFile, roots
}

func mustParseCert(t *testing.T, der []byte) *x509.Certificate {
	t.Helper()
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

// freeLoopbackAddr reserves a port by binding and releasing it. The window is short and
// the test owns the machine's loopback, which is the usual trade-off for these tests.
func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// TestRealAgentConnectsOverRawTransports runs the production agent loop against a
// server with a raw listener and checks that the agent's metadata is persisted, which
// only happens after the preface, the hello and the authenticated registration.
func TestRealAgentConnectsOverRawTransports(t *testing.T) {
	cases := []struct {
		name string
		tls  bool
	}{
		{"tcp plaintext on loopback", false},
		{"tls over tcp", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db, err := storage.OpenSQLite(ctx, "file:raw-agent-"+tc.name+"?mode=memory&cache=shared")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			owner, err := auth.NewAuthService(db).CreateUser(ctx, "raw-owner-"+tc.name, "owner-pass", "user")
			if err != nil {
				t.Fatal(err)
			}
			agentID := "raw-agent"
			if err := db.Agents().Create(ctx, storage.Agent{ID: agentID, Name: agentID, OwnerUserID: owner.ID, Enabled: true}); err != nil {
				t.Fatal(err)
			}
			credentials := auth.NewCredentialService(db)
			token, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeAgent, OwnerUserID: owner.ID, AgentID: agentID})
			if err != nil {
				t.Fatal(err)
			}
			_ = credentials.Close()

			listen := freeLoopbackAddr(t)
			transports := config.ServerAgentTransportsConfig{TCP: config.ServerAgentListenerConfig{Listen: listen}}
			var roots *x509.CertPool
			scheme := "tcp"
			if tc.tls {
				certFile, keyFile, pool := selfSignedForLoopback(t)
				roots = pool
				scheme = "tls"
				transports.TCP.TLSCertFile = certFile
				transports.TCP.TLSKeyFile = keyFile
			}
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
				agentErr <- agent.RunWebSocketWithOptions(agentCtx, scheme+"://"+listen, token.Secret, agentID, "node-raw", 1,
					agent.NewMetadataCollector(nil), nil, agent.WebSocketRunOptions{TLSRootCAs: roots})
			}()

			deadline := time.Now().Add(5 * time.Second)
			for {
				metadata, getErr := db.Metadata().Get(ctx, agentID)
				if getErr == nil && metadata.AgentID == agentID {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("agent never registered over %s: err=%v", scheme, getErr)
				}
				time.Sleep(20 * time.Millisecond)
			}
			agentCancel()
			<-agentErr
		})
	}
}

// TestRawTransportRefusesWrongToken checks that a bad token never registers an agent.
func TestRawTransportRefusesWrongToken(t *testing.T) {
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, "file:raw-agent-wrong-token?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	listen := freeLoopbackAddr(t)
	runtime, err := NewServerRuntime(db, AgentSessionConfig{}, RuntimeConfig{AgentTransports: config.ServerAgentTransportsConfig{TCP: config.ServerAgentListenerConfig{Listen: listen}}})
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

	agentCtx, agentCancel := context.WithTimeout(ctx, 800*time.Millisecond)
	defer agentCancel()
	err = agent.RunWebSocketWithOptions(agentCtx, "tcp://"+listen, "tm_not_a_real_token", "ghost", "node-ghost", 1,
		agent.NewMetadataCollector(nil), nil, agent.WebSocketRunOptions{})
	if err == nil {
		t.Fatal("an unknown token must not be accepted")
	}
	if _, getErr := db.Metadata().Get(ctx, "ghost"); getErr == nil {
		t.Fatal("a refused agent must not have metadata persisted")
	}
}
