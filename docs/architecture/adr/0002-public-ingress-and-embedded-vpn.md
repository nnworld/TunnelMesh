# ADR 0002: Open a public UDP ingress for an embedded VPN gateway

- Status: Accepted
- Date: 2026-09-20

## Context

The `tp-*` managed proxy entry gives a browser or an operating system a standard
HTTPS proxy without installing `tunnelmesh-client`, but it only covers HTTP
CONNECT and absolute-form requests. A user who wants system-level routing,
transparent proxying for arbitrary applications, or `ping` into a private network
still has to install and run the Client on their own machine.

Two repository constraints blocked building that capability into the Server:

- `AGENTS.md:11` — "公网入口默认只使用 HTTP/HTTPS/WebSocket；公网 UDP 不作为服务端监听能力."
- `AGENTS.md:148` — "继续不实现：ICMP、TUN/L2 VPN、P2P NAT traversal、任意远程命令执行."

A native VPN client needs a public UDP endpoint and a tunnel device, so both
statements had to be retired before any implementation work could start. Retiring
them is an architectural decision rather than an editorial one: it widens the
public attack surface, changes what the Server process is allowed to depend on,
and sets the privilege boundary for every later phase.

The design under consideration is
[内嵌 VPN 网关（WireGuard）设计](../../superpowers/specs/2026-09-19-embedded-vpn-gateway-design.md).
Its highest-risk premise — that a WireGuard device can run against an in-memory
`tun.Device` bridged to a gVisor netstack, with no `/dev/net/tun` and no
`CAP_NET_ADMIN` — was measured rather than assumed. Evidence:
`test/spike/vpn-task0/REPORT.md` (Tasks 1-4 all PASS, no FAIL, so the fallback
path in spec §2.2 was not triggered).

Deployment constraints confirmed by the operator (spec §2.1): the internal
gateway's routing table cannot be modified, the Agent cannot maintain nftables
rules, the Agent host accepts a one-time `net.ipv4.ping_group_range` sysctl, no
fourth binary may be introduced, and the Server host can be given a public IP.

## Decision

1. **One public UDP port.** The Server process embeds a WireGuard endpoint on a
   single public UDP port (default 51820). It does not go through nginx or
   OpenResty, does not participate in HTTP routing, and must be allowed
   separately in cloud security groups and host firewalls. `AGENTS.md:11` is
   rewritten from an absolute prohibition into "HTTP/HTTPS/WebSocket is the
   primary ingress; the VPN gateway adds one public UDP port when enabled".

2. **In-memory TUN over gVisor netstack.** The data plane uses a memory-backed
   `tun.Device` bridged to a netstack `LinkEndpoint`. The Server never opens
   `/dev/net/tun` and never requires `CAP_NET_ADMIN`; it only binds an
   unprivileged UDP port. This keeps the netstack integration tests runnable in
   an unprivileged CI job.

3. **`AGENTS.md:148` is partially reversed, not deleted.** TUN/L2 VPN comes off
   the banned list. ICMP is reworded: the Server does not construct ICMP messages
   on its own, and ICMP echo is supported through an unprivileged ping socket on
   the Agent. The bans on **P2P NAT traversal** and **arbitrary remote command
   execution** stay in force; they are orthogonal to this goal and pure risk.
   L2 Ethernet frames are never forwarded.

4. **Policy enforcement stays in Server user space.** Per-packet target
   validation wraps the existing `routing.Policy` instead of restating a deny
   list, so VPN traffic, `tp-*` traffic and Agent policy share one source of
   truth. The Agent gains no privilege, maintains no firewall rules, and the
   internal routing table is untouched. Source addresses are rewritten to the
   Agent host on the internal leg, which is the consequence of not being allowed
   to change internal routing.

5. **Heavy dependencies are build-tag isolated.** `gvisor.dev/gvisor` and
   `golang.zx2c4.com/wireguard` are referenced only from files carrying
   `//go:build vpn`. A Server built without the tag must fail fast at startup if
   `server.vpn.enabled` is true, with a message naming the missing tag, rather
   than silently ignoring the setting. Release packaging gains a tagged variant;
   the default artifact does not carry the extra weight.

