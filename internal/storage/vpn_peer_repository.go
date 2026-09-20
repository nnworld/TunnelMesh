package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// vpnPeerColumns lists every column of vpn_peers in DDL order. Holding the list
// in one constant keeps the INSERT, the UPDATE and every SELECT from drifting
// apart when a column is added.
const vpnPeerColumns = `id,name,owner_id,public_key,private_key_ciphertext,private_key_nonce,private_key_key_id,private_key_version,vpn_ip,node_id,agent_id,allowed_ips,allowed_ports,allow_private_targets,icmp_enabled,max_concurrent_flows,packet_rate_limit,expires_at,status,description,created_at,updated_at`

type vpnPeerRepo struct{ db *sql.DB }

func NewVPNPeerRepository(db *sql.DB) VPNPeerRepository {
	return &vpnPeerRepo{db: db}
}

// validateVPNPeer enforces the invariants the DDL cannot express: the identity
// fields the gateway needs to build a WireGuard configuration, a status from the
// closed set, and non-negative resource caps. It runs before every write so a
// bad record fails at the boundary instead of producing an unroutable peer.
func validateVPNPeer(peer VPNPeer) error {
	switch {
	case strings.TrimSpace(peer.OwnerID) == "":
		return errors.New("vpn peer owner is required")
	case strings.TrimSpace(peer.Name) == "":
		return errors.New("vpn peer name is required")
	case strings.TrimSpace(peer.PublicKey) == "":
		return errors.New("vpn peer public key is required")
	case strings.TrimSpace(peer.VPNIP) == "":
		return errors.New("vpn peer address is required")
	case strings.TrimSpace(peer.NodeID) == "":
		return errors.New("vpn peer node is required")
	case strings.TrimSpace(peer.AgentID) == "":
		return errors.New("vpn peer agent is required")
	}
	switch peer.Status {
	case VPNPeerStatusActive, VPNPeerStatusDisabled, VPNPeerStatusRevoked:
	case "":
		return errors.New("vpn peer status is required")
	default:
		return errors.New("unsupported vpn peer status " + string(peer.Status))
	}
	// The sealed private key is all-or-nothing. AES-GCM ciphertext without its
	// nonce can never be opened, so persisting half of it would create a peer
	// whose configuration is unrecoverable. The key id stays optional because
	// TUNNELMESH_TOKEN_ENCRYPTION_KEY_ID is optional by design.
	if (peer.PrivateKeyCiphertext == "") != (peer.PrivateKeyNonce == "") {
		return errors.New("vpn peer private key ciphertext and nonce must be set together")
	}
	if peer.MaxConcurrentFlows < 0 {
		return errors.New("vpn peer max concurrent flows must not be negative")
	}
	if peer.PacketRateLimit < 0 {
		return errors.New("vpn peer packet rate limit must not be negative")
	}
	return nil
}

func (r *vpnPeerRepo) Create(ctx context.Context, peer VPNPeer) (VPNPeer, error) {
	if err := validateVPNPeer(peer); err != nil {
		return VPNPeer{}, err
	}
	peer.ID, peer.CreatedAt, peer.UpdatedAt = stamp(peer.ID, peer.CreatedAt, peer.UpdatedAt, "vpn-peer")
	_, err := r.db.ExecContext(ctx, `INSERT INTO vpn_peers(`+vpnPeerColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		peer.ID, peer.Name, peer.OwnerID, peer.PublicKey,
		peer.PrivateKeyCiphertext, peer.PrivateKeyNonce, peer.PrivateKeyKeyID, peer.PrivateKeyVersion,
		peer.VPNIP, peer.NodeID, peer.AgentID, peer.AllowedIPs, peer.AllowedPorts,
		boolInt(peer.AllowPrivateTargets), boolInt(peer.ICMPEnabled),
		peer.MaxConcurrentFlows, peer.PacketRateLimit, nullableTime(peer.ExpiresAt),
		string(peer.Status), peer.Description, tm(peer.CreatedAt), tm(peer.UpdatedAt))
	// Both unique constraints (public_key and (node_id, vpn_ip)) tell a caller
	// the same thing: this peer cannot be created as asked.
	if isDuplicateError(err) {
		return VPNPeer{}, ErrVPNPeerConflict
	}
	if err != nil {
		return VPNPeer{}, err
	}
	return peer, nil
}

func (r *vpnPeerRepo) Get(ctx context.Context, id string) (VPNPeer, error) {
	if strings.TrimSpace(id) == "" {
		return VPNPeer{}, sql.ErrNoRows
	}
	return scanVPNPeer(r.db.QueryRowContext(ctx, `SELECT `+vpnPeerColumns+` FROM vpn_peers WHERE id=?`, id))
}

