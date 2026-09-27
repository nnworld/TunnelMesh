// Package vpn holds the embedded VPN gateway's domain logic: WireGuard key
// material, IP pool carving, per-packet egress policy, peer specification
// validation and WireGuard configuration rendering.
//
// Everything in this package is a pure function over plain values. It does no
// I/O, holds no state, starts no goroutine and never imports the storage or
// HTTP layers, so every rule here is exhaustively unit testable without a
// database, a TUN device or a network.
//
// The boundary is deliberate:
//
//   - Persistence belongs to internal/storage. The 22-column storage.VPNPeer
//     row is a record; this package's PeerSpec is a validated request. The one
//     place that maps between them is internal/server/vpn_peer_service.go, so
//     storage details cannot leak into the domain rules.
//   - Transport belongs to internal/server. Handlers parse HTTP, authorize the
//     principal and write the envelope; they never re-derive a policy decision.
//   - Address safety belongs to internal/routing, which this package wraps the
//     same way internal/proxyentry does. A dangerous-address or private-target
//     rule is written once, in routing, and inherited here.
//
// Two encoding rules are owned exclusively by this package and must never be
// reimplemented elsewhere: the canonical text form of a peer's allowed IPs and
// allowed ports (see PeerSpec), and the stable error codes in errors.go, which
// are part of the public API contract.
//
// Phase status: this package is the control plane. The data plane that actually
// moves packets (the WireGuard endpoint, the userspace network stack and the
// flow table) is a later phase and is gated behind the "vpn" build tag. Nothing
// here claims otherwise.
package vpn
