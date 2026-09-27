package client

import (
	"fmt"
	"net"
	"strings"
)

// requireLoopbackListen is the single admission rule for a local listener that
// forwards to an internal target.
//
// A raw TCP, UDP or HTTP forward has no authentication layer of its own, so a
// non-loopback bind publishes the target service to anyone who can reach this
// host, with the full authority of the Client token behind it. socks5 and
// http-proxy already refuse that without an explicit --allow-remote; the
// forwarders that carry no credentials can only ever ask for the explicit
// acknowledgement, which is what this helper does. It lives in the client
// library rather than only in the CLI so a caller that builds a forwarder
// programmatically cannot walk past it.
//
// The host is judged textually, exactly like the socks5 guard: "localhost" and a
// loopback address are local, and anything else - including a hostname that
// happens to resolve to loopback - requires the flag. Refusing a hostname is the
// fail-closed direction because the listener binds whatever the resolver returns,
// and a target that resolves to 0.0.0.0 today would silently widen the exposure.
func requireLoopbackListen(kind, listenAddr string, allowRemote bool) error {
	if allowRemote {
		return nil
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(listenAddr))
	if err != nil {
		return fmt.Errorf("client: invalid %s listen address %q", strings.ToLower(kind), listenAddr)
	}
	if isLoopbackHost(host) {
		return nil
	}
	return fmt.Errorf("client: non-loopback %s forward requires --allow-remote", strings.ToLower(kind))
}
