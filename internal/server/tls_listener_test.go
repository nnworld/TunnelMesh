package server

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func TestNativeTLSDisabledPreservesInternalHTTPListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	wrapped, err := nativeTLSListener(listener, config.TLSConfig{Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	if wrapped != listener {
		t.Fatal("disabled native TLS replaced the internal HTTP listener")
	}
}

func TestNativeTLSRejectsMismatchedCertificateAndKeyBeforeServing(t *testing.T) {
	certFile, _, _ := writeNativeTLSCertificate(t)
	_, otherKeyFile, _ := writeNativeTLSCertificate(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	db, err := storage.OpenSQLite(context.Background(), "file:native-tls-mismatch?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	runtime, err := NewServerRuntime(db, AgentSessionConfig{}, RuntimeConfig{TLS: config.TLSConfig{Enabled: true, CertFile: certFile, KeyFile: otherKeyFile, MinVersion: "1.2"}})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	if err := runtime.ServeListener(context.Background(), listener); err == nil {
		t.Fatal("ServeListener accepted a mismatched certificate and private key")
	}
	if conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond); err == nil {
		_ = conn.Close()
		t.Fatal("listener remained open after native TLS setup failure")
	}
}

func TestNativeTLSServesTLS12AndRejectsPlaintext(t *testing.T) {
	certFile, keyFile, roots := writeNativeTLSCertificate(t)
	db, err := storage.OpenSQLite(context.Background(), "file:native-tls-runtime?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	runtime, err := NewServerRuntime(db, AgentSessionConfig{}, RuntimeConfig{TLS: config.TLSConfig{Enabled: true, CertFile: certFile, KeyFile: keyFile, MinVersion: "1.2"}})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	serverCtx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- runtime.ServeListener(serverCtx, listener) }()
	defer func() {
		cancel()
		<-serveErr
	}()

	dialer := &net.Dialer{Timeout: time.Second}
	if conn, err := tls.DialWithDialer(dialer, "tcp", address, &tls.Config{RootCAs: roots, ServerName: "localhost", MinVersion: tls.VersionTLS10, MaxVersion: tls.VersionTLS11}); err == nil {
		_ = conn.Close()
		t.Fatal("native TLS accepted a TLS 1.1 client")
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "localhost", MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12}}}
	response, err := client.Get("https://" + address + "/")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("TLS 1.2 response status = %d, want %d", response.StatusCode, http.StatusOK)
	}

	plain, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	if _, err := plain.Write([]byte("GET / HTTP/1.1\r\nHost: " + address + "\r\nConnection: close\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	_ = plain.SetReadDeadline(time.Now().Add(time.Second))
	line, _ := bufio.NewReader(plain).ReadString('\n')
	if strings.Contains(line, "200 OK") {
		t.Fatalf("native TLS listener served plaintext HTTP: %q", line)
	}
}

func TestNativeTLS13RejectsTLS12(t *testing.T) {
	certFile, keyFile, roots := writeNativeTLSCertificate(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	wrapped, err := nativeTLSListener(listener, config.TLSConfig{Enabled: true, CertFile: certFile, KeyFile: keyFile, MinVersion: "1.3"})
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(wrapped) }()
	defer func() {
		_ = server.Close()
		<-serveErr
	}()
	dialer := &net.Dialer{Timeout: time.Second}
	if conn, err := tls.DialWithDialer(dialer, "tcp", listener.Addr().String(), &tls.Config{RootCAs: roots, ServerName: "localhost", MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12}); err == nil {
		_ = conn.Close()
		t.Fatal("TLS 1.3 minimum accepted a TLS 1.2 client")
	}
	conn, err := tls.DialWithDialer(dialer, "tcp", listener.Addr().String(), &tls.Config{RootCAs: roots, ServerName: "localhost", MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13})
	if err != nil {
		t.Fatalf("TLS 1.3 client failed: %v", err)
	}
	_ = conn.Close()
}

func writeNativeTLSCertificate(t *testing.T) (string, string, *x509.CertPool) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(20260906),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile := filepath.Join(dir, "server.crt")
	keyFile := filepath.Join(dir, "server.key")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600); err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(certificate)
	return certFile, keyFile, roots
}

func TestPreflightNativeTLSValidatesMaterialWithoutBinding(t *testing.T) {
	certFile, keyFile, _ := writeNativeTLSCertificate(t)
	_, otherKeyFile, _ := writeNativeTLSCertificate(t)

	if err := PreflightNativeTLS(config.TLSConfig{Enabled: false}); err != nil {
		t.Fatalf("disabled native TLS should be a no-op, got %v", err)
	}
	if err := PreflightNativeTLS(config.TLSConfig{Enabled: true, CertFile: certFile, KeyFile: keyFile, MinVersion: "1.2"}); err != nil {
		t.Fatalf("valid material failed preflight: %v", err)
	}
	for name, cfg := range map[string]config.TLSConfig{
		"missing files":       {Enabled: true, MinVersion: "1.2"},
		"unsupported version": {Enabled: true, CertFile: certFile, KeyFile: keyFile, MinVersion: "1.0"},
		"unreadable cert":     {Enabled: true, CertFile: filepath.Join(t.TempDir(), "absent.crt"), KeyFile: keyFile, MinVersion: "1.3"},
		"mismatched pair":     {Enabled: true, CertFile: certFile, KeyFile: otherKeyFile, MinVersion: "1.2"},
	} {
		if err := PreflightNativeTLS(cfg); err == nil {
			t.Fatalf("%s: expected preflight to fail", name)
		}
	}
	// A mismatched pair must keep the wording the CLI startup contract asserts on.
	err := PreflightNativeTLS(config.TLSConfig{Enabled: true, CertFile: certFile, KeyFile: otherKeyFile, MinVersion: "1.2"})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "native tls certificate") {
		t.Fatalf("mismatched pair error = %v, want it to mention the native TLS certificate", err)
	}
}
