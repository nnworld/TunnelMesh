package reconnect

import (
	"errors"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

func rejected() error { return &websocket.DialError{Err: websocket.ErrBadStatus} }

func TestDelayStaysWithinHalfToFullCeiling(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for attempt := 1; attempt <= 10; attempt++ {
		got := Delay(time.Second, 30*time.Second, attempt, rng)
		want := time.Second << (attempt - 1)
		if want > 30*time.Second {
			want = 30 * time.Second
		}
		if got < want/2 || got > want {
			t.Fatalf("Delay(attempt=%d) = %v, want within [%v, %v]", attempt, got, want/2, want)
		}
	}
}

func TestAfterSessionResetsOnlyForStableSessions(t *testing.T) {
	b := New(time.Second, 30*time.Second, rand.New(rand.NewSource(1)))
	for i := 0; i < 8; i++ {
		b.AfterDialFailure(errors.New("dial"))
	}
	if got := b.AfterSession(time.Second); got < 15*time.Second {
		t.Fatalf("short session delay = %v, want backoff to keep growing", got)
	}
	if got := b.AfterSession(StableAfter); got > time.Second {
		t.Fatalf("stable session delay = %v, want a fresh first-attempt delay", got)
	}
}

func TestRepeatedRejectionRaisesCeilingAndOtherErrorsLowerIt(t *testing.T) {
	b := New(time.Second, 30*time.Second, rand.New(rand.NewSource(1)))
	var last time.Duration
	for i := 0; i < 12; i++ {
		last = b.AfterDialFailure(rejected())
	}
	if last <= 30*time.Second || last > RejectedMaxBackoff {
		t.Fatalf("rejected delay = %v, want within (30s, %v]", last, RejectedMaxBackoff)
	}
	if got := b.AfterDialFailure(errors.New("connection refused")); got > 30*time.Second {
		t.Fatalf("non-rejection delay = %v, want back under the normal ceiling", got)
	}
}

func TestIsRejectedRecognisesTransportNeutralRejection(t *testing.T) {
	rejected := fmt.Errorf("preface refused: %w", &RejectedError{Reason: "unauthorized"})
	if !IsRejected(rejected) {
		t.Fatal("a wrapped RejectedError must count as rejection")
	}
	if !errors.Is(rejected, ErrRejected) {
		t.Fatal("RejectedError must match the ErrRejected sentinel")
	}
	if IsRejected(errors.New("connection reset by peer")) {
		t.Fatal("an ordinary network error must not count as rejection")
	}
	if IsRejected(&RejectedError{}) != true {
		t.Fatal("a bare RejectedError must count as rejection")
	}
}