// GetByPublicKey is the handshake entry point: an inbound WireGuard packet
// identifies its sender by key, so this lookup sits on the hot path and is
// backed by the UNIQUE index on public_key.
func (r *vpnPeerRepo) GetByPublicKey(ctx context.Context, publicKey string) (VPNPeer, error) {
	if strings.TrimSpace(publicKey) == "" {
		return VPNPeer{}, sql.ErrNoRows
	}
	return scanVPNPeer(r.db.QueryRowContext(ctx, `SELECT `+vpnPeerColumns+` FROM vpn_peers WHERE public_key=?`, publicKey))
}

// GetByNodeAndIP resolves an address inside one node's pool. The pair, not the
// address alone, is unique, because every node carves its own /24 out of the
// shared ip_pool and may legitimately reuse an address another node holds.
func (r *vpnPeerRepo) GetByNodeAndIP(ctx context.Context, nodeID, vpnIP string) (VPNPeer, error) {
	if strings.TrimSpace(nodeID) == "" || strings.TrimSpace(vpnIP) == "" {
		return VPNPeer{}, sql.ErrNoRows
	}
	return scanVPNPeer(r.db.QueryRowContext(ctx, `SELECT `+vpnPeerColumns+` FROM vpn_peers WHERE node_id=? AND vpn_ip=?`, nodeID, vpnIP))
}

func (r *vpnPeerRepo) Update(ctx context.Context, peer VPNPeer) error {
	if err := validateVPNPeer(peer); err != nil {
		return err
	}
	if peer.UpdatedAt.IsZero() {
		peer.UpdatedAt = time.Now().UTC()
	}
	res, err := r.db.ExecContext(ctx, `UPDATE vpn_peers SET name=?,owner_id=?,public_key=?,private_key_ciphertext=?,private_key_nonce=?,private_key_key_id=?,private_key_version=?,vpn_ip=?,node_id=?,agent_id=?,allowed_ips=?,allowed_ports=?,allow_private_targets=?,icmp_enabled=?,max_concurrent_flows=?,packet_rate_limit=?,expires_at=?,status=?,description=?,updated_at=? WHERE id=?`,
		peer.Name, peer.OwnerID, peer.PublicKey,
		peer.PrivateKeyCiphertext, peer.PrivateKeyNonce, peer.PrivateKeyKeyID, peer.PrivateKeyVersion,
		peer.VPNIP, peer.NodeID, peer.AgentID, peer.AllowedIPs, peer.AllowedPorts,
		boolInt(peer.AllowPrivateTargets), boolInt(peer.ICMPEnabled),
		peer.MaxConcurrentFlows, peer.PacketRateLimit, nullableTime(peer.ExpiresAt),
		string(peer.Status), peer.Description, tm(peer.UpdatedAt), peer.ID)
	if isDuplicateError(err) {
		return ErrVPNPeerConflict
	}
	return checkAffected(res, err)
}

// SetStatus moves a peer between lifecycle states. Revoked is terminal and the
// guard is part of the WHERE clause rather than a prior read, so two concurrent
// calls cannot race a revoked peer back to active. Zero rows affected is
// ambiguous between "no such peer" and "already revoked", so one extra read
// resolves it and keeps the two errors distinguishable for callers.
func (r *vpnPeerRepo) SetStatus(ctx context.Context, id string, status VPNPeerStatus, when time.Time) error {
	switch status {
	case VPNPeerStatusActive, VPNPeerStatusDisabled, VPNPeerStatusRevoked:
	default:
		return errors.New("unsupported vpn peer status " + string(status))
	}
	res, err := r.db.ExecContext(ctx, `UPDATE vpn_peers SET status=?,updated_at=? WHERE id=? AND status<>?`,
		string(status), tm(timeOrNow(when)), id, string(VPNPeerStatusRevoked))
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected > 0 {
		return nil
	}
	current, err := r.Get(ctx, id)
	if err != nil {
		return err
	}
	if current.Status == VPNPeerStatusRevoked {
		return ErrVPNPeerRevoked
	}
	// The row already carries the requested status and timestamp, so MySQL
	// reports zero changed rows. That is a no-op, not a failure.
	return nil
}

func (r *vpnPeerRepo) List(ctx context.Context, filter VPNPeerFilter, cursor string, limit int) (Page[VPNPeer], error) {
	cursor, limit = pageArgs(cursor, limit)
	conditions, args := vpnPeerConditions(filter)
	if id := decodeCursor(cursor); id != "" {
		conditions = append(conditions, `id>?`)
		args = append(args, id)
	}
	query := `SELECT ` + vpnPeerColumns + ` FROM vpn_peers`
	if len(conditions) > 0 {
		query += ` WHERE ` + strings.Join(conditions, ` AND `)
	}
	query += ` ORDER BY id LIMIT ?`
	args = append(args, limit+1)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return Page[VPNPeer]{}, err
	}
	defer rows.Close()
	page := Page[VPNPeer]{}
	for rows.Next() {
		peer, err := scanVPNPeer(rows)
		if err != nil {
			return Page[VPNPeer]{}, err
		}
		page.Items = append(page.Items, peer)
	}
	if err := rows.Err(); err != nil {
		return Page[VPNPeer]{}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.HasMore = true
		page.NextCursor = encodeCursor(page.Items[len(page.Items)-1].ID)
	}
	return page, nil
}

