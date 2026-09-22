package agent

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// fakeICMPConn stands in for the unprivileged ping socket. The real socket
// cannot be opened in CI (net.ipv4.ping_group_range is not set for the test
// user), and it could not rewrite the identifier on demand either, which is the
// one behaviour the correlation design depends on.
type fakeICMPConn struct {
	mu        sync.Mutex
	written   [][]byte
	dests     []net.Addr
	incoming  chan incomingRead
	readErr   error
	writeErr  error
	closed    bool
	closeChan chan struct{}
}

type incomingRead struct {
	data []byte
	addr net.Addr
	err  error
}

func newFakeICMPConn() *fakeICMPConn {
	return &fakeICMPConn{incoming: make(chan incomingRead, 16), closeChan: make(chan struct{})}
}

func (c *fakeICMPConn) ReadFrom(b []byte) (int, net.Addr, error) {
	select {
	case read := <-c.incoming:
		if read.err != nil {
			return 0, nil, read.err
		}
		return copy(b, read.data), read.addr, nil
	case <-c.closeChan:
		return 0, nil, errors.New("icmp: closed")
	}
}

func (c *fakeICMPConn) WriteTo(b []byte, dst net.Addr) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	c.written = append(c.written, append([]byte(nil), b...))
	c.dests = append(c.dests, dst)
	return len(b), nil
}

func (c *fakeICMPConn) SetReadDeadline(time.Time) error { return nil }
func (c *fakeICMPConn) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(192, 168, 1, 20)}
}
func (c *fakeICMPConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed = true
		close(c.closeChan)
	}
	return nil
}

func (c *fakeICMPConn) writes() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]byte(nil), c.written...)
}

// lastEcho parses the most recent datagram the engine sent and returns the wire
// sequence it chose, which is the only key that survives the kernel.
func (c *fakeICMPConn) lastEcho(t *testing.T) (*icmp.Echo, net.Addr) {
	t.Helper()
	writes := c.writes()
	if len(writes) == 0 {
		t.Fatal("engine wrote nothing")
	}
	return parseEcho(t, writes[len(writes)-1]), c.dests[len(c.dests)-1]
}

func parseEcho(t *testing.T, raw []byte) *icmp.Echo {
	t.Helper()
	message, err := icmp.ParseMessage(1, raw)
	if err != nil {
		t.Fatalf("parse sent message: %v", err)
	}
	if message.Type != ipv4.ICMPTypeEcho {
		t.Fatalf("sent type = %v, want echo request", message.Type)
	}
	body, ok := message.Body.(*icmp.Echo)
	if !ok {
		t.Fatalf("sent body = %T, want *icmp.Echo", message.Body)
	}
	return body
}

// deliverReply pushes an echo reply that a real kernel would have rewritten: a
// different identifier, the wire sequence the engine allocated, and the payload
// verbatim.
func (c *fakeICMPConn) deliverReply(t *testing.T, kernelID, wireSeq int, data []byte) {
	t.Helper()
	message := icmp.Message{Type: ipv4.ICMPTypeEchoReply, Code: 0, Body: &icmp.Echo{ID: kernelID, Seq: wireSeq, Data: data}}
	encoded, err := message.Marshal(nil)
	if err != nil {
		t.Fatal(err)
	}
	c.incoming <- incomingRead{data: encoded, addr: &net.UDPAddr{IP: net.IPv4(10, 0, 0, 5)}}
}

func (c *fakeICMPConn) deliverRaw(data []byte) {
	c.incoming <- incomingRead{data: data, addr: &net.UDPAddr{IP: net.IPv4(10, 0, 0, 5)}}
}

func testEchoer(conn ICMPPacketConn, timeout time.Duration, maxConcurrent int) *Echoer {
	echoer := NewEchoer(conn, EchoerConfig{Timeout: timeout, MaxConcurrent: maxConcurrent})
	return echoer
}

func waitForWrite(t *testing.T, conn *fakeICMPConn, count int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(conn.writes()) >= count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("engine wrote %d datagrams, want %d", len(conn.writes()), count)
}

