package agent

import (
	"context"
	"encoding/json"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

// openOutcome is the observable half of the OPEN_STREAM handshake: the Agent
// either accepted the open or refused it with a stable code. Asserting on the
// frame instead of on "nothing happened within N ms" keeps the capacity tests
// honest under -race and a loaded machine, where a wall-clock budget is the only
// thing that can expire by accident.
type openOutcome struct {
	streamID uint32
	accepted bool
	code     protocol.OpenResultCode
}

// streamCapacityFixture returns a dispatcher whose dials are counted and whose
// OPEN_RESULT frames are captured. maxActive is the level ceiling under test.
func streamCapacityFixture(t *testing.T, maxActive int) (*StreamDispatcher, *atomic.Int32, chan openOutcome) {
	t.Helper()
	dialed := &atomic.Int32{}
	results := make(chan openOutcome, 8)
	d := NewStreamDispatcherWithSender(
		Dialer{Policy: func(context.Context, string, string, int) error { return nil }},
		func(context.Context, string, string, int) (io.ReadWriteCloser, error) {
			dialed.Add(1)
			return newDirectionalDispatcherConn(), nil
		},
		func(f protocol.Frame) error {
			if f.Type != protocol.FrameOpenResult {
				return nil
			}
			payload, err := protocol.DecodeOpenResultPayload(f.Payload)
			if err != nil {
				return nil
			}
			results <- openOutcome{streamID: f.StreamID, accepted: payload.Accepted, code: payload.Code}
			return nil
		},
	)
	// Strict mode is what makes a refusal an explicit OPEN_RESULT rather than a
	// bare RESET, so the refusal reason (queue_full) is part of the assertion.
	d.SetOpenResultEnabled(true)
	d.SetMaxActiveStreams(maxActive)
	t.Cleanup(func() { _ = d.Close() })
	return d, dialed, results
}

// openStream sends one OPEN_STREAM frame for id. Every stream in these tests is
// opened and awaited one at a time, because the shared dial executor has its own
// small queue: this is about the level ceiling, not about queue depth.
func openStream(t *testing.T, d *StreamDispatcher, id uint32) {
	t.Helper()
	payload, err := json.Marshal(StreamOpenPayload{Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22})
	if err != nil {
		t.Fatal(err)
	}
	frame := protocol.Frame{
		Version:  protocol.CurrentVersion,
		Type:     protocol.FrameOpenStream,
		StreamID: id,
		Flags:    protocol.FlagStrictOpen,
		Payload:  payload,
	}
	if err := d.Handle(frame); err != nil {
		t.Fatalf("handle OPEN_STREAM %d: %v", id, err)
	}
}

// awaitOpen blocks for the outcome of id. The deadline is a safety net for a
// hung dispatcher, not a timing assumption: a healthy Agent answers as soon as
// the dial completes.
func awaitOpen(t *testing.T, results chan openOutcome, id uint32) openOutcome {
	t.Helper()
	select {
	case got := <-results:
		if got.streamID != id {
			t.Fatalf("OPEN_RESULT for stream %d, want %d", got.streamID, id)
		}
		return got
	case <-time.After(30 * time.Second):
		t.Fatalf("stream %d never got an OPEN_RESULT", id)
		return openOutcome{}
	}
}

// TestStreamDispatcherRefusesBeyondMaxActiveStreams guards the ceiling that the
// dial queue does not provide: max_concurrent_dials bounds how many dials are in
// flight, but a completed dial keeps its stream and its target connection, so a
// single peer can accumulate unbounded live streams through one Agent.
func TestStreamDispatcherRefusesBeyondMaxActiveStreams(t *testing.T) {
	d, dialed, results := streamCapacityFixture(t, 2)

	for id := uint32(1); id <= 2; id++ {
		openStream(t, d, id)
		got := awaitOpen(t, results, id)
		if !got.accepted || got.code != protocol.OpenResultCodeOK {
			t.Fatalf("stream %d outcome = %+v, want an accepted open below the ceiling", id, got)
		}
	}
	if got := d.ActiveStreams(); got != 2 {
		t.Fatalf("ActiveStreams() = %d, want the two opens below the ceiling", got)
	}

	openStream(t, d, 3)
	got := awaitOpen(t, results, 3)
	if got.accepted {
		t.Fatalf("stream 3 was accepted above the ceiling of 2: %+v", got)
	}
	if got.code != protocol.OpenResultCodeQueueFull {
		t.Fatalf("stream 3 refused with code %q, want %q so the client knows to retry",
			got.code, protocol.OpenResultCodeQueueFull)
	}
	// The ceiling is checked before any pending state or dial is created, so a
	// refusal cannot leave a target connection behind.
	if n := dialed.Load(); n != 2 {
		t.Fatalf("dials = %d, want the refused open to have dialled nothing", n)
	}
	if got := d.ActiveStreams(); got != 2 {
		t.Fatalf("ActiveStreams() = %d, want the ceiling of 2 to hold", got)
	}
}

// TestStreamDispatcherUnlimitedByDefault keeps the guard opt-out honest: a zero
// ceiling must not refuse anything, because that is the documented meaning of 0.
func TestStreamDispatcherUnlimitedByDefault(t *testing.T) {
	d, dialed, results := streamCapacityFixture(t, 0)

	for id := uint32(1); id <= 3; id++ {
		openStream(t, d, id)
		got := awaitOpen(t, results, id)
		if !got.accepted {
			t.Fatalf("a zero ceiling refused stream %d: %+v", id, got)
		}
	}
	if n := dialed.Load(); n != 3 {
		t.Fatalf("dials = %d, want all three opens dialled", n)
	}
	if got := d.ActiveStreams(); got != 3 {
		t.Fatalf("ActiveStreams() = %d, want all three accepted", got)
	}
}
