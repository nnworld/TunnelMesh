//go:build vpn

package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

// vpnDeviceInboundBuffer lays one packet out the way wireguard-go's receive path
// does before it calls tun.Device.Write: a buffer sliced to exactly the transport
// offset plus the decrypted length.
func vpnDeviceInboundBuffer(packet []byte) []byte {
	buf := make([]byte, vpnDeviceWriteOffset+len(packet))
	copy(buf[vpnDeviceWriteOffset:], packet)
	return buf
}

// vpnHex renders a packet for a failure message, truncated. Printing a whole
// multi-kilobyte buffer in hex is how a useful test output becomes an unusable
// one, and the first bytes plus the length are what identifies a packet.
func vpnHex(packet []byte) string {
	const limit = 48
	if len(packet) > limit {
		return fmt.Sprintf("%x... (%d bytes)", packet[:limit], len(packet))
	}
	return fmt.Sprintf("%x (%d bytes)", packet, len(packet))
}

// recordingNetworkDispatcher stands in for the netstack network layer. Attaching
// it to the channel endpoint lets a test prove that inject reaches the stack
// without building a stack, which keeps this file about the device and leaves the
// assembly itself to vpn_stack_test.go.
type recordingNetworkDispatcher struct {
	mu        sync.Mutex
	packets   [][]byte
	protocols []tcpip.NetworkProtocolNumber
	linkOnly  int
}

func (d *recordingNetworkDispatcher) DeliverNetworkPacket(protocol tcpip.NetworkProtocolNumber, pkt *stack.PacketBuffer) {
	view := pkt.ToView()
	d.mu.Lock()
	d.packets = append(d.packets, view.ToSlice())
	d.protocols = append(d.protocols, protocol)
	d.mu.Unlock()
	view.Release()
	// The dispatcher owns the buffer once it is handed over, exactly as the real
	// network layer does. Not releasing it would hide a double-free in inject.
	pkt.DecRef()
}

func (d *recordingNetworkDispatcher) DeliverLinkPacket(_ tcpip.NetworkProtocolNumber, pkt *stack.PacketBuffer) {
	d.mu.Lock()
	d.linkOnly++
	d.mu.Unlock()
	pkt.DecRef()
}

func (d *recordingNetworkDispatcher) delivered() [][]byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([][]byte, len(d.packets))
	copy(out, d.packets)
	return out
}

func (d *recordingNetworkDispatcher) networkProtocol() tcpip.NetworkProtocolNumber {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.protocols) == 0 {
		return 0
	}
	return d.protocols[0]
}

// recordingInboundHandler captures what the device hands to the packet pipeline.
type recordingInboundHandler struct {
	mu      sync.Mutex
	packets [][]byte
	calls   int
}

func (h *recordingInboundHandler) handle(packet []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls++
	h.packets = append(h.packets, packet)
}

func (h *recordingInboundHandler) received() [][]byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([][]byte, len(h.packets))
	copy(out, h.packets)
	return out
}

// The three sizes mirror what wireguard-go actually passes, so the device is
// tested against the real reader's buffer shape rather than a convenient one.
const (
	vpnDeviceMessageSize     = device.MaxMessageSize
	vpnDeviceTransportOffset = device.MessageTransportHeaderSize
	vpnDeviceWriteOffset     = device.MessageTransportOffsetContent
)

func newVPNDeviceFixture(t *testing.T, mtu, queueSize int) (*memoryDevice, *recordingInboundHandler) {
	t.Helper()
	handler := &recordingInboundHandler{}
	device, err := newMemoryDevice(mtu, queueSize, handler.handle)
	if err != nil {
		t.Fatalf("newMemoryDevice(%d, %d): %v", mtu, queueSize, err)
	}
	t.Cleanup(func() { _ = device.Close() })
	return device, handler
}

// outboundPacket pushes one packet through the endpoint the way netstack does and
// drives the notification the stack would have raised.
func outboundPacket(t *testing.T, device *memoryDevice, packet []byte) {
	t.Helper()
	var list stack.PacketBufferList
	list.PushBack(stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(packet)}))
	written, err := device.endpoint().WritePackets(list)
	if err != nil {
		t.Fatalf("WritePackets: %v", err)
	}
	if written != 1 {
		t.Fatalf("WritePackets accepted %d of 1 packet", written)
	}
}

