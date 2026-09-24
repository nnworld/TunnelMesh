package client

import (
	"errors"
	"testing"
	"time"
)

// TestServeSignalCloseReleasesWatchers pins the shutdown contract: a listener that
// was deliberately closed must not be reported as a failure, and a watcher blocked
// on Err() has to be released or every reconnect leaks one goroutine.
func TestServeSignalCloseReleasesWatchers(t *testing.T) {
	signal := newServeSignal()
	errs := signal.Err()
	go func() {
		err, open := <-errs
		if open || err != nil {
			t.Errorf("closed channel reported %v", err)
		}
	}()
	signal.close()
	select {
	case err, open := <-errs:
		if open || err != nil {
			t.Fatalf("clean close must not carry an error, got %v (open=%v)", err, open)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("close did not release the watcher")
	}
	// A late publish races with shutdown; dropping is the only safe answer.
	signal.close()
	signal.publish(errors.New("accept failed"))
	if err := <-signal.Err(); err != nil {
		t.Fatalf("publish after close must be dropped, got %v", err)
	}
}

func TestServeSignalPublishesOneFailure(t *testing.T) {
	signal := newServeSignal()
	signal.publish(nil)
	if signal.reported() {
		t.Fatal("a nil error is a clean stop and must not be marked as reported")
	}
	want := errors.New("bind: address already in use")
	signal.publish(want)
	signal.publish(errors.New("second failure"))
	select {
	case err := <-signal.Err():
		if !errors.Is(err, want) {
			t.Fatalf("Err() = %v, want %v", err, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("publish did not reach the watcher")
	}
	if !signal.reported() {
		t.Fatal("reported must be true once a failure was published")
	}
}
