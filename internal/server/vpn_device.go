//go:build vpn

package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"

	"golang.zx2c4.com/wireguard/tun"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/stack"

	"github.com/tunnelmesh/tunnelmesh/internal/vpn"
)

// vpnDeviceName is what the device reports to anything that asks.
//
// It is a fixed string rather than a generated one because it appears in
// wireguard-go's own log lines, and an operator reading "tunnelmesh0" knows which
// subsystem a message came from. It is deliberately not "wg0" or "tun0": those
// are the names the kernel gives real interfaces, and a log line naming one of
// them sends whoever is debugging off to look for an interface that does not
// exist. The device never appears in "ip link" either, because it is not opened
// as a file at all.
const vpnDeviceName = "tunnelmesh0"

// vpnDeviceQueueSizeDefault bounds the outbound queue when a caller does not
// choose one. 1024 packets at the largest MTU the loader accepts is about 1.5 MB,
// which is small enough to hold per gateway and large enough that a brief stall in
// the WireGuard encryption path does not cost packets.
const vpnDeviceQueueSizeDefault = 1024

var (
	// errVPNDeviceInboundMissing refuses to build a device with nowhere to send
	// decrypted packets.
	//
	// It is an error rather than a default of "inject everything into the stack"
	// because that default would compile, start and quietly skip every peer, rate
	// and destination check the packet pipeline exists to apply. A device that
	// cannot be built without its policy hook cannot be built around it.
	errVPNDeviceInboundMissing = errors.New("vpn: the memory tun device requires an inbound packet handler")
	// errVPNDeviceNoPacket reports that a bounded read found nothing. It is
	// distinct from os.ErrClosed so a caller polling with a deadline can tell "the
	// queue is empty" from "the device is gone".
	errVPNDeviceNoPacket = errors.New("vpn: no outbound packet is available")
	// errVPNDeviceNotIPv4 refuses a datagram of the wrong address family. The
	// tunnel is IPv4-only end to end - the address pool, the egress policy and the
	// packet parser all are - and the stack this device feeds has no IPv6 network
	// protocol registered, so injecting one would be counted by gvisor as an
	// unknown protocol rather than refused by anything that could explain it.
	errVPNDeviceNotIPv4 = errors.New("vpn: the tunnel carries ipv4 only")
	// errVPNDeviceShortPacket refuses a datagram too short to hold an IPv4 header.
	errVPNDeviceShortPacket = errors.New("vpn: packet is shorter than an ipv4 header")
	// errVPNDeviceBadBuffer reports a Read call whose arguments cannot be
	// satisfied. wireguard-go always passes a batch of buffers and a matching
	// slice of sizes, so this is a programming error in a caller rather than a
	// runtime condition.
	errVPNDeviceBadBuffer = errors.New("vpn: read requires at least one buffer and one size")
)

// vpnInboundHandler receives one decrypted tunnel packet.
//
// The slice is owned by the callee: the device copies before calling, so a
// handler may keep it, queue it or hand it to another goroutine. That guarantee
// is the reason the copy exists - wireguard-go returns the message buffer to a
// pool as soon as Write returns, and a handler that kept the original slice would
// be reading memory another packet is being decrypted into. The race is invisible
// to -race when the pool hands the buffer to a different goroutine than the one
// that read it, which is exactly the kind of bug that survives testing and
// corrupts traffic under load.
type vpnInboundHandler func(packet []byte)

// memoryDevice is the tun.Device half of the gateway.
//
// It is a bridge and nothing more. Inbound, it copies each decrypted packet and
// hands it to the packet pipeline, which owns every decision about what may be
// forwarded; only what the pipeline chooses to inject reaches the stack.
// Outbound, it drains the stack's transmit queue into a bounded channel that
// wireguard-go's reader consumes.
//
// The reason it is not tun/netstack's CreateNetTUN, which does the same bridging,
// is that one: netTun's outbound queue is an unbuffered channel, so its
// WriteNotify blocks until a reader arrives. WriteNotify is called from the
// stack's own transmit path, which means a slow or absent WireGuard reader stalls
// the stack for every flow it carries, not just the one that overflowed. This
// device uses a bounded queue and drops instead, so backpressure degrades one
// flow and is counted.
//
// It opens no file. File() returns nil, and nothing in the construction path
// touches /dev/net/tun, so the gateway needs no CAP_NET_ADMIN and runs unchanged
// inside an unprivileged container.
type memoryDevice struct {
	ep           *channel.Endpoint
	notifyHandle *channel.NotificationHandle
	events       chan tun.Event
	outbound     chan *buffer.View
	mtu          int
	inbound      vpnInboundHandler

	// ctx is cancelled by Close and is the single source of "is this device still
	// usable". Every blocking path selects on it, which is what lets Close wake a
	// reader parked in Read instead of leaving it until the process exits.
	ctx    context.Context
	cancel context.CancelFunc

	dropped atomic.Int64

	closeOnce sync.Once
}

