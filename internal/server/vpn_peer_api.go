package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
	"github.com/tunnelmesh/tunnelmesh/internal/vpn"
)

// vpnSubnetLeaseTTL is how long a node's claim on its subnet survives without a
// renewal. It matches the repository's own default so the two layers cannot
// disagree, and it is deliberately short: a node that stops issuing peers also
// stops renewing, and a subnet held by a dead node must become takeable. Peers
// already holding an address keep it either way, because a peer row is never
// renumbered and a takeover inherits the allocated counter.
const vpnSubnetLeaseTTL = time.Minute

// vpnPeerRequest is the create body. AllowedIPs arrives as text and is parsed by
// internal/vpn, the only owner of the canonical encoding, so a body and a stored
// row can never disagree about what a legal CIDR is.
type vpnPeerRequest struct {
	Name                string          `json:"name"`
	Description         string          `json:"description"`
	AgentID             string          `json:"agentId"`
	AllowedIPs          []string        `json:"allowedIps"`
	AllowedPorts        []int           `json:"allowedPorts"`
	AllowPrivateTargets bool            `json:"allowPrivateTargets"`
	ICMPEnabled         bool            `json:"icmpEnabled"`
	MaxConcurrentFlows  int             `json:"maxConcurrentFlows"`
	PacketRateLimit     int             `json:"packetRateLimit"`
	ExpiresAt           json.RawMessage `json:"expiresAt"`
}

// vpnPeerPatchRequest is the PATCH body. Pointers carry presence, and the two
// collection fields are pointers to slices because an empty list is a value with
// a meaning of its own: no reachable destination, and no port restriction.
type vpnPeerPatchRequest struct {
	Name                *string                `json:"name"`
	Description         *string                `json:"description"`
	AgentID             *string                `json:"agentId"`
	AllowedIPs          *[]string              `json:"allowedIps"`
	AllowedPorts        *[]int                 `json:"allowedPorts"`
	AllowPrivateTargets *bool                  `json:"allowPrivateTargets"`
	ICMPEnabled         *bool                  `json:"icmpEnabled"`
	MaxConcurrentFlows  *int                   `json:"maxConcurrentFlows"`
	PacketRateLimit     *int                   `json:"packetRateLimit"`
	ExpiresAt           json.RawMessage        `json:"expiresAt"`
	Status              *storage.VPNPeerStatus `json:"status"`
}

func (r vpnPeerPatchRequest) isEmpty() bool {
	return r.Name == nil && r.Description == nil && r.AgentID == nil &&
		r.AllowedIPs == nil && r.AllowedPorts == nil && r.AllowPrivateTargets == nil &&
		r.ICMPEnabled == nil && r.MaxConcurrentFlows == nil && r.PacketRateLimit == nil &&
		len(r.ExpiresAt) == 0 && r.Status == nil
}

// vpnConfigRevealRequest is the acknowledgement body of the reveal action. It
// carries no reason field on purpose: decodeJSON rejects unknown fields, so a
// request that means something this endpoint does not accept cannot be silently
// reinterpreted as a confirmed reveal.
type vpnConfigRevealRequest struct {
	AcknowledgeRisk bool `json:"acknowledgeRisk"`
}

// vpnPeerResponse is the peer summary every endpoint returns except reveal.
//
// The four sealed private-key columns are absent from this type, not merely
// omitted from the JSON: a summary that carried them would leak ciphertext,
// nonce and key id into browser caches, proxy logs and console state. The
// plaintext key exists in exactly one response, config:reveal.
type vpnPeerResponse struct {
	ID                  string                `json:"id"`
	OwnerUserID         string                `json:"ownerUserId"`
	Name                string                `json:"name"`
	Description         string                `json:"description"`
	PublicKey           string                `json:"publicKey"`
	VPNIP               string                `json:"vpnIp"`
	NodeID              string                `json:"nodeId"`
	AgentID             string                `json:"agentId"`
	AllowedIPs          []string              `json:"allowedIps"`
	AllowedPorts        []int                 `json:"allowedPorts"`
	AllowPrivateTargets bool                  `json:"allowPrivateTargets"`
	ICMPEnabled         bool                  `json:"icmpEnabled"`
	MaxConcurrentFlows  int                   `json:"maxConcurrentFlows"`
	PacketRateLimit     int                   `json:"packetRateLimit"`
	ExpiresAt           *string               `json:"expiresAt"`
	Status              storage.VPNPeerStatus `json:"status"`
	CreatedAt           string                `json:"createdAt"`
	UpdatedAt           string                `json:"updatedAt"`
}

