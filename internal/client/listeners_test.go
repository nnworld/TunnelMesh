package client

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

// failingListener models the permanent Accept error that a descriptor limit
// produces: not net.ErrClosed, so the loop cannot treat it as a shutdown.
type failingListener struct {
	err  error
	done chan struct{}
}

func (l *failingListener) Accept() (net.Conn, error) {
	close(l.done)
	return nil, l.err
}
func (l *failingListener) Close() error   { return nil }
func (l *failingListener) Addr() net.Addr { return &net.TCPAddr{} }

// TestTCPListenerPublishesServeError is the other half of the fix for the silent
// dead forward: Serve must not be the only place the failure can be observed.
func TestTCPListenerPublishesServeError(t *testing.T) {
	wanted := errors.New("accept: too many open files")
	ln := &failingListener{err: wanted, done: make(chan struct{})}
	listener, err := newTCPListener(ln, context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Serve(); !errors.Is(err, wanted) {
		t.Fatalf("Serve() = %v, want %v", err, wanted)
	}
	select {
	case <-ln.done:
	case <-time.After(time.Second):
		t.Fatal("Accept was never called")
	}
	select {
	case got := <-listener.Err():
		if !errors.Is(got, wanted) {
			t.Fatalf("Err() = %v, want %v", got, wanted)
		}
	case <-time.After(time.Second):
		t.Fatal("Err() never reported the accept failure")
	}
}

// TestTCPListenerCleanCloseDoesNotReportFailure keeps a normal shutdown from
// looking like an outage.
func TestTCPListenerCleanCloseDoesNotReportFailure(t *testing.T) {
	ln, err := ListenTCP(context.Background(), "127.0.0.1:0", nil)
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- ln.Serve() }()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("Serve() after Close = %v, want a clean nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after Close")
	}
	select {
	case err, ok := <-ln.Err():
		if !ok || err != nil {
			t.Fatalf("Err() = %v (open=%v), want a closed nil-only channel", err, ok)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Err() never closed on a clean shutdown")
	}
}
