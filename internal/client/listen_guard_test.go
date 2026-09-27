package client

import (
	"context"
	"io"
	"strings"
	"testing"
)

// guardOpener exists only so the constructors accept an opener: the guard runs
// before anything is dialled, so no stream is ever requested by these tests.
type guardOpener struct{}

func (guardOpener) OpenStream(context.Context, StreamRequest) (io.ReadWriteCloser, error) {
	return nil, io.EOF
}

// TestNonLoopbackListenersRequireAllowRemote closes the asymmetry that made
// forward tcp/udp/http the only unauthenticated entry points without the guard
// socks5 and http-proxy already have: a bare --listen 0.0.0.0:15432 turned the
// workstation into an open tunnel into the internal target.
func TestNonLoopbackListenersRequireAllowRemote(t *testing.T) {
	cases := []struct {
		name   string
		build  func(allowRemote bool) error
		wanted string
	}{
		{
			name: "tcp",
			build: func(allow bool) error {
				_, err := NewTCPForward(guardOpener{}, TCPForwardConfig{
					ListenAddr: "0.0.0.0:15432", AgentID: "agent-a", TargetHost: "10.0.0.10", TargetPort: 5432, AllowRemote: allow,
				})
				return err
			},
			wanted: "non-loopback tcp forward requires --allow-remote",
		},
		{
			name: "udp",
			build: func(allow bool) error {
				_, err := NewUDPForward(guardOpener{}, UDPForwardConfig{
					ListenAddr: "0.0.0.0:15433", AgentID: "agent-a", TargetHost: "10.0.0.10", TargetPort: 5432, AllowRemote: allow,
				})
				return err
			},
			wanted: "non-loopback udp forward requires --allow-remote",
		},
		{
			name: "http",
			build: func(allow bool) error {
				_, err := NewHTTPForward(guardOpener{}, HTTPForwardConfig{
					ListenAddr: "0.0.0.0:18080", AgentID: "agent-a", TargetHost: "10.0.0.10", TargetPort: 8080, AllowRemote: allow,
				})
				return err
			},
			wanted: "non-loopback http forward requires --allow-remote",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.build(false)
			if err == nil {
				t.Fatalf("a %s forward on a non-loopback address was accepted without --allow-remote", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wanted) {
				t.Fatalf("%s guard error = %v, want it to mention %q", tc.name, err, tc.wanted)
			}
			if err := tc.build(true); err != nil {
				t.Fatalf("%s forward with --allow-remote = %v, want acceptance", tc.name, err)
			}
		})
	}
}

// TestLoopbackListenersNeverNeedAllowRemote keeps the guard from becoming a tax
// on the documented default: loopback binds must work with no flag at all.
func TestLoopbackListenersNeverNeedAllowRemote(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:15500", "localhost:15501", "[::1]:15502"} {
		if _, err := NewTCPForward(guardOpener{}, TCPForwardConfig{
			ListenAddr: addr, AgentID: "agent-a", TargetHost: "10.0.0.10", TargetPort: 5432,
		}); err != nil {
			t.Fatalf("loopback listen %s was rejected: %v", addr, err)
		}
	}
}