// readOutbound performs one Read the way wireguard-go's TUN reader does, with the
// transport header offset it always passes.
func readOutbound(t *testing.T, device *memoryDevice) []byte {
	t.Helper()
	buffer := make([]byte, vpnDeviceMessageSize)
	sizes := []int{0}
	n, err := device.Read([][]byte{buffer}, sizes, vpnDeviceTransportOffset)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if n != 1 {
		t.Fatalf("Read returned %d packets, want 1", n)
	}
	if sizes[0] < 1 {
		t.Fatalf("Read reported a packet size of %d", sizes[0])
	}
	return append([]byte(nil), buffer[vpnDeviceTransportOffset:vpnDeviceTransportOffset+sizes[0]]...)
}

func TestVPNDeviceImplementsTheTunDeviceInterface(t *testing.T) {
	// The compile-time assertion is the point: wireguard-go accepts a
	// tun.Device, so a missing method is a build failure rather than a runtime
	// surprise.
	var _ tun.Device = (*memoryDevice)(nil)

	device, _ := newVPNDeviceFixture(t, 1420, 8)
	if got := device.File(); got != nil {
		t.Errorf("File() = %v, want nil: the gateway must never open a kernel device", got)
	}
	name, err := device.Name()
	if err != nil || name == "" {
		t.Errorf("Name() = %q, %v, want a stable non-empty name", name, err)
	}
	if name == "wg0" || name == "tun0" {
		t.Errorf("Name() = %q, which an operator would read as a kernel interface", name)
	}
	mtu, err := device.MTU()
	if err != nil || mtu != 1420 {
		t.Errorf("MTU() = %d, %v, want 1420", mtu, err)
	}
	if got := device.BatchSize(); got != 1 {
		t.Errorf("BatchSize() = %d, want 1: the gateway does no GSO", got)
	}
	// Constructing the device on a host with no /dev/net/tun is itself part of
	// the evidence: this test runs on macOS in CI, where that path does not exist.
	select {
	case event := <-device.Events():
		if event&tun.EventUp == 0 {
			t.Errorf("the first device event is %v, want EventUp", event)
		}
	case <-time.After(time.Second):
		t.Error("the device never reported EventUp, so wireguard-go would not start its timers")
	}
}

func TestVPNDeviceConstructionRefusesUnusableArguments(t *testing.T) {
	handler := &recordingInboundHandler{}
	for name, args := range map[string]struct {
		mtu       int
		queueSize int
		inbound   vpnInboundHandler
	}{
		"mtu below the ipv4 minimum": {mtu: 575, queueSize: 8, inbound: handler.handle},
		"mtu above ethernet":         {mtu: 1501, queueSize: 8, inbound: handler.handle},
		"mtu zero":                   {mtu: 0, queueSize: 8, inbound: handler.handle},
		"queue size zero":            {mtu: 1420, queueSize: 0, inbound: handler.handle},
		"queue size negative":        {mtu: 1420, queueSize: -1, inbound: handler.handle},
		"no inbound handler":         {mtu: 1420, queueSize: 8, inbound: nil},
	} {
		device, err := newMemoryDevice(args.mtu, args.queueSize, args.inbound)
		if err == nil {
			_ = device.Close()
			t.Errorf("%s: newMemoryDevice succeeded, want a refusal", name)
			continue
		}
		if device != nil {
			t.Errorf("%s: newMemoryDevice returned %v alongside an error", name, device)
		}
	}
	// A missing handler is refused rather than defaulted. A device that injected
	// everything straight into the stack would compile, start and quietly skip
	// every peer, rate and destination check the pipeline exists to apply.
	if _, err := newMemoryDevice(1420, 8, nil); !errors.Is(err, errVPNDeviceInboundMissing) {
		t.Errorf("error = %v, want errVPNDeviceInboundMissing", err)
	}
}