var _ tun.Device = (*memoryDevice)(nil)
var _ channel.Notification = (*memoryDevice)(nil)

// newMemoryDevice builds a device that is immediately usable.
//
// The MTU is validated against the same bounds the loader uses for
// server.vpn.mtu, because a channel endpoint silently truncates anything larger
// than the MTU it was built with and a truncated packet is indistinguishable
// from corruption once it reaches a peer.
func newMemoryDevice(mtu, queueSize int, inbound vpnInboundHandler) (*memoryDevice, error) {
	if inbound == nil {
		return nil, errVPNDeviceInboundMissing
	}
	if mtu < vpn.MinTunnelMTU || mtu > vpn.MaxTunnelMTU {
		return nil, fmt.Errorf("vpn: device mtu %d must be between %d and %d", mtu, vpn.MinTunnelMTU, vpn.MaxTunnelMTU)
	}
	if queueSize <= 0 {
		return nil, fmt.Errorf("vpn: device queue size %d must be positive", queueSize)
	}
	ctx, cancel := context.WithCancel(context.Background())
	device := &memoryDevice{
		ep:       channel.New(queueSize, uint32(mtu), ""),
		events:   make(chan tun.Event, 1),
		outbound: make(chan *buffer.View, queueSize),
		mtu:      mtu,
		inbound:  inbound,
		ctx:      ctx,
		cancel:   cancel,
	}
	// The notification handle is registered before anything can write, so no
	// outbound packet can be queued without the device being told about it.
	device.notifyHandle = device.ep.AddNotify(device)
	// EventUp is buffered so construction cannot block on a reader that has not
	// started yet. wireguard-go waits for it before starting peer timers, so a
	// device that never reports it leaves every handshake unsent.
	device.events <- tun.EventUp
	return device, nil
}

// endpoint exposes the link-layer endpoint so the stack assembly can attach it to
// a NIC and so a test can inject outbound packets the way the stack does.
func (d *memoryDevice) endpoint() *channel.Endpoint { return d.ep }

// inject hands one IPv4 packet to the stack.
//
// It is the pipeline's only door into netstack, and calling it is a decision the
// pipeline makes after the peer, rate and destination checks have passed. The
// device itself never calls it: a decrypted packet goes to the handler first, and
// only what the handler allows comes back.
//
// The packet is copied by buffer.MakeWithData, so the caller keeps ownership of
// the slice it passed.
func (d *memoryDevice) inject(packet []byte) error {
	if d.isClosed() {
		return os.ErrClosed
	}
	if len(packet) < header.IPv4MinimumSize {
		return errVPNDeviceShortPacket
	}
	if packet[0]>>4 != 4 {
		return errVPNDeviceNotIPv4
	}
	d.ep.InjectInbound(header.IPv4ProtocolNumber, stack.NewPacketBuffer(stack.PacketBufferOptions{
		Payload: buffer.MakeWithData(packet),
	}))
	return nil
}

// Name reports the device name. It never fails: there is no kernel object to
// query, which is the point of the device.
func (d *memoryDevice) Name() (string, error) { return vpnDeviceName, nil }

// File returns nil. There is no descriptor, and a caller that treats nil as an
// error is correctly reporting that this device cannot be handed to something
// expecting a real interface.
func (d *memoryDevice) File() *os.File { return nil }

// MTU reports the tunnel MTU the stack was told to respect.
func (d *memoryDevice) MTU() (int, error) { return d.mtu, nil }

// BatchSize reports one. The device does no generic segmentation offload, so a
// batch larger than one would only make wireguard-go allocate buffers it cannot
// fill and would raise the batch size of the whole device, including the UDP
// socket side, which does not segment either.
func (d *memoryDevice) BatchSize() int { return 1 }

// Events returns the device event channel. It carries EventUp once at
// construction and is closed by Close, which is how a consumer learns the device
// is gone.
func (d *memoryDevice) Events() <-chan tun.Event { return d.events }

// Read blocks until one outbound packet is available.
//
// It is what wireguard-go's TUN reader calls in a loop, so it must never return
// (0, nil): that combination reads as "nothing happened, try again" and turns the
// reader into a spin loop burning a core.
func (d *memoryDevice) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	return d.ReadContext(d.ctx, bufs, sizes, offset)
}