// vpnPeerCreateResponse adds the reveal path to the summary. It is a fact about
// the resource rather than a promise about availability: the gateway still has
// to hold an identity before it can render a configuration.
type vpnPeerCreateResponse struct {
	vpnPeerResponse
	ConfigRevealPath string `json:"configRevealPath"`
}

type vpnConfigRevealResponse struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	VPNIP  string `json:"vpnIp"`
	Format string `json:"format"`
	Config string `json:"config"`
}

// vpnConfigFormat names the rendered file format. It is a field rather than an
// assumption so a future client kind can be added without a second endpoint.
const vpnConfigFormat = "wg-quick"

// publicVPNPeer maps a persistence row onto its wire form. The policy columns are
// parsed back into arrays, because a console counts and renders entries rather
// than splitting comma-joined text. A parse failure means the row was corrupted
// or hand-edited, and reporting it beats rendering an empty policy that looks
// like "nothing is reachable".
func publicVPNPeer(peer storage.VPNPeer) (vpnPeerResponse, error) {
	networks, err := vpn.ParseAllowedIPs(peer.AllowedIPs)
	if err != nil {
		return vpnPeerResponse{}, err
	}
	ports, err := vpn.ParseAllowedPorts(peer.AllowedPorts)
	if err != nil {
		return vpnPeerResponse{}, err
	}
	allowedIPs := make([]string, 0, len(networks))
	for _, network := range networks {
		allowedIPs = append(allowedIPs, network.String())
	}
	if ports == nil {
		ports = []int{}
	}
	return vpnPeerResponse{
		ID: peer.ID, OwnerUserID: peer.OwnerID, Name: peer.Name, Description: peer.Description,
		PublicKey: peer.PublicKey, VPNIP: peer.VPNIP, NodeID: peer.NodeID, AgentID: peer.AgentID,
		AllowedIPs: allowedIPs, AllowedPorts: ports,
		AllowPrivateTargets: peer.AllowPrivateTargets, ICMPEnabled: peer.ICMPEnabled,
		MaxConcurrentFlows: peer.MaxConcurrentFlows, PacketRateLimit: peer.PacketRateLimit,
		ExpiresAt: timeString(peer.ExpiresAt), Status: peer.Status,
		CreatedAt: tmString(peer.CreatedAt), UpdatedAt: tmString(peer.UpdatedAt),
	}, nil
}

func vpnPeerResponses(peers []storage.VPNPeer) ([]any, error) {
	items := make([]any, 0, len(peers))
	for _, peer := range peers {
		response, err := publicVPNPeer(peer)
		if err != nil {
			return nil, err
		}
		items = append(items, response)
	}
	return items, nil
}

func vpnConfigRevealPath(peerID string) string {
	return "/api/v1/vpn-peers/" + peerID + "/config:reveal"
}

// vpnExpiresAt distinguishes the three states one JSON field can carry: absent
// (leave the stored value alone), null (clear it) and an instant (set it). A
// pointer alone cannot express the difference between absent and null, which is
// why the raw bytes are inspected here rather than decoded into a *time.Time.
func vpnExpiresAt(raw json.RawMessage) (*time.Time, bool, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, false, nil
	}
	if bytes.Equal(trimmed, []byte("null")) {
		return nil, true, nil
	}
	var value time.Time
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return nil, false, err
	}
	return &value, true, nil
}

// SetVPN assembles the peer management service from the loaded server
// configuration. It is called once at runtime startup, next to the other
// configuration-driven setters.
//
// The gateway's own WireGuard identity is left empty on purpose. It is generated
// and persisted together with the data plane, and until it exists config:reveal
// answers 409 vpn_node_disabled rather than rendering a file whose [Peer]
// PublicKey is blank, which would fail on the user's machine with no pointer
// back to the server. Everything else in the surface is fully functional.
func (a *API) SetVPN(cfg config.VPNConfig, nodeID string) {
	if a == nil || a.DB == nil {
		return
	}
	a.vpnPeerService = NewVPNPeerService(VPNPeerServiceDeps{
		Peers:  a.DB.VPNPeers(),
		Leases: a.DB.VPNIPLeases(),
		Agents: a.DB.Agents(),
		Audits: a.DB.Audits(),
		// The encryption key is injected by the environment or a secret manager.
		// A missing key leaves the store nil, and the service fails closed with
		// 503 credential_secret_unavailable rather than storing a plaintext key.
		Secrets: envSecretStore(),
		Config: VPNServiceConfig{
			Enabled: cfg.Enabled, IPPool: cfg.IPPool, NodeSubnetSize: cfg.NodeSubnetSize,
			EndpointHost: cfg.EndpointHost, Listen: cfg.Listen, MTU: cfg.MTU,
			MaxPeers: cfg.MaxPeers, ICMPEnabled: cfg.ICMPEnabled,
		},
		NodeID:   nodeID,
		LeaseTTL: vpnSubnetLeaseTTL,
		// The probe reads this API's live session and cluster state, so an agent
		// that negotiated stream_icmp_echo.v1 is what makes a peer pingable
		// rather than a release note.
		AgentCapabilities: a.vpnAgentCapabilityProbe(nodeID),
	})
}