func TestVPNDeviceWriteHandsDecryptedPacketsToThePipeline(t *testing.T) {
	device, handler := newVPNDeviceFixture(t, 1420, 8)
	packets := [][]byte{
		vpnIPv4Packet(vpnTestProtocolTCP, net.ParseIP("10.64.5.7"), net.ParseIP("10.0.0.9"), []byte("syn")),
		vpnIPv4Packet(vpnTestProtocolUDP, net.ParseIP("10.64.5.7"), net.ParseIP("10.0.0.53"), []byte("query")),
		vpnIPv4Packet(vpnTestProtocolICMP, net.ParseIP("10.64.5.7"), net.ParseIP("10.0.0.1"), []byte("echo")),
	}
	bufs := make([][]byte, 0, len(packets)+1)
	for _, packet := range packets {
		// Sized exactly the way wireguard-go sizes it: its receive path slices
		// the buffer to MessageTransportOffsetContent+len(packet), so the device
		// is tested against the real caller's shape rather than a padded one.
		bufs = append(bufs, vpnDeviceInboundBuffer(packet))
	}
	// An empty element is skipped rather than delivered: wireguard-go batches
	// whatever it decrypted, and a zero-length slot carries no packet.
	bufs = append(bufs, make([]byte, vpnDeviceWriteOffset))

	written, err := device.Write(bufs, vpnDeviceWriteOffset)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if written != len(packets) {
		t.Errorf("Write reported %d packets, want %d", written, len(packets))
	}
	received := handler.received()
	if len(received) != len(packets) {
		t.Fatalf("the pipeline saw %d packets, want %d", len(received), len(packets))
	}
	for i, packet := range packets {
		if !bytes.Equal(received[i], packet) {
			t.Errorf("packet %d reached the pipeline as %s, want %s", i, vpnHex(received[i]), vpnHex(packet))
		}
	}
	// wireguard-go returns the message buffer to a pool as soon as Write
	// returns, so the pipeline must never be holding a slice of it. Mutating the
	// caller's buffer afterwards is what proves the device copied.
	for _, buf := range bufs {
		for i := range buf {
			buf[i] = 0xff
		}
	}
	for i, packet := range packets {
		if !bytes.Equal(handler.received()[i], packet) {
			t.Errorf("packet %d changed after Write returned, so the pipeline holds a recycled buffer", i)
		}
	}
}

func TestVPNDeviceWriteRefusesAfterClose(t *testing.T) {
	device, handler := newVPNDeviceFixture(t, 1420, 8)
	packet := vpnIPv4Packet(vpnTestProtocolTCP, net.ParseIP("10.64.5.7"), net.ParseIP("10.0.0.9"), nil)
	buf := vpnDeviceInboundBuffer(packet)
	if err := device.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := device.Write([][]byte{buf}, vpnDeviceWriteOffset); !errors.Is(err, os.ErrClosed) {
		t.Errorf("Write after Close = %v, want os.ErrClosed", err)
	}
	if handler.calls != 0 {
		t.Errorf("a closed device delivered %d packets to the pipeline", handler.calls)
	}
}

func TestVPNDeviceInjectReachesTheNetworkDispatcher(t *testing.T) {
	device, _ := newVPNDeviceFixture(t, 1420, 8)
	dispatcher := &recordingNetworkDispatcher{}
	device.endpoint().Attach(dispatcher)

	packet := vpnIPv4Packet(vpnTestProtocolTCP, net.ParseIP("10.64.5.7"), net.ParseIP("10.0.0.9"), []byte("syn"))
	if err := device.inject(packet); err != nil {
		t.Fatalf("inject: %v", err)
	}
	delivered := dispatcher.delivered()
	if len(delivered) != 1 {
		t.Fatalf("the dispatcher saw %d packets, want 1", len(delivered))
	}
	if !bytes.Equal(delivered[0], packet) {
		t.Errorf("the stack received %s, want %s", vpnHex(delivered[0]), vpnHex(packet))
	}
	if got := dispatcher.networkProtocol(); got != header.IPv4ProtocolNumber {
		t.Errorf("the packet was delivered as protocol %d, want ipv4", got)
	}

	// The tunnel is IPv4-only, and refusing at the device is what keeps an IPv6
	// datagram out of a stack that has no IPv6 network protocol registered:
	// injecting one would be counted by gvisor as an unknown protocol and could
	// make the stack answer with an error datagram of its own.
	v6 := vpnIPv6VersionOf(packet)
	if err := device.inject(v6); err == nil {
		t.Error("inject accepted an ipv6 datagram")
	} else if len(dispatcher.delivered()) != 1 {
		t.Error("inject delivered a datagram it should have refused")
	}
	if err := device.inject(nil); err == nil {
		t.Error("inject accepted an empty packet")
	}
	if err := device.inject(packet[:8]); err == nil {
		t.Error("inject accepted a truncated ipv4 header")
	}

	if err := device.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := device.inject(packet); !errors.Is(err, os.ErrClosed) {
		t.Errorf("inject after Close = %v, want os.ErrClosed", err)
	}
}