// ReadContext is Read with a caller-supplied deadline.
//
// It consumes the queue WriteNotify fills rather than the endpoint's own, because
// the endpoint queue is drained eagerly on the stack's transmit path: reading from
// it here would always find it empty and block forever with packets sitting one
// channel away.
//
// The device's own lifetime is always part of the wait, so a context that outlives
// the device returns os.ErrClosed rather than blocking on a queue nobody will ever
// fill again.
func (d *memoryDevice) ReadContext(ctx context.Context, bufs [][]byte, sizes []int, offset int) (int, error) {
	if len(bufs) == 0 || len(sizes) == 0 || offset > len(bufs[0]) {
		return 0, errVPNDeviceBadBuffer
	}
	// Checked first so a closed device answers the same way whether or not packets
	// are still queued: after Close nobody is left to encrypt them, and returning
	// one would make wireguard-go build an outbound element for a device that is
	// already shutting down.
	if d.isClosed() {
		return 0, os.ErrClosed
	}
	select {
	case view := <-d.outbound:
		defer view.Release()
		// A packet longer than the caller's buffer cannot be sent: wireguard-go
		// would encrypt a runt and the peer would drop it as malformed. The MTU
		// the endpoint was built with and the MTU reported to wireguard-go are the
		// same number, so this cannot happen in an assembled gateway.
		n := copy(bufs[0][offset:], view.AsSlice())
		sizes[0] = n
		return 1, nil
	case <-ctx.Done():
		if d.isClosed() {
			return 0, os.ErrClosed
		}
		return 0, errVPNDeviceNoPacket
	}
}

// Write receives the packets wireguard-go decrypted from one peer.
//
// Each non-empty element is copied and handed to the inbound handler, which is
// where the packet pipeline runs. The return value counts the packets delivered,
// not the elements passed: wireguard-go batches whatever it decrypted and a slot
// with no content carries no packet.
func (d *memoryDevice) Write(bufs [][]byte, offset int) (int, error) {
	if d.isClosed() {
		return 0, os.ErrClosed
	}
	delivered := 0
	for _, buf := range bufs {
		if offset > len(buf) {
			return delivered, errVPNDeviceBadBuffer
		}
		packet := buf[offset:]
		if len(packet) == 0 {
			continue
		}
		d.inbound(bytes.Clone(packet))
		delivered++
	}
	return delivered, nil
}

// WriteNotify drains the stack's transmit queue into the device's bounded queue.
//
// It runs on the stack's own transmit path, so it must not block: a send that
// waits would stall the netstack for every flow the gateway carries. When the
// queue is full the packet is dropped and counted, which turns one flow's
// backpressure into a visible number instead of a frozen stack.
//
// One notification may cover several packets, so the endpoint is drained until it
// reports nothing left rather than read exactly once.
func (d *memoryDevice) WriteNotify() {
	for {
		packet := d.ep.Read()
		if packet == nil {
			return
		}
		view := packet.ToView()
		packet.DecRef()
		if d.isClosed() {
			view.Release()
			return
		}
		select {
		case d.outbound <- view:
		default:
			// Release before counting so a view is never left holding a chunk
			// reference if the counter update is what a reader is waiting on.
			view.Release()
			d.dropped.Add(1)
		}
	}
}

// DroppedOutbound reports how many packets the stack produced that the device
// could not queue. It is the number behind the capacity_exhausted class, and the
// only signal that distinguishes "the peer is not reading" from "the peer is not
// reachable".
func (d *memoryDevice) DroppedOutbound() int64 { return d.dropped.Load() }

// Close releases the device. It is idempotent, and it wakes every parked reader.
func (d *memoryDevice) Close() error {
	d.closeOnce.Do(func() {
		// Cancel first: it is what makes every blocking path return, and doing it
		// before the endpoint is closed means a concurrent WriteNotify sees the
		// device as gone rather than racing the queue close.
		d.cancel()
		d.ep.RemoveNotify(d.notifyHandle)
		d.ep.Close()
		close(d.events)
		d.drain()
	})
	return nil
}

// drain releases the views still queued, so closing a device under load returns
// its buffers to the pool instead of leaking a chunk reference per packet.
//
// It is safe against a concurrent Read: a channel delivers each element to
// exactly one receiver, so the two can never release the same view. A reader that
// wins the race simply serves one last packet to a device that is closing, which
// wireguard-go discards because its own Close has already run.
func (d *memoryDevice) drain() {
	for {
		select {
		case view := <-d.outbound:
			view.Release()
		default:
			return
		}
	}
}

// isClosed reports whether Close has run.
func (d *memoryDevice) isClosed() bool {
	select {
	case <-d.ctx.Done():
		return true
	default:
		return false
	}
}
