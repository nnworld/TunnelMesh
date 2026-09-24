package protocol

// Flow-control defaults shared by the Server relay and the Agent stream
// dispatcher. Keeping them in one place guarantees both ends of a stream agree
// on the credit contract without negotiating a new frame type.
const (
	// MaxStreamFrame is the largest DATA payload the Agent relay reads from a
	// target connection in one frame.
	MaxStreamFrame = 32 << 10

	// DefaultServerReceiveWindow is the credit the Server advertises in
	// OPEN_STREAM when the caller does not request one. It must equal the
	// relay stream inbound buffer (16 frames of MaxStreamFrame) so a peer that
	// respects the window can never overflow it.
	DefaultServerReceiveWindow = 16 * MaxStreamFrame

	// DefaultAgentReceiveWindow is the credit the Agent grants the Server for
	// Server-to-Agent DATA. The Server mirrors it as the initial send window
	// because the Agent enforces it regardless of what the Server believes.
	DefaultAgentReceiveWindow = 256 << 10

	// DefaultWindowUpdateThreshold is how many consumed bytes accumulate
	// before a WINDOW_UPDATE is emitted, on either end.
	DefaultWindowUpdateThreshold = 128 << 10

	// MinRefillableWindow is the smallest advertised window a receiver can be
	// trusted with: even with the largest possible unacknowledged residue, the
	// sender must still be able to emit one whole frame.
	MinRefillableWindow = DefaultWindowUpdateThreshold + MaxStreamFrame
)

// ShouldFlushWindowUpdate reports whether a receiver must return credit now.
// The threshold amortizes control traffic, but it must never be the only rule:
// a sender whose remaining window is below one whole frame cannot make progress
// and has no way to ask for credit, so the residue is flushed early instead.
// Without this, a stream whose window drains just below the threshold stalls
// forever, because every pump in this codebase waits without a timeout.
func ShouldFlushWindowUpdate(remaining uint32, unacked uint32, threshold uint32) bool {
	if unacked == 0 {
		return false
	}
	return unacked >= threshold || remaining < MaxStreamFrame
}

// NegotiateReceiveWindow turns a peer-advertised receive window into one this
// process will actually honour. Zero and undersized windows are rejected
// because a sender that can never be refilled below one frame deadlocks the
// stream, and oversized windows are capped because the buffer that enforces the
// window is sized from it. Both ends call this single helper so an Agent and a
// Server can never disagree about what an advertised number means.
func NegotiateReceiveWindow(advertised uint32) uint32 {
	if advertised < MinRefillableWindow || advertised > DefaultServerReceiveWindow {
		return DefaultServerReceiveWindow
	}
	return advertised
}

// Every relay pump in this codebase consumes send credit before it hands a frame
// to its outbound queue, and a peer only returns credit after it has taken the
// bytes. Queued bytes therefore can never exceed the credit the peer advertised
// when the stream opened:
//
//	queued = consumed - sent  <=  initialWindow + updates - sent  <=  initialWindow
//
// because updates are bounded by what the peer has already received. That makes
// "per-stream outbound queue >= the window the peer advertises" the invariant
// which keeps a full queue unreachable for a compliant peer. It matters because
// the outbound queues refuse frames instead of blocking, and every pump treats a
// refusal as a fatal stream error: sizing a queue below the credit does not add
// safety, it turns ordinary backpressure into a truncated transfer. The Server
// mirrors the same rule on its inbound side, where the relay stream's
// receiveBudget equals the window it advertised in OPEN_STREAM.