6. **No fourth binary.** The VPN data plane lives inside `tunnelmesh-server`.
   `server.vpn.enabled: false` (the default) creates no VPN resources at all, and
   flipping that switch off plus a restart is the five-minute stop-loss path.

## Consequences

- The public surface grows by one UDP port. Handshake floods, unregistered public
  keys, and dropped-packet classes need counters, rate limits and alerts (spec
  §11, §13). The node private key is environment-injected only and peer private
  keys are stored sealed, revealed through the same confirmed, audited,
  `no-store` path as service tokens.
- Capability boundaries must be stated in user documentation: source addresses
  are not preserved on the internal leg, only ICMP echo is supported, IP
  fragmentation and IPv6 data plane are out of scope, and the Agent cannot
  initiate connections towards the user (spec §4.3).
- The first SYN of a new tuple is necessarily dropped because the interception
  hook only runs on a demux miss while listener registration is asynchronous, so
  production connect latency includes one client RTO. Phase 4 must budget for it
  or pre-warm listeners (Task 0 carry-forward item 7).
- Concurrent registration of one `(address, port)` yields exactly one winner; the
  losers receive `*tcpip.ErrDuplicateAddress` and "port is in use" and must treat
  both as success. A loser that removes the address or closes the listener tears
  down a flow that is already being served.
- `channel.Endpoint.WritePackets` performs no MTU check, so the oversize-drop
  rule in the spec has to be enforced by the gateway itself.
- **Two evidence gates stay open.** The no-root / no-`CAP_NET_ADMIN` claim has
  only been measured on macOS, where `/dev/net/tun` is trivially absent and
  capabilities are not observable; it must be re-measured in a non-root
  `--cap-drop=ALL` Linux container. Task 5 (unprivileged ICMP datagram socket)
  has not been run on Linux at all. Until both are closed, neither claim may be
  published as verified, and phase 7 (Agent ICMP) may not start.
- Live documentation states that the gateway is approved and in progress, not
  that it is available. Capability documentation ships with the phases that
  implement it, so no doc promises a feature a release does not have.

## Rejected alternatives

### Real `/dev/net/tun` with `CAP_NET_ADMIN`

Running wireguard-go against a kernel TUN device was the fallback in spec §2.2.
It was rejected because it grants the Server process a kernel capability, makes
the data plane untestable in unprivileged CI, and adds a host-level dependency to
every deployment. Task 0 showed the in-memory bridge works, so the fallback is
unnecessary. If the bridge premise is ever invalidated, this becomes the chosen
path again and the privilege acceptance must be recorded here as a superseding
note: systemd `AmbientCapabilities=CAP_NET_ADMIN` or Docker `--cap-add=NET_ADMIN`,
never `--privileged`.

### A separate privileged VPN gateway binary

A fourth component would isolate the heavy dependencies and the privilege
question, but the operator explicitly ruled out another binary to deploy,
upgrade and monitor. Build-tag isolation achieves the dependency half of that
benefit without the operational cost.

### Kernel forwarding on the Agent with route injection

Forwarding at the Agent and injecting routes into the internal network would
preserve source addresses and give true end-to-end L3. It was rejected because
the internal gateway's routing table cannot be modified and the Agent cannot
maintain nftables rules, and because it would move policy enforcement to a place
where `Dialer.Policy` is not even wired up in production today.

### OpenVPN, IPsec, L2TP or Shadowsocks

WireGuard needs one UDP port, has a small protocol surface, has a mature Go
implementation, and reduces peer configuration to a single ini file. OpenVPN
would add TLS plus a second protocol and a much larger code base without covering
more of the stated goal.

### Publishing the capability before it ships

Rewriting the live docs to say the VPN gateway works would have been cheaper than
the "approved and in progress" wording chosen here. It was rejected because it
makes every current release's documentation false, and because the two open
evidence gates above mean some claims are not yet ours to make.
