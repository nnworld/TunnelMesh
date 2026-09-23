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
)

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
