package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"sync"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// The reply statuses the engine can produce. They are aliases of the wire
// enumeration rather than a second list, so the agent cannot report a status the
// server has no error_class for.
const (
	StatusEchoOK                = protocol.ICMPEchoStatusOK
	StatusEchoTimeout           = protocol.ICMPEchoStatusTimeout
	StatusEchoCapacityExhausted = protocol.ICMPEchoStatusCapacityExhausted
	StatusEchoUnreachable       = protocol.ICMPEchoStatusUnreachable
	StatusEchoUnsupported       = protocol.ICMPEchoStatusUnsupported
	StatusEchoCancelled         = protocol.ICMPEchoStatusCancelled
)

const (
	defaultEchoTimeout       = 5 * time.Second
	defaultEchoMaxConcurrent = 64
	defaultEchoBindAddress   = "0.0.0.0"
)

var (
	// ErrEchoerClosed is returned to every in-flight and subsequent echo once
	// the engine is closed, so a shutting-down agent answers its streams instead
	// of leaving them to time out on the server.
	ErrEchoerClosed = errors.New("agent: icmp echo engine is closed")
	// ErrPingGroupRangeRequired names the one host setting an unprivileged ping
	// socket depends on. Reporting it as a plain permission error would leave the
	// operator guessing which sysctl to change.
	ErrPingGroupRangeRequired = errors.New("agent: cannot open an unprivileged icmp socket because net.ipv4.ping_group_range does not include this process gid")
	// ErrEchoTargetRejected covers the address families and ranges no VPN echo
	// can legitimately address.
	ErrEchoTargetRejected = errors.New("agent: icmp echo target rejected")
	errEchoSequenceSpace  = errors.New("agent: no free icmp sequence")
)

// ICMPPacketConn is the slice of *icmp.PacketConn the engine uses. Injecting it
// is what makes the correlation, timeout and budget rules testable without
// CAP_NET_ADMIN and without net.ipv4.ping_group_range on the test host.
type ICMPPacketConn interface {
	ReadFrom(b []byte) (int, net.Addr, error)
	WriteTo(b []byte, dst net.Addr) (int, error)
	SetReadDeadline(t time.Time) error
	LocalAddr() net.Addr
	Close() error
}

// listenICMPPacket is a variable so a test can reproduce the permission failure
// a real host produces when ping_group_range excludes the process gid.
var listenICMPPacket = func(network, address string) (ICMPPacketConn, error) {
	conn, err := icmp.ListenPacket(network, address)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

// EchoerConfig bounds one agent's echo handling. MaxConcurrent is a process-wide
// budget and deliberately independent of the server's per-peer limit: the server
// cannot see the other peers an agent serves, so this is the last line of
// defence against one fleet of VPN peers turning an agent into a reflector.
type EchoerConfig struct {
	BindAddress   string
	Timeout       time.Duration
	MaxConcurrent int
}

func (c *EchoerConfig) normalize() {
	if c.BindAddress == "" {
		c.BindAddress = defaultEchoBindAddress
	}
	if c.Timeout <= 0 {
		c.Timeout = defaultEchoTimeout
	}
	if c.MaxConcurrent <= 0 {
		c.MaxConcurrent = defaultEchoMaxConcurrent
	}
}

// EchoRequest is one echo the server asked for. Identifier and Sequence belong
// to the VPN peer and are returned untouched; the wire sequence the internal
// host sees is allocated by the engine because the kernel rewrites the
// identifier of an unprivileged ping socket.
type EchoRequest struct {
	CorrelationID string
	Target        netip.Addr
	Identifier    uint16
	Sequence      uint16
	Data          []byte
}

// EchoReply is the outcome. A timeout, an exhausted budget and an unreachable
// host are answers rather than errors: IP has no error channel, and the server
// needs the identifiers back to answer the peer even when the answer is "nothing
// came back".
type EchoReply struct {
	Identifier uint16
	Sequence   uint16
	Data       []byte
	Status     string
	RTT        time.Duration
}

type pendingEcho struct {
	correlationID string
	identifier    uint16
	sequence      uint16
	data          []byte
	startedAt     time.Time
	reply         chan EchoReply
}

// Echoer owns one ping socket and demultiplexes every echo in flight over it.
type Echoer struct {
	conn          ICMPPacketConn
	timeout       time.Duration
	maxConcurrent int
	localID       int

	mu        sync.Mutex
	pending   map[uint16]*pendingEcho
	nextSeq   uint16
	closed    bool
	brokenErr error

	closeOnce sync.Once
	done      chan struct{}
}

// NewEchoer starts the read loop over an existing socket. It never fails: the
// socket is the caller's responsibility, which is what lets a test inject one.
func NewEchoer(conn ICMPPacketConn, cfg EchoerConfig) *Echoer {
	cfg.normalize()
	echoer := &Echoer{
		conn: conn, timeout: cfg.Timeout, maxConcurrent: cfg.MaxConcurrent,
		localID: os.Getpid() & 0xffff,
		pending: make(map[uint16]*pendingEcho), nextSeq: 1,
		done: make(chan struct{}),
	}
	go echoer.readLoop()
	return echoer
}

// OpenEchoer creates the unprivileged ping socket and starts the engine. A
// permission failure is reported as ErrPingGroupRangeRequired with the sysctl
// named, because a silent fallback would leave the operator with an agent that
// negotiates nothing and no clue why.
func OpenEchoer(cfg EchoerConfig) (*Echoer, error) {
	cfg.normalize()
	conn, err := listenICMPPacket("udp4", cfg.BindAddress)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return nil, fmt.Errorf("%w: listen udp4 %s: %v (set it with: sysctl -w net.ipv4.ping_group_range='0 2147483647')", ErrPingGroupRangeRequired, cfg.BindAddress, err)
		}
		return nil, fmt.Errorf("agent: open icmp ping socket on %s: %w", cfg.BindAddress, err)
	}
	return NewEchoer(conn, cfg), nil
}