// ListByNode returns the peers a gateway node loads into its WireGuard
// configuration: everything that is not revoked, in a stable id order so a node
// can diff two consecutive reads without re-sorting. Revoked peers keep their
// row for audit but must never reach the data plane again.
func (r *vpnPeerRepo) ListByNode(ctx context.Context, nodeID string) ([]VPNPeer, error) {
	if strings.TrimSpace(nodeID) == "" {
		return []VPNPeer{}, nil
	}
	rows, err := r.db.QueryContext(ctx, `SELECT `+vpnPeerColumns+` FROM vpn_peers WHERE node_id=? AND status<>? ORDER BY id`,
		nodeID, string(VPNPeerStatusRevoked))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	peers := []VPNPeer{}
	for rows.Next() {
		peer, err := scanVPNPeer(rows)
		if err != nil {
			return nil, err
		}
		peers = append(peers, peer)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return peers, nil
}

// CountByNode reports the peers a node is currently serving, so revoked peers
// are excluded: they hold no gateway slot.
func (r *vpnPeerRepo) CountByNode(ctx context.Context, nodeID string) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM vpn_peers WHERE node_id=? AND status<>?`,
		nodeID, string(VPNPeerStatusRevoked)).Scan(&count)
	return count, err
}

// CountByOwner reports every peer an owner has, revoked ones included. Whether a
// revoked peer still consumes quota is a product decision made above this
// layer; the repository reports the fact and lets the caller subtract.
func (r *vpnPeerRepo) CountByOwner(ctx context.Context, ownerID string) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM vpn_peers WHERE owner_id=?`, ownerID).Scan(&count)
	return count, err
}

// vpnPeerConditions builds the WHERE clause for List. OwnerUserID is a condition
// like any other so permission filtering happens inside pagination semantics;
// applying it after the query would silently truncate a user's own page.
// Keyword uses INSTR rather than LIKE, matching every other entity search in
// this package: there are no wildcards to escape and no collation surprises.
func vpnPeerConditions(filter VPNPeerFilter) ([]string, []any) {
	conditions := make([]string, 0, 5)
	args := make([]any, 0, 6)
	if filter.OwnerUserID != "" {
		conditions = append(conditions, `owner_id=?`)
		args = append(args, filter.OwnerUserID)
	}
	if filter.NodeID != "" {
		conditions = append(conditions, `node_id=?`)
		args = append(args, filter.NodeID)
	}
	if filter.AgentID != "" {
		conditions = append(conditions, `agent_id=?`)
		args = append(args, filter.AgentID)
	}
	switch filter.Status {
	case VPNPeerStatusActive, VPNPeerStatusDisabled, VPNPeerStatusRevoked:
		conditions = append(conditions, `status=?`)
		args = append(args, string(filter.Status))
	case "":
	default:
		// An unknown status narrows to nothing rather than to everything, so a
		// typo in a filter value cannot widen a scoped query into a full read.
		conditions = append(conditions, `1=0`)
	}
	if filter.Keyword != "" {
		keyword := boundedAgentFilter(filter.Keyword)
		conditions = append(conditions, `(INSTR(name, ?)>0 OR INSTR(description, ?)>0)`)
		args = append(args, keyword, keyword)
	}
	return conditions, args
}

func scanVPNPeer(row rowScanner) (VPNPeer, error) {
	var peer VPNPeer
	var status, description sql.NullString
	// The four sealed private key columns stay NULL until the gateway stores key
	// material, so they must be scanned as nullable.
	var ciphertext, nonce, keyID sql.NullString
	var version sql.NullInt64
	var allowPrivateTargets, icmpEnabled int
	var expires, created, updated sql.NullString
	if err := row.Scan(&peer.ID, &peer.Name, &peer.OwnerID, &peer.PublicKey,
		&ciphertext, &nonce, &keyID, &version,
		&peer.VPNIP, &peer.NodeID, &peer.AgentID, &peer.AllowedIPs, &peer.AllowedPorts,
		&allowPrivateTargets, &icmpEnabled, &peer.MaxConcurrentFlows, &peer.PacketRateLimit,
		&expires, &status, &description, &created, &updated); err != nil {
		return VPNPeer{}, err
	}
	peer.PrivateKeyCiphertext, peer.PrivateKeyNonce, peer.PrivateKeyKeyID = ciphertext.String, nonce.String, keyID.String
	peer.PrivateKeyVersion = int(version.Int64)
	peer.AllowPrivateTargets, peer.ICMPEnabled = allowPrivateTargets != 0, icmpEnabled != 0
	peer.ExpiresAt = parseTM(expires)
	peer.Status = VPNPeerStatus(status.String)
	peer.Description = description.String
	peer.CreatedAt = parseTime(created.String)
	peer.UpdatedAt = parseTime(updated.String)
	return peer, nil
}
