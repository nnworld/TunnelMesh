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
