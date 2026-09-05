package relay

import (
	"context"
	"errors"
	"io"
	"testing"
)

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