// Send transmits one echo and waits for its reply, its timeout, ctx, or the
// engine closing. It never blocks longer than the configured timeout.
func (e *Echoer) Send(ctx context.Context, request EchoRequest) (EchoReply, error) {
	if e == nil {
		return EchoReply{}, ErrEchoerClosed
	}
	target, err := validateEchoTarget(request.Target)
	if err != nil {
		return EchoReply{}, err
	}

	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return EchoReply{}, e.failureLocked()
	}
	if len(e.pending) >= e.maxConcurrent {
		e.mu.Unlock()
		// Refusing at once is the point of an agent-side budget: queueing would
		// let one peer's flood delay every other peer's echoes.
		return EchoReply{Identifier: request.Identifier, Sequence: request.Sequence, Status: StatusEchoCapacityExhausted}, nil
	}
	sequence, err := e.allocateSequenceLocked()
	if err != nil {
		e.mu.Unlock()
		return EchoReply{}, err
	}
	entry := &pendingEcho{
		correlationID: request.CorrelationID, identifier: request.Identifier, sequence: request.Sequence,
		data: append([]byte(nil), request.Data...), startedAt: time.Now(), reply: make(chan EchoReply, 1),
	}
	e.pending[sequence] = entry
	e.mu.Unlock()

	message := icmp.Message{Type: ipv4.ICMPTypeEcho, Code: 0, Body: &icmp.Echo{ID: e.localID, Seq: int(sequence), Data: entry.data}}
	encoded, err := message.Marshal(nil)
	if err != nil {
		e.release(sequence)
		return EchoReply{}, fmt.Errorf("agent: encode icmp echo: %w", err)
	}
	if _, err := e.conn.WriteTo(encoded, &net.UDPAddr{IP: target.AsSlice()}); err != nil {
		e.release(sequence)
		e.mu.Lock()
		failure := e.failureLocked()
		e.mu.Unlock()
		if failure != nil {
			return EchoReply{}, failure
		}
		// The host did not accept the datagram. That is an answer about the
		// target, not a malfunction of the engine.
		return EchoReply{Identifier: request.Identifier, Sequence: request.Sequence, Status: StatusEchoUnreachable}, nil
	}

	timer := time.NewTimer(e.timeout)
	defer timer.Stop()
	select {
	case reply := <-entry.reply:
		return reply, nil
	case <-timer.C:
		// Losing the race with a reply that arrived in the same instant is
		// possible; delivering it is better than reporting a timeout.
		if e.release(sequence) {
			return EchoReply{Identifier: request.Identifier, Sequence: request.Sequence, Status: StatusEchoTimeout}, nil
		}
		select {
		case reply := <-entry.reply:
			return reply, nil
		default:
			return EchoReply{Identifier: request.Identifier, Sequence: request.Sequence, Status: StatusEchoTimeout}, nil
		}
	case <-ctx.Done():
		e.release(sequence)
		return EchoReply{}, ctx.Err()
	case <-e.done:
		e.release(sequence)
		e.mu.Lock()
		failure := e.failureLocked()
		e.mu.Unlock()
		if failure == nil {
			failure = ErrEchoerClosed
		}
		return EchoReply{}, failure
	}
}

// InFlight reports the echoes currently waiting, which is what an operator and a
// test need to see to know the budget is being released.
func (e *Echoer) InFlight() int {
	if e == nil {
		return 0
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.pending)
}

