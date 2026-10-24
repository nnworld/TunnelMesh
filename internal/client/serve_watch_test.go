package client

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSuperviseRunDoesNotCancelSessionPoolOnListenerFailure(t *testing.T) {
	watcher := NewForwardServeWatcher(1)
	listenerErr := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watcher.Watch(ctx, "tcp tunnel", nil, listenerErr)
	listenerErr <- errors.New("listener closed")

	finished := make(chan error, 1)
	go func() {
		finished <- SuperviseRun(func() error {
			time.Sleep(40 * time.Millisecond)
			return nil
		}, watcher)
	}()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("SuperviseRun() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SuperviseRun cancelled the session pool after a listener failure")
	}
}
