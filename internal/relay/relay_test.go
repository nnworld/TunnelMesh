package relay

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"testing"
	"time"
)

func TestGRPCRelayRequiresMTLSAndBuildsConcreteAdapter(t *testing.T) {
	if _, err := NewGRPCRelayTransport(func(context.Context, StreamRequest) (io.ReadWriteCloser, error) { return nil, nil }, &tls.Config{}); err == nil {
		t.Fatal("expected mTLS validation")
	}
	cfg := &tls.Config{Certificates: []tls.Certificate{{}}, RootCAs: x509.NewCertPool()}
	tr, err := NewGRPCRelayTransport(func(context.Context, StreamRequest) (io.ReadWriteCloser, error) { return nopConn{}, nil }, cfg)
	if err != nil || tr == nil {
		t.Fatalf("adapter=%v", err)
	}
}

type fakeNode struct {
	open int
	fail bool
}

func (n *fakeNode) OpenStream(ctx context.Context, req StreamRequest) (io.ReadWriteCloser, error) {
	if n.fail {
		return nil, ErrNodeDisconnected
	}
	n.open++
	return nopConn{}, nil
}
func (n *fakeNode) Close() error { return nil }

type nopConn struct{}

func (nopConn) Read([]byte) (int, error)    { return 0, io.EOF }
func (nopConn) Write(p []byte) (int, error) { return len(p), nil }
func (nopConn) Close() error                { return nil }
func TestRelayEpochValidationAndNodeDisconnect(t *testing.T) {
	n := &fakeNode{}
	r := NewRelayService()
	r.RegisterNode("n1", 3, n)
	if _, err := r.OpenStream(context.Background(), StreamRequest{NodeID: "n1", Epoch: 2}); !errors.Is(err, ErrEpoch) {
		t.Fatalf("%v", err)
	}
	if _, err := r.OpenStream(context.Background(), StreamRequest{NodeID: "n1", Epoch: 3}); err != nil {
		t.Fatal(err)
	}
	r.UnregisterNode("n1")
	if _, err := r.OpenStream(context.Background(), StreamRequest{NodeID: "n1", Epoch: 3}); !errors.Is(err, ErrNodeDisconnected) {
		t.Fatalf("%v", err)
	}
}

func TestRelayRegisterNodeRejectsStaleEpoch(t *testing.T) {
	r := NewRelayService()
	old, newer := &fakeNode{}, &fakeNode{}
	r.RegisterNode("n", 3, newer)
	r.RegisterNode("n", 2, old)
	if _, err := r.OpenStream(context.Background(), StreamRequest{NodeID: "n", Epoch: 3}); err != nil {
		t.Fatalf("stale registration replaced owner: %v", err)
	}
	if newer.open != 1 || old.open != 0 {
		t.Fatalf("old=%d newer=%d", old.open, newer.open)
	}
}

type errorConn struct{ closed bool }

func (c *errorConn) Read([]byte) (int, error)  { return 0, io.EOF }
func (c *errorConn) Write([]byte) (int, error) { return 0, errors.New("write failed") }
func (c *errorConn) Close() error              { c.closed = true; return nil }
func TestBoundedTransportPropagatesWriteErrorAndRejectsAfterClose(t *testing.T) {
	c := &errorConn{}
	tr := NewBoundedTransport(c, 1)
	if _, err := tr.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	_ = tr.Close()
	if _, err := tr.Write([]byte("y")); err == nil {
		t.Fatalf("err=%v", err)
	}
}