// SetVPNPeerService installs an explicitly assembled service. Tests and
// embedders use it, and the gateway runtime will use it to supply the node
// identity it generates.
func (a *API) SetVPNPeerService(service *VPNPeerService) {
	if a == nil {
		return
	}
	a.vpnPeerService = service
}

// handleVPNPeers routes the peer collection and its actions. Routing is by shape
// first and by method second, so an unknown sub-resource is a 404 and a known
// one reached with the wrong verb is a 405: the two answers must not collapse
// into each other, or a console cannot tell a typo from a bug.
func (a *API) handleVPNPeers(w http.ResponseWriter, r *http.Request, p auth.Principal, parts []string) {
	if a.vpnPeerService == nil {
		// 503 rather than 404: the surface exists in this build, it was simply
		// never wired to a database, and a 404 would read as "no such feature".
		writeAPIError(w, http.StatusServiceUnavailable, "vpn service unavailable")
		return
	}
	switch {
	case len(parts) == 0:
		a.handleVPNPeerListCreate(w, r, p)
	case len(parts) == 1:
		a.handleVPNPeerItem(w, r, p, parts[0])
	case len(parts) == 2:
		a.handleVPNPeerAction(w, r, p, parts[0], parts[1])
	default:
		writeAPIError(w, http.StatusNotFound, "not found")
	}
}

