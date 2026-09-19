// Command icmpsock proves an unprivileged ICMP datagram socket works when
// net.ipv4.ping_group_range covers the process GID, and reports whether the
// kernel rewrites the ICMP identifier. MUST run on Linux: macOS has no
// ping_group_range and its ICMP semantics differ, so a macOS result is void.
package main

import (
	"fmt"
	"net"
	"os"
	"runtime"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

func main() {
	if runtime.GOOS != "linux" {
		fmt.Println("SKIP: must run on Linux; result on", runtime.GOOS, "is void")
		os.Exit(0)
	}
	target := os.Getenv("TM_SPIKE_ICMP_TARGET")
	if target == "" {
		target = "127.0.0.1"
	}

	// "udp4" selects the unprivileged ICMP datagram socket (SOCK_DGRAM,
	// IPPROTO_ICMP), which requires net.ipv4.ping_group_range to cover our GID.
	c, err := icmp.ListenPacket("udp4", "0.0.0.0")
	if err != nil {
		fmt.Printf("FAIL: cannot open unprivileged ping socket: %v\n", err)
		fmt.Println("      check: sysctl net.ipv4.ping_group_range")
		os.Exit(1)
	}
	defer c.Close()

	const wantID = 0x1234
	msg := icmp.Message{
		Type: ipv4.ICMPTypeEcho, Code: 0,
		Body: &icmp.Echo{ID: wantID, Seq: 1, Data: []byte("tunnelmesh-task0")},
	}
	b, err := msg.Marshal(nil)
	if err != nil {
		fmt.Println("FAIL marshal:", err)
		os.Exit(1)
	}
	dst := &net.UDPAddr{IP: net.ParseIP(target)}
	if _, err := c.WriteTo(b, dst); err != nil {
		fmt.Printf("FAIL: write to %s: %v\n", target, err)
		os.Exit(1)
	}

	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	reply := make([]byte, 1500)
	n, peer, err := c.ReadFrom(reply)
	if err != nil {
		fmt.Printf("FAIL: no echo reply from %s: %v\n", target, err)
		os.Exit(1)
	}
	rm, err := icmp.ParseMessage(1, reply[:n])
	if err != nil {
		fmt.Println("FAIL parse:", err)
		os.Exit(1)
	}
	echo, ok := rm.Body.(*icmp.Echo)
	if !ok {
		fmt.Printf("FAIL: unexpected reply body type %T from %v\n", rm.Body, peer)
		os.Exit(1)
	}
	fmt.Printf("PASS: unprivileged ping socket works; target=%s reply_seq=%d\n", target, echo.Seq)
	fmt.Printf("      requested id=0x%04x, kernel-visible id=0x%04x\n", wantID, echo.ID)
	if echo.ID != wantID {
		fmt.Println("      CONFIRMED: kernel rewrites the ICMP id -> the gateway must")
		fmt.Println("      correlate replies with its own id carried in the stream,")
		fmt.Println("      never with the ICMP id (spec section 6.2).")
	} else {
		fmt.Println("      NOTE: id preserved on this kernel; still correlate by stream id.")
	}
}