func TestEchoerCorrelatesBySequenceAfterTheKernelRewritesTheIdentifier(t *testing.T) {
	conn := newFakeICMPConn()
	echoer := testEchoer(conn, time.Second, 8)
	defer echoer.Close()

	result := make(chan EchoReply, 1)
	errs := make(chan error, 1)
	go func() {
		reply, err := echoer.Send(context.Background(), EchoRequest{
			CorrelationID: "echo-1", Target: netip.MustParseAddr("10.0.0.5"),
			Identifier: 4242, Sequence: 7, Data: []byte("tunnelmesh"),
		})
		result <- reply
		errs <- err
	}()

	waitForWrite(t, conn, 1)
	sent, dst := conn.lastEcho(t)
	if string(sent.Data) != "tunnelmesh" {
		t.Fatalf("payload = %q", sent.Data)
	}
	if udpAddr, ok := dst.(*net.UDPAddr); !ok || !udpAddr.IP.Equal(net.IPv4(10, 0, 0, 5)) {
		t.Fatalf("destination = %v", dst)
	}
	// The kernel hands the peer a different identifier than the one it asked
	// for; correlation that depended on it would never match.
	conn.deliverReply(t, 31337, sent.Seq, []byte("tunnelmesh"))

	select {
	case err := <-errs:
		if err != nil {
			t.Fatalf("Send err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Send did not return")
	}
	reply := <-result
	if reply.Status != StatusEchoOK {
		t.Fatalf("status = %q", reply.Status)
	}
	if reply.Identifier != 4242 || reply.Sequence != 7 {
		t.Fatalf("identifiers = %d/%d, want the peer's own 4242/7", reply.Identifier, reply.Sequence)
	}
	if string(reply.Data) != "tunnelmesh" {
		t.Fatalf("data = %q", reply.Data)
	}
	if echoer.InFlight() != 0 {
		t.Fatalf("in flight = %d after a completed echo", echoer.InFlight())
	}
}

func TestEchoerDemultiplexesConcurrentEchoesAndOutOfOrderReplies(t *testing.T) {
	conn := newFakeICMPConn()
	echoer := testEchoer(conn, time.Second, 8)
	defer echoer.Close()

	type outcome struct {
		reply EchoReply
		err   error
	}
	first := make(chan outcome, 1)
	second := make(chan outcome, 1)
	send := func(id string, channel chan outcome, data string) {
		go func() {
			reply, err := echoer.Send(context.Background(), EchoRequest{
				CorrelationID: id, Target: netip.MustParseAddr("10.0.0.5"), Identifier: 1, Sequence: 2, Data: []byte(data),
			})
			channel <- outcome{reply, err}
		}()
	}
	send("echo-a", first, "aaaa")
	send("echo-b", second, "bbbb")
	waitForWrite(t, conn, 2)

	// The two sends race, so the wire sequences are matched up by payload rather
	// than by write order.
	var seqA, seqB int
	for _, raw := range conn.writes() {
		switch body := parseEcho(t, raw); string(body.Data) {
		case "aaaa":
			seqA = body.Seq
		case "bbbb":
			seqB = body.Seq
		}
	}
	if seqA == 0 || seqB == 0 {
		t.Fatal("one of the two echoes was never written")
	}
	if seqA == seqB {
		t.Fatalf("two in-flight echoes share wire sequence %d", seqA)
	}
	// Answer out of order: the second request first.
	conn.deliverReply(t, 31337, seqB, []byte("bbbb"))
	conn.deliverReply(t, 31337, seqA, []byte("aaaa"))

	gotFirst := <-first
	gotSecond := <-second
	if gotFirst.err != nil || gotSecond.err != nil {
		t.Fatalf("errors = %v / %v", gotFirst.err, gotSecond.err)
	}
	if string(gotFirst.reply.Data) != "aaaa" || string(gotSecond.reply.Data) != "bbbb" {
		t.Fatalf("crossed replies: %q / %q", gotFirst.reply.Data, gotSecond.reply.Data)
	}
}

func TestEchoerDiscardsARetryWhosePayloadDoesNotMatchTheRequest(t *testing.T) {
	conn := newFakeICMPConn()
	echoer := testEchoer(conn, 60*time.Millisecond, 8)
	defer echoer.Close()

	done := make(chan EchoReply, 1)
	go func() {
		reply, _ := echoer.Send(context.Background(), EchoRequest{
			CorrelationID: "echo-1", Target: netip.MustParseAddr("10.0.0.5"), Identifier: 1, Sequence: 1, Data: []byte("original"),
		})
		done <- reply
	}()
	waitForWrite(t, conn, 1)
	sent, _ := conn.lastEcho(t)
	// A sequence number is 16 bits and wraps, so a stale or spoofed reply can
	// land on a live association. The payload is the tie-breaker.
	conn.deliverReply(t, 31337, sent.Seq, []byte("forged"))

	select {
	case reply := <-done:
		if reply.Status != StatusEchoTimeout {
			t.Fatalf("status = %q, want timeout after a mismatched payload", reply.Status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Send did not return")
	}
}

func TestEchoerReportsTimeoutWithoutLeakingTheAssociation(t *testing.T) {
	conn := newFakeICMPConn()
	echoer := testEchoer(conn, 40*time.Millisecond, 8)
	defer echoer.Close()

	reply, err := echoer.Send(context.Background(), EchoRequest{
		CorrelationID: "echo-1", Target: netip.MustParseAddr("10.0.0.9"), Identifier: 5, Sequence: 9,
	})
	if err != nil {
		t.Fatalf("a timeout is an answer, not an error: %v", err)
	}
	if reply.Status != StatusEchoTimeout {
		t.Fatalf("status = %q", reply.Status)
	}
	if reply.Identifier != 5 || reply.Sequence != 9 {
		t.Fatalf("identifiers = %d/%d", reply.Identifier, reply.Sequence)
	}
	if echoer.InFlight() != 0 {
		t.Fatalf("in flight = %d after a timeout", echoer.InFlight())
	}
}

func TestEchoerRefusesImmediatelyWhenItsBudgetIsExhausted(t *testing.T) {
	conn := newFakeICMPConn()
	echoer := testEchoer(conn, time.Second, 1)
	defer echoer.Close()

	held := make(chan EchoReply, 1)
	go func() {
		reply, _ := echoer.Send(context.Background(), EchoRequest{CorrelationID: "echo-1", Target: netip.MustParseAddr("10.0.0.5"), Identifier: 1, Sequence: 1})
		held <- reply
	}()
	waitForWrite(t, conn, 1)
	if echoer.InFlight() != 1 {
		t.Fatalf("in flight = %d", echoer.InFlight())
	}

	// Queueing here would turn the server's per-peer limit into a promise the
	// agent does not keep, so the second echo is refused at once.
	start := time.Now()
	reply, err := echoer.Send(context.Background(), EchoRequest{CorrelationID: "echo-2", Target: netip.MustParseAddr("10.0.0.5"), Identifier: 1, Sequence: 2})
	if err != nil {
		t.Fatalf("an exhausted budget is an answer, not an error: %v", err)
	}
	if reply.Status != StatusEchoCapacityExhausted {
		t.Fatalf("status = %q", reply.Status)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatalf("refusal took %v, want immediate", time.Since(start))
	}
	conn.deliverReply(t, 1, parseEcho(t, conn.writes()[0]).Seq, nil)
	if got := <-held; got.Status != StatusEchoOK {
		t.Fatalf("held echo status = %q", got.Status)
	}
}

func TestEchoerSurvivesUnmatchedAndNonEchoDatagrams(t *testing.T) {
	conn := newFakeICMPConn()
	echoer := testEchoer(conn, time.Second, 8)
	defer echoer.Close()

	done := make(chan EchoReply, 1)
	go func() {
		reply, _ := echoer.Send(context.Background(), EchoRequest{CorrelationID: "echo-1", Target: netip.MustParseAddr("10.0.0.5"), Identifier: 8, Sequence: 8, Data: []byte("x")})
		done <- reply
	}()
	waitForWrite(t, conn, 1)
	sent, _ := conn.lastEcho(t)

	unmatched := icmp.Message{Type: ipv4.ICMPTypeEchoReply, Code: 0, Body: &icmp.Echo{ID: 1, Seq: sent.Seq + 32000, Data: []byte("x")}}
	encoded, err := unmatched.Marshal(nil)
	if err != nil {
		t.Fatal(err)
	}
	conn.deliverRaw(encoded)
	conn.deliverRaw([]byte{0x01, 0x02, 0x03})
	destinationUnreachable := icmp.Message{Type: ipv4.ICMPTypeDestinationUnreachable, Code: 1, Body: &icmp.DstUnreach{Data: []byte("unused")}}
	encodedUnreachable, err := destinationUnreachable.Marshal(nil)
	if err != nil {
		t.Fatal(err)
	}
	conn.deliverRaw(encodedUnreachable)

	conn.deliverReply(t, 31337, sent.Seq, []byte("x"))
	select {
	case reply := <-done:
		if reply.Status != StatusEchoOK {
			t.Fatalf("status = %q after unrelated datagrams", reply.Status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Send did not return")
	}
}

func TestEchoerCloseFailsEveryInFlightEcho(t *testing.T) {
	conn := newFakeICMPConn()
	echoer := testEchoer(conn, 5*time.Second, 8)

	errs := make(chan error, 1)
	go func() {
		_, err := echoer.Send(context.Background(), EchoRequest{CorrelationID: "echo-1", Target: netip.MustParseAddr("10.0.0.5"), Identifier: 1, Sequence: 1})
		errs <- err
	}()
	waitForWrite(t, conn, 1)
	if err := echoer.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}
	select {
	case err := <-errs:
		if !errors.Is(err, ErrEchoerClosed) {
			t.Fatalf("in-flight err = %v, want ErrEchoerClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not release the in-flight echo")
	}
	if _, err := echoer.Send(context.Background(), EchoRequest{CorrelationID: "echo-2", Target: netip.MustParseAddr("10.0.0.5")}); !errors.Is(err, ErrEchoerClosed) {
		t.Fatalf("Send after Close = %v", err)
	}
	if err := echoer.Close(); err != nil {
		t.Fatalf("second Close = %v", err)
	}
}

func TestEchoerContextCancellationReleasesTheAssociation(t *testing.T) {
	conn := newFakeICMPConn()
	echoer := testEchoer(conn, 5*time.Second, 8)
	defer echoer.Close()

	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 1)
	go func() {
		_, err := echoer.Send(ctx, EchoRequest{CorrelationID: "echo-1", Target: netip.MustParseAddr("10.0.0.5"), Identifier: 1, Sequence: 1})
		errs <- err
	}()
	waitForWrite(t, conn, 1)
	cancel()
	select {
	case err := <-errs:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not return")
	}
	if echoer.InFlight() != 0 {
		t.Fatalf("in flight = %d after cancellation", echoer.InFlight())
	}
}

func TestEchoerRejectsTargetsItCannotSendTo(t *testing.T) {
	conn := newFakeICMPConn()
	echoer := testEchoer(conn, time.Second, 8)
	defer echoer.Close()

	for _, target := range []string{"2001:db8::1", "224.0.0.1", "0.0.0.0"} {
		_, err := echoer.Send(context.Background(), EchoRequest{CorrelationID: "echo-1", Target: netip.MustParseAddr(target), Identifier: 1, Sequence: 1})
		if err == nil {
			t.Fatalf("Send(%s) = nil error", target)
		}
	}
	if len(conn.writes()) != 0 {
		t.Fatalf("rejected targets still wrote %d datagrams", len(conn.writes()))
	}
}

func TestEchoerReportsAnUnreachableHostAsAnAnswer(t *testing.T) {
	conn := newFakeICMPConn()
	conn.writeErr = &net.OpError{Op: "write", Net: "udp4", Err: syscall.EHOSTUNREACH}
	echoer := testEchoer(conn, time.Second, 8)
	defer echoer.Close()

	reply, err := echoer.Send(context.Background(), EchoRequest{CorrelationID: "echo-1", Target: netip.MustParseAddr("10.0.0.77"), Identifier: 3, Sequence: 3})
	if err != nil {
		t.Fatalf("an unreachable host is an answer, not an error: %v", err)
	}
	if reply.Status != StatusEchoUnreachable {
		t.Fatalf("status = %q", reply.Status)
	}
	if echoer.InFlight() != 0 {
		t.Fatalf("in flight = %d", echoer.InFlight())
	}
}

func TestOpenEchoerNamesTheSysctlWhenThePingSocketIsRefused(t *testing.T) {
	original := listenICMPPacket
	defer func() { listenICMPPacket = original }()

	listenICMPPacket = func(network, address string) (ICMPPacketConn, error) {
		return nil, &net.OpError{Op: "listen", Net: network, Source: nil, Addr: nil, Err: syscall.EACCES}
	}
	_, err := OpenEchoer(EchoerConfig{BindAddress: "0.0.0.0", Timeout: time.Second, MaxConcurrent: 4})
	if !errors.Is(err, ErrPingGroupRangeRequired) {
		t.Fatalf("err = %v, want ErrPingGroupRangeRequired", err)
	}
	if !strings.Contains(err.Error(), "net.ipv4.ping_group_range") {
		t.Fatalf("the operator has to know which sysctl to set: %v", err)
	}

	listenICMPPacket = func(network, address string) (ICMPPacketConn, error) {
		return nil, errors.New("socket factory exploded")
	}
	if _, err := OpenEchoer(EchoerConfig{BindAddress: "0.0.0.0", Timeout: time.Second, MaxConcurrent: 4}); err == nil || errors.Is(err, ErrPingGroupRangeRequired) {
		t.Fatalf("a non-permission failure must not be reported as a sysctl problem: %v", err)
	}

	listenICMPPacket = func(network, address string) (ICMPPacketConn, error) { return newFakeICMPConn(), nil }
	echoer, err := OpenEchoer(EchoerConfig{BindAddress: "0.0.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := echoer.Close(); err != nil {
		t.Fatal(err)
	}
}