// handleVPNNodes answers the gateway fleet status endpoint. It is registered in
// this release and refuses with the stable code, because the status it would
// report (listening sockets, allocated addresses, ICMP capability) is produced by
// the gateway runtime, which does not exist yet. A 200 with an empty list would
// be read as "no node serves VPN" rather than "this build cannot tell".
func (a *API) handleVPNNodes(w http.ResponseWriter, r *http.Request, _ auth.Principal, parts []string) {
	if len(parts) != 0 {
		writeAPIError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeVPNError(w, vpn.ErrNotImplemented.WithMessage("vpn node status needs the gateway runtime, which ships with the data plane"))
}

func (a *API) handleVPNPeerListCreate(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	switch r.Method {
	case http.MethodGet:
		a.listVPNPeers(w, r, p)
	case http.MethodPost:
		a.createVPNPeer(w, r, p)
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// listVPNPeers renders one cursor page. The owner restriction is applied by the
// service from the authenticated principal, never from a query parameter, so a
// caller cannot widen their own view by asking for somebody else's rows.
func (a *API) listVPNPeers(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	query := r.URL.Query()
	filter := storage.VPNPeerFilter{
		Keyword: strings.TrimSpace(query.Get("keyword")),
		NodeID:  strings.TrimSpace(query.Get("nodeId")),
		AgentID: strings.TrimSpace(query.Get("agentId")),
		Status:  storage.VPNPeerStatus(strings.TrimSpace(query.Get("status"))),
	}
	page, err := a.vpnPeerService.List(r.Context(), p, filter, query.Get("cursor"), queryLimit(r))
	if err != nil {
		writeVPNError(w, err)
		return
	}
	items, err := vpnPeerResponses(page.Items)
	if err != nil {
		writeVPNError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pageData(items, page.NextCursor, page.HasMore))
}

func (a *API) createVPNPeer(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	var req vpnPeerRequest
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	expiresAt, _, err := vpnExpiresAt(req.ExpiresAt)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid expiresAt")
		return
	}
	input := VPNPeerInput{
		Name: req.Name, Description: req.Description, AgentID: req.AgentID,
		AllowedIPs: req.AllowedIPs, AllowedPorts: req.AllowedPorts,
		AllowPrivateTargets: req.AllowPrivateTargets, ICMPEnabled: req.ICMPEnabled,
		MaxConcurrentFlows: req.MaxConcurrentFlows, PacketRateLimit: req.PacketRateLimit,
		ExpiresAt: expiresAt,
	}
	status, data, err := a.mutate(r, p, func() (int, any, error) {
		created, err := a.vpnPeerService.Issue(r.Context(), p, input)
		if err != nil {
			return 0, nil, err
		}
		response, err := publicVPNPeer(created)
		if err != nil {
			return 0, nil, err
		}
		return http.StatusCreated, vpnPeerCreateResponse{
			vpnPeerResponse:  response,
			ConfigRevealPath: vpnConfigRevealPath(created.ID),
		}, nil
	})
	if err != nil {
		writeVPNError(w, err)
		return
	}
	writeStored(w, status, data)
}

func (a *API) handleVPNPeerItem(w http.ResponseWriter, r *http.Request, p auth.Principal, peerID string) {
	switch r.Method {
	case http.MethodGet:
		peer, err := a.vpnPeerService.Get(r.Context(), p, peerID)
		if err != nil {
			writeVPNError(w, err)
			return
		}
		response, err := publicVPNPeer(peer)
		if err != nil {
			writeVPNError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, response)
	case http.MethodPatch:
		a.patchVPNPeer(w, r, p, peerID)
	case http.MethodDelete:
		a.revokeVPNPeer(w, r, p, peerID)
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *API) patchVPNPeer(w http.ResponseWriter, r *http.Request, p auth.Principal, peerID string) {
	var req vpnPeerPatchRequest
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.isEmpty() {
		// An empty patch is a caller bug, not a no-op. Answering 200 would hide
		// a console that silently submitted nothing and let the user believe the
		// change was saved.
		writeAPIError(w, http.StatusBadRequest, "at least one field must be provided")
		return
	}
	expiresAt, hasExpiresAt, err := vpnExpiresAt(req.ExpiresAt)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid expiresAt")
		return
	}
	patch := VPNPeerPatch{
		Name: req.Name, Description: req.Description, AgentID: req.AgentID,
		AllowPrivateTargets: req.AllowPrivateTargets, ICMPEnabled: req.ICMPEnabled,
		MaxConcurrentFlows: req.MaxConcurrentFlows, PacketRateLimit: req.PacketRateLimit,
		HasExpiresAt: hasExpiresAt, ExpiresAt: expiresAt, Status: req.Status,
	}
	if req.AllowedIPs != nil {
		patch.HasAllowedIPs, patch.AllowedIPs = true, *req.AllowedIPs
	}
	if req.AllowedPorts != nil {
		patch.HasAllowedPorts, patch.AllowedPorts = true, *req.AllowedPorts
	}
	status, data, err := a.mutate(r, p, func() (int, any, error) {
		updated, err := a.vpnPeerService.Update(r.Context(), p, peerID, patch)
		if err != nil {
			return 0, nil, err
		}
		response, err := publicVPNPeer(updated)
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, response, nil
	})
	if err != nil {
		writeVPNError(w, err)
		return
	}
	writeStored(w, status, data)
}

// revokeVPNPeer answers DELETE with the revoked peer rather than an empty body,
// so the console can update the row it already rendered without a second read.
func (a *API) revokeVPNPeer(w http.ResponseWriter, r *http.Request, p auth.Principal, peerID string) {
	status, data, err := a.mutate(r, p, func() (int, any, error) {
		if err := a.vpnPeerService.Revoke(r.Context(), p, peerID); err != nil {
			return 0, nil, err
		}
		revoked, err := a.vpnPeerService.Get(r.Context(), p, peerID)
		if err != nil {
			return 0, nil, err
		}
		response, err := publicVPNPeer(revoked)
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, response, nil
	})
	if err != nil {
		writeVPNError(w, err)
		return
	}
	writeStored(w, status, data)
}

func (a *API) handleVPNPeerAction(w http.ResponseWriter, r *http.Request, p auth.Principal, peerID, action string) {
	switch action {
	case "rotate":
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		a.rotateVPNPeer(w, r, p, peerID)
	case "config:reveal":
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		a.revealVPNPeerConfig(w, r, p, peerID)
	case "flows":
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		a.listVPNPeerFlows(w, r, p, peerID)
	default:
		writeAPIError(w, http.StatusNotFound, "not found")
	}
}

// rotateVPNPeer is wrapped in the idempotency store, and that is not a
// formality: a rotation invalidates the configuration the user has already
// downloaded, so a retried request that rotated twice would leave them holding a
// file that no longer works, with no error to explain it.
func (a *API) rotateVPNPeer(w http.ResponseWriter, r *http.Request, p auth.Principal, peerID string) {
	status, data, err := a.mutate(r, p, func() (int, any, error) {
		rotated, err := a.vpnPeerService.Rotate(r.Context(), p, peerID)
		if err != nil {
			return 0, nil, err
		}
		response, err := publicVPNPeer(rotated)
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, response, nil
	})
	if err != nil {
		writeVPNError(w, err)
		return
	}
	writeStored(w, status, data)
}

// revealVPNPeerConfig follows the approved tokens/{tokenId}/reveal pattern: an
// explicit confirmation header, an idempotency key, an acknowledgement in the
// body, Cache-Control: no-store and an audit entry.
//
// It deliberately does not go through a.mutate, which is the one deviation from
// the create and rotate paths. That helper persists the response body so a
// replay can be answered identically, and persisting this body would write a
// peer private key into the idempotency table in the clear. The key is required
// as a confirmation that the caller knows what they are doing, not as a cache
// key; a second confirmed reveal simply renders the sealed key again, which is
// safe because the key is recoverable by design.
func (a *API) revealVPNPeerConfig(w http.ResponseWriter, r *http.Request, p auth.Principal, peerID string) {
	// Visibility is resolved first, so the precondition errors cannot be used to
	// probe which peer IDs exist.
	peer, err := a.vpnPeerService.Get(r.Context(), p, peerID)
	if err != nil {
		writeVPNError(w, err)
		return
	}
	if strings.TrimSpace(r.Header.Get("X-VPN-Config-Reveal-Confirm")) == "" {
		writeAPIError(w, http.StatusBadRequest, "X-VPN-Config-Reveal-Confirm is required")
		return
	}
	if strings.TrimSpace(r.Header.Get("Idempotency-Key")) == "" {
		writeAPIError(w, http.StatusBadRequest, "Idempotency-Key is required")
		return
	}
	var confirmation vpnConfigRevealRequest
	if err := decodeJSON(r, &confirmation); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if !confirmation.AcknowledgeRisk {
		writeAPIError(w, http.StatusBadRequest, "acknowledgeRisk=true is required")
		return
	}
	rendered, err := a.vpnPeerService.RevealConfig(r.Context(), p, peerID)
	if err != nil {
		writeVPNError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, http.StatusOK, vpnConfigRevealResponse{
		ID: peer.ID, Name: peer.Name, VPNIP: peer.VPNIP,
		Format: vpnConfigFormat, Config: rendered,
	})
}

// listVPNPeerFlows refuses with the stable code, but only after the peer has
// been resolved: answering 501 for an identifier that does not exist, or that
// belongs to somebody else, would turn the route into an existence oracle.
func (a *API) listVPNPeerFlows(w http.ResponseWriter, r *http.Request, p auth.Principal, peerID string) {
	if _, err := a.vpnPeerService.Get(r.Context(), p, peerID); err != nil {
		writeVPNError(w, err)
		return
	}
	writeVPNError(w, vpn.ErrNotImplemented.WithMessage("active vpn flows need the gateway flow table, which ships with the data plane"))
}

// writeVPNError renders the {code,msg,data} envelope with the stable vpn code in
// data.error and the human detail in msg, matching writeProxyEntryError so one
// client-side parser covers the proxy entry and the management API. The code is
// what a console branches on and what a metric label may carry; the message is
// what it shows.
//
// An error that is not a *vpn.Error belongs to storage or to the process, and
// falls through to the shared mapping so the driver text and the
// unique-constraint to 409 rule stay in one place.
func writeVPNError(w http.ResponseWriter, err error) {
	apiErr := asVPNError(err)
	if apiErr == nil {
		writeStorageError(w, err)
		return
	}
	body, marshalErr := json.Marshal(apiEnvelope{
		Code: apiErr.Status,
		Msg:  apiErr.Error(),
		Data: map[string]any{"error": apiErr.Code},
	})
	if marshalErr != nil {
		body = []byte(`{"code":500,"msg":"vpn error","data":null}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(apiErr.Status)
	_, _ = w.Write(body)
}

func asVPNError(err error) *vpn.Error {
	var apiErr *vpn.Error
	if errors.As(err, &apiErr) {
		return apiErr
	}
	return nil
}