func TestVPNDeviceReadReturnsOutboundPackets(t *testing.T) {
	device, _ := newVPNDeviceFixture(t, 1420, 8)
	first := vpnIPv4Packet(vpnTestProtocolTCP, net.ParseIP("10.0.0.9"), net.ParseIP("10.64.5.7"), []byte("syn-ack"))
	second := vpnIPv4Packet(vpnTestProtocolTCP, net.ParseIP("10.0.0.9"), net.ParseIP("10.64.5.7"), []byte("ack"))
	outboundPacket(t, device, first)
	outboundPacket(t, device, second)

	if got := readOutbound(t, device); !bytes.Equal(got, first) {
		t.Errorf("the first outbound packet came back as %s, want %s", vpnHex(got), vpnHex(first))
	}
	if got := readOutbound(t, device); !bytes.Equal(got, second) {
		t.Errorf("the second outbound packet came back as %s, want %s", vpnHex(got), vpnHex(second))
	}
	if got := device.DroppedOutbound(); got != 0 {
		t.Errorf("DroppedOutbound() = %d, want 0 on a queue that never filled", got)
	}
}

// TestVPNDeviceNeverBlocksTheStackOnAFullQueue pins D8. netstack calls the
// notification from its own transmit path, so a device that waits for a reader
// would stall the stack and with it every other flow the gateway is carrying.
// The queue is bounded instead, and the overflow is counted rather than hidden.
func TestVPNDeviceNeverBlocksTheStackOnAFullQueue(t *testing.T) {
	const queueSize = 4
	device, _ := newVPNDeviceFixture(t, 1420, queueSize)
	packet := vpnIPv4Packet(vpnTestProtocolTCP, net.ParseIP("10.0.0.9"), net.ParseIP("10.64.5.7"), []byte("data"))

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < queueSize+16; i++ {
			outboundPacket(t, device, packet)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("filling the outbound queue blocked, which would stall the netstack transmit path")
	}
	if got := device.DroppedOutbound(); got != 16 {
		t.Errorf("DroppedOutbound() = %d, want the 16 packets that did not fit", got)
	}
	for i := 0; i < queueSize; i++ {
		if got := readOutbound(t, device); !bytes.Equal(got, packet) {
			t.Fatalf("queued packet %d came back as %s", i, vpnHex(got))
		}
	}
	// Nothing else was retained: the queue held exactly its capacity.
	blocked := make(chan struct{})
	go func() {
		defer close(blocked)
		buffer := make([]byte, vpnDeviceMessageSize)
		sizes := []int{0}
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		if _, err := device.ReadContext(ctx, [][]byte{buffer}, sizes, vpnDeviceTransportOffset); !errors.Is(err, errVPNDeviceNoPacket) {
			t.Errorf("ReadContext on an empty queue = %v, want errVPNDeviceNoPacket", err)
		}
	}()
	select {
	case <-blocked:
	case <-time.After(2 * time.Second):
		t.Error("ReadContext on an empty queue blocked past its deadline")
	}
}

func TestVPNDeviceCloseIsIdempotentAndStopsReaders(t *testing.T) {
	device, _ := newVPNDeviceFixture(t, 1420, 8)
	packet := vpnIPv4Packet(vpnTestProtocolTCP, net.ParseIP("10.0.0.9"), net.ParseIP("10.64.5.7"), []byte("fin"))
	outboundPacket(t, device, packet)

	// A reader already parked in Read must be woken by Close rather than left
	// until the process exits: wireguard-go's TUN reader goroutine only returns
	// when Read reports an error.
	readErr := make(chan error, 1)
	go func() {
		buffer := make([]byte, vpnDeviceMessageSize)
		sizes := []int{0}
		_, err := device.Read([][]byte{buffer}, sizes, vpnDeviceTransportOffset)
		readErr <- err
	}()
	// The queued packet may satisfy that read, so drain and park a second one.
	<-readErr
	go func() {
		buffer := make([]byte, vpnDeviceMessageSize)
		sizes := []int{0}
		_, err := device.Read([][]byte{buffer}, sizes, vpnDeviceTransportOffset)
		readErr <- err
	}()
	time.Sleep(20 * time.Millisecond)

	if err := device.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-readErr:
		if !errors.Is(err, os.ErrClosed) {
			t.Errorf("a parked Read returned %v, want os.ErrClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("Close did not wake the parked reader")
	}
	if err := device.Close(); err != nil {
		t.Errorf("a second Close = %v, want nil", err)
	}
	if _, ok := <-device.Events(); ok {
		// Either the channel is closed, which is what a consumer needs in order
		// to stop, or an event was already buffered. Draining proves the former
		// by the second read returning !ok.
		if _, stillOpen := <-device.Events(); stillOpen {
			t.Error("the event channel is still open after Close")
		}
	}
	buffer := make([]byte, vpnDeviceMessageSize)
	sizes := []int{0}
	if _, err := device.Read([][]byte{buffer}, sizes, vpnDeviceTransportOffset); !errors.Is(err, os.ErrClosed) {
		t.Errorf("Read after Close = %v, want os.ErrClosed", err)
	}
}