// Close stops the engine. It unblocks the read loop and lets it fail every
// in-flight echo, which is why it does not wait for the loop itself: a socket
// that cannot be closed must not be able to hang agent shutdown.
func (e *Echoer) Close() error {
	if e == nil {
		return nil
	}
	var err error
	e.closeOnce.Do(func() {
		e.mu.Lock()
		e.closed = true
		e.mu.Unlock()
		_ = e.conn.SetReadDeadline(time.Now())
		err = e.conn.Close()
	})
	return err
}

func (e *Echoer) readLoop() {
	defer close(e.done)
	buffer := make([]byte, protocol.MaxDatagram)
	for {
		count, _, err := e.conn.ReadFrom(buffer)
		if err != nil {
			e.mu.Lock()
			closed := e.closed
			e.mu.Unlock()
			if closed {
				e.failAll(nil)
				return
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			e.failAll(fmt.Errorf("agent: icmp read loop stopped: %w", err))
			return
		}
		e.handleDatagram(buffer[:count])
	}
}

func (e *Echoer) handleDatagram(raw []byte) {
	message, err := icmp.ParseMessage(1, raw)
	if err != nil {
		return
	}
	if message.Type != ipv4.ICMPTypeEchoReply {
		// Anything that is not an echo reply belongs to the data plane's
		// counters, not to this engine: only echo was ever sent.
		return
	}
	body, ok := message.Body.(*icmp.Echo)
	if !ok {
		return
	}
	sequence := uint16(body.Seq)
	e.mu.Lock()
	entry := e.pending[sequence]
	if entry == nil {
		e.mu.Unlock()
		return
	}
	// The sequence space is 16 bits and wraps, so a late or forged datagram can
	// land on a live association. The payload is the tie-breaker, and comparing
	// it costs nothing because it is already in memory.
	if !bytes.Equal(entry.data, body.Data) {
		e.mu.Unlock()
		return
	}
	delete(e.pending, sequence)
	e.mu.Unlock()

	entry.reply <- EchoReply{
		Identifier: entry.identifier, Sequence: entry.sequence, Data: body.Data,
		Status: StatusEchoOK, RTT: time.Since(entry.startedAt),
	}
}

// failAll drops every association and records why. Senders wake on e.done, so
// nothing has to be delivered to their reply channels here; the cause is what
// turns "the engine stopped" into an error an operator can act on.
func (e *Echoer) failAll(cause error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if cause == nil {
		cause = ErrEchoerClosed
	}
	if e.brokenErr == nil {
		e.brokenErr = cause
	}
	e.pending = make(map[uint16]*pendingEcho)
}

func (e *Echoer) release(sequence uint16) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.pending[sequence]; !ok {
		return false
	}
	delete(e.pending, sequence)
	return true
}

func (e *Echoer) allocateSequenceLocked() (uint16, error) {
	for attempt := 0; attempt < 1<<16; attempt++ {
		candidate := e.nextSeq
		e.nextSeq++
		if _, busy := e.pending[candidate]; !busy {
			return candidate, nil
		}
	}
	return 0, errEchoSequenceSpace
}

// failureLocked reports why the engine stopped, if it did. Callers hold e.mu.
func (e *Echoer) failureLocked() error {
	if e.brokenErr != nil {
		return e.brokenErr
	}
	if e.closed {
		return ErrEchoerClosed
	}
	return nil
}

// validateEchoTarget keeps the engine free of routing policy while still
// refusing addresses no VPN echo can mean. Private and loopback targets are
// deliberately allowed here: whether a peer may reach them is the server's
// per-packet policy, evaluated before a stream is ever opened.
func validateEchoTarget(target netip.Addr) (netip.Addr, error) {
	if !target.IsValid() {
		return netip.Addr{}, fmt.Errorf("%w: address is not valid", ErrEchoTargetRejected)
	}
	address := target.Unmap()
	if !address.Is4() {
		return netip.Addr{}, fmt.Errorf("%w: only ipv4 echo is supported", ErrEchoTargetRejected)
	}
	if address.IsUnspecified() {
		return netip.Addr{}, fmt.Errorf("%w: unspecified address", ErrEchoTargetRejected)
	}
	if address.IsMulticast() {
		return netip.Addr{}, fmt.Errorf("%w: multicast address", ErrEchoTargetRejected)
	}
	if address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() {
		// 169.254.0.0/16 is where cloud metadata lives. The server refuses it too,
		// and refusing it here means a compromised control plane cannot use the
		// agent as a metadata oracle.
		return netip.Addr{}, fmt.Errorf("%w: link-local address", ErrEchoTargetRejected)
	}
	return address, nil
}
