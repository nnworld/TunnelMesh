package storage

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"
)

const clientInstanceColumns = `id,owner_user_id,instance_id,metadata,capabilities,reported_at,last_seen_at,expires_at,stale,updated_at`
const clientConnectionColumns = `connection_id,client_instance_id,token_id,owner_user_id,server_node_id,connection_epoch,active_streams,health_score,acquired_at,expires_at,updated_at`

type clientInstanceRepo struct {
	db     *sql.DB
	driver string
}

type clientConnectionRepo struct {
	db     *sql.DB
	driver string
}

func (r *clientInstanceRepo) Upsert(ctx context.Context, instance ClientInstance) (ClientInstance, error) {
	if r == nil || r.db == nil {
		return ClientInstance{}, errors.New("client instance repository is unavailable")
	}
	if instance.OwnerUserID == "" || instance.InstanceID == "" {
		return ClientInstance{}, errors.New("client instance owner and instance id are required")
	}
	instance.ID, instance.ReportedAt, instance.UpdatedAt = stamp(instance.ID, instance.ReportedAt, instance.UpdatedAt, "client-instance")
	if instance.LastSeenAt.IsZero() {
		instance.LastSeenAt = instance.ReportedAt
	}
	if instance.Metadata == "" {
		instance.Metadata = "{}"
	}
	if instance.Capabilities == "" {
		instance.Capabilities = "[]"
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return ClientInstance{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if r.driver == DriverSQLite {
		if _, err := tx.ExecContext(ctx, `UPDATE schema_meta SET version=version WHERE id=1`); err != nil {
			return ClientInstance{}, err
		}
	}

	lockQuery := `SELECT id FROM client_instance_metadata WHERE owner_user_id=? AND instance_id=?`
	if r.driver == DriverMySQL {
		lockQuery += ` FOR UPDATE`
	}
	var existingID string
	err = tx.QueryRowContext(ctx, lockQuery, instance.OwnerUserID, instance.InstanceID).Scan(&existingID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err = tx.ExecContext(ctx, `INSERT INTO client_instance_metadata(`+clientInstanceColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			instance.ID, instance.OwnerUserID, instance.InstanceID, instance.Metadata, instance.Capabilities,
			tm(instance.ReportedAt), tm(instance.LastSeenAt), nullableTime(instance.ExpiresAt), boolInt(instance.Stale), tm(instance.UpdatedAt)); err != nil {
			return ClientInstance{}, err
		}
	case err != nil:
		return ClientInstance{}, err
	default:
		instance.ID = existingID
		if _, err = tx.ExecContext(ctx, `UPDATE client_instance_metadata SET metadata=?,capabilities=?,reported_at=?,last_seen_at=?,expires_at=?,stale=?,updated_at=? WHERE id=?`,
			instance.Metadata, instance.Capabilities, tm(instance.ReportedAt), tm(instance.LastSeenAt),
			nullableTime(instance.ExpiresAt), boolInt(instance.Stale), tm(instance.UpdatedAt), existingID); err != nil {
			return ClientInstance{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ClientInstance{}, err
	}
	return instance, nil
}

func (r *clientInstanceRepo) GetByOwnerAndInstance(ctx context.Context, ownerUserID, instanceID string) (ClientInstance, error) {
	if ownerUserID == "" || instanceID == "" {
		return ClientInstance{}, sql.ErrNoRows
	}
	return scanClientInstance(r.db.QueryRowContext(ctx, `SELECT `+clientInstanceColumns+` FROM client_instance_metadata WHERE owner_user_id=? AND instance_id=?`, ownerUserID, instanceID))
}

func (r *clientInstanceRepo) Get(ctx context.Context, id string) (ClientInstance, error) {
	if id == "" {
		return ClientInstance{}, sql.ErrNoRows
	}
	return scanClientInstance(r.db.QueryRowContext(ctx, `SELECT `+clientInstanceColumns+` FROM client_instance_metadata WHERE id=?`, id))
}

func (r *clientInstanceRepo) List(ctx context.Context, filter ClientInstanceFilter, cursor string, limit int) (Page[ClientInstance], error) {
	cursor, limit = pageArgs(cursor, limit)
	conditions, args := clientInstanceConditions(filter, time.Now().UTC())
	if updated, id, ok := decodeCompositeCursor(cursor); ok {
		conditions = append(conditions, `(updated_at>? OR (updated_at=? AND id>?))`)
		args = append(args, updated, updated, id)
	}
	query := `SELECT ` + clientInstanceColumns + ` FROM client_instance_metadata`
	if len(conditions) > 0 {
		query += ` WHERE ` + strings.Join(conditions, ` AND `)
	}
	query += ` ORDER BY updated_at,id LIMIT ?`
	args = append(args, limit+1)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return Page[ClientInstance]{}, err
	}
	defer rows.Close()
	page := Page[ClientInstance]{}
	for rows.Next() {
		instance, err := scanClientInstanceRows(rows)
		if err != nil {
			return Page[ClientInstance]{}, err
		}
		page.Items = append(page.Items, instance)
	}
	if err := rows.Err(); err != nil {
		return Page[ClientInstance]{}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.HasMore = true
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeCursor(tm(last.UpdatedAt) + "\x00" + last.ID)
	}
	return page, nil
}

func (r *clientInstanceRepo) TouchInstance(ctx context.Context, ownerUserID, instanceID string, lastSeenAt, expiresAt time.Time) error {
	if ownerUserID == "" || instanceID == "" || lastSeenAt.IsZero() || expiresAt.IsZero() {
		return errors.New("invalid client instance touch")
	}
	res, err := r.db.ExecContext(ctx, `UPDATE client_instance_metadata SET last_seen_at=?,expires_at=?,stale=0,updated_at=? WHERE owner_user_id=? AND instance_id=?`,
		tm(lastSeenAt), tm(expiresAt), tm(lastSeenAt), ownerUserID, instanceID)
	return checkAffected(res, err)
}

func (r *clientInstanceRepo) MarkStale(ctx context.Context, id string, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	res, err := r.db.ExecContext(ctx, `UPDATE client_instance_metadata SET stale=1,updated_at=? WHERE id=?`, tm(at), id)
	return checkAffected(res, err)
}

func (r *clientInstanceRepo) MarkExpired(ctx context.Context, at time.Time) (int64, error) {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	res, err := r.db.ExecContext(ctx, `UPDATE client_instance_metadata SET stale=1,updated_at=? WHERE stale=0 AND (expires_at IS NULL OR expires_at<=?)`, tm(at), tm(at))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (r *clientConnectionRepo) Register(ctx context.Context, lease ClientConnectionLease) (ClientConnectionLease, error) {
	if r == nil || r.db == nil {
		return ClientConnectionLease{}, errors.New("client connection repository is unavailable")
	}
	if err := validateClientConnection(lease); err != nil {
		return ClientConnectionLease{}, err
	}
	if lease.UpdatedAt.IsZero() {
		lease.UpdatedAt = lease.AcquiredAt
	}
	if lease.HealthScore == 0 {
		lease.HealthScore = 100
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return ClientConnectionLease{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if r.driver == DriverSQLite {
		if _, err := tx.ExecContext(ctx, `UPDATE schema_meta SET version=version WHERE id=1`); err != nil {
			return ClientConnectionLease{}, err
		}
	}
	lockQuery := `SELECT connection_epoch,client_instance_id FROM client_connection_leases WHERE connection_id=?`
	if r.driver == DriverMySQL {
		lockQuery += ` FOR UPDATE`
	}
	var existingEpoch int64
	var existingInstanceID string
	err = tx.QueryRowContext(ctx, lockQuery, lease.ConnectionID).Scan(&existingEpoch, &existingInstanceID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err = tx.ExecContext(ctx, `INSERT INTO client_connection_leases(`+clientConnectionColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			lease.ConnectionID, lease.ClientInstanceID, lease.TokenID, lease.OwnerUserID, lease.ServerNodeID, lease.ConnectionEpoch,
			lease.ActiveStreams, lease.HealthScore, tm(lease.AcquiredAt), tm(lease.ExpiresAt), tm(lease.UpdatedAt)); err != nil {
			return ClientConnectionLease{}, err
		}
	case err != nil:
		return ClientConnectionLease{}, err
	case existingInstanceID != lease.ClientInstanceID:
		return ClientConnectionLease{}, errors.New("client connection id belongs to another client instance")
	case lease.ConnectionEpoch >= existingEpoch:
		if _, err = tx.ExecContext(ctx, `UPDATE client_connection_leases SET token_id=?,owner_user_id=?,server_node_id=?,connection_epoch=?,active_streams=?,health_score=?,expires_at=?,updated_at=? WHERE connection_id=?`,
			lease.TokenID, lease.OwnerUserID, lease.ServerNodeID, lease.ConnectionEpoch, lease.ActiveStreams, lease.HealthScore,
			tm(lease.ExpiresAt), tm(lease.UpdatedAt), lease.ConnectionID); err != nil {
			return ClientConnectionLease{}, err
		}
	default:
		lease.ConnectionEpoch = existingEpoch
	}
	if err := tx.Commit(); err != nil {
		return ClientConnectionLease{}, err
	}
	return lease, nil
}

func (r *clientConnectionRepo) Get(ctx context.Context, connectionID string) (ClientConnectionLease, error) {
	if connectionID == "" {
		return ClientConnectionLease{}, sql.ErrNoRows
	}
	return scanClientConnection(r.db.QueryRowContext(ctx, `SELECT `+clientConnectionColumns+` FROM client_connection_leases WHERE connection_id=?`, connectionID))
}

func (r *clientConnectionRepo) Renew(ctx context.Context, connectionID string, epoch int64, ttl time.Duration) error {
	if connectionID == "" || epoch <= 0 || ttl <= 0 {
		return errors.New("invalid client connection renewal")
	}
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `UPDATE client_connection_leases SET expires_at=?,updated_at=? WHERE connection_id=? AND connection_epoch=?`,
		tm(now.Add(ttl)), tm(now), connectionID, epoch)
	return checkAffected(res, err)
}

func (r *clientConnectionRepo) Release(ctx context.Context, connectionID string, epoch int64) error {
	if connectionID == "" || epoch <= 0 {
		return errors.New("invalid client connection release")
	}
	res, err := r.db.ExecContext(ctx, `DELETE FROM client_connection_leases WHERE connection_id=? AND connection_epoch=?`, connectionID, epoch)
	return checkAffected(res, err)
}

func (r *clientConnectionRepo) List(ctx context.Context, filter ClientConnectionFilter, cursor string, limit int) (Page[ClientConnectionLease], error) {
	cursor, limit = pageArgs(cursor, limit)
	conditions, args := clientConnectionConditions(filter, time.Now().UTC())
	if updated, id, ok := decodeCompositeCursor(cursor); ok {
		conditions = append(conditions, `(updated_at>? OR (updated_at=? AND connection_id>?))`)
		args = append(args, updated, updated, id)
	}
	query := `SELECT ` + clientConnectionColumns + ` FROM client_connection_leases`
	if len(conditions) > 0 {
		query += ` WHERE ` + strings.Join(conditions, ` AND `)
	}
	query += ` ORDER BY updated_at,connection_id LIMIT ?`
	args = append(args, limit+1)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return Page[ClientConnectionLease]{}, err
	}
	defer rows.Close()
	page := Page[ClientConnectionLease]{}
	for rows.Next() {
		lease, err := scanClientConnectionRows(rows)
		if err != nil {
			return Page[ClientConnectionLease]{}, err
		}
		page.Items = append(page.Items, lease)
	}
	if err := rows.Err(); err != nil {
		return Page[ClientConnectionLease]{}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.HasMore = true
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeCursor(tm(last.UpdatedAt) + "\x00" + last.ConnectionID)
	}
	return page, nil
}

func (r *clientConnectionRepo) ListByInstance(ctx context.Context, clientInstanceID string) ([]ClientConnectionLease, error) {
	return r.ListByInstances(ctx, []string{clientInstanceID})
}

func (r *clientConnectionRepo) ListByInstances(ctx context.Context, clientInstanceIDs []string) ([]ClientConnectionLease, error) {
	if len(clientInstanceIDs) == 0 {
		return nil, nil
	}
	if len(clientInstanceIDs) > 500 {
		clientInstanceIDs = clientInstanceIDs[:500]
	}
	placeholders := make([]string, len(clientInstanceIDs))
	args := make([]any, len(clientInstanceIDs))
	for i, id := range clientInstanceIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := r.db.QueryContext(ctx, `SELECT `+clientConnectionColumns+` FROM client_connection_leases WHERE client_instance_id IN (`+strings.Join(placeholders, `,`)+`) ORDER BY client_instance_id,connection_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClientConnectionLease
	for rows.Next() {
		lease, err := scanClientConnectionRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, lease)
	}
	return out, rows.Err()
}

func (r *clientConnectionRepo) UpdateStats(ctx context.Context, lease ClientConnectionLease) error {
	if lease.ConnectionID == "" || lease.ConnectionEpoch <= 0 {
		return errors.New("invalid client connection stats")
	}
	updatedAt := lease.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	res, err := r.db.ExecContext(ctx, `UPDATE client_connection_leases SET active_streams=?,health_score=?,updated_at=? WHERE connection_id=? AND connection_epoch=?`,
		lease.ActiveStreams, lease.HealthScore, tm(updatedAt), lease.ConnectionID, lease.ConnectionEpoch)
	return checkAffected(res, err)
}

func clientInstanceConditions(filter ClientInstanceFilter, now time.Time) ([]string, []any) {
	conditions := make([]string, 0, 7)
	args := make([]any, 0, 8)
	if filter.OwnerUserID != "" {
		conditions = append(conditions, `owner_user_id=?`)
		args = append(args, filter.OwnerUserID)
	}
	if filter.TokenID != "" {
		conditions = append(conditions, `EXISTS (SELECT 1 FROM client_connection_leases c WHERE c.client_instance_id=client_instance_metadata.id AND c.token_id=?)`)
		args = append(args, filter.TokenID)
	}
	if filter.ServerNodeID != "" {
		conditions = append(conditions, `EXISTS (SELECT 1 FROM client_connection_leases c WHERE c.client_instance_id=client_instance_metadata.id AND c.server_node_id=?)`)
		args = append(args, filter.ServerNodeID)
	}
	switch filter.Status {
	case "":
	case "online":
		conditions = append(conditions, `EXISTS (SELECT 1 FROM client_connection_leases c WHERE c.client_instance_id=client_instance_metadata.id AND c.expires_at>?)`)
		args = append(args, tm(now))
	case "offline":
		conditions = append(conditions, `NOT EXISTS (SELECT 1 FROM client_connection_leases c WHERE c.client_instance_id=client_instance_metadata.id AND c.expires_at>?)`)
		args = append(args, tm(now))
	case "stale":
		conditions = append(conditions, `stale=1`)
	case "metadata_unavailable":
		conditions = append(conditions, `(capabilities='' OR capabilities='[]')`)
	default:
		// An unsupported status cannot match any row; the API layer validates
		// this value, while the repository remains safe if called directly.
		conditions = append(conditions, `1=0`)
	}
	if filter.AgentID != "" {
		// Match the quoted JSON string so agent-1 does not also match agent-12.
		conditions = append(conditions, `INSTR(metadata, ?)>0`)
		args = append(args, strconv.Quote(boundedAgentFilter(filter.AgentID)))
	}
	if filter.Keyword != "" {
		conditions = append(conditions, `(INSTR(id, ?)>0 OR INSTR(instance_id, ?)>0 OR INSTR(metadata, ?)>0)`)
		args = append(args, boundedAgentFilter(filter.Keyword), boundedAgentFilter(filter.Keyword), boundedAgentFilter(filter.Keyword))
	}
	return conditions, args
}

func clientConnectionConditions(filter ClientConnectionFilter, now time.Time) ([]string, []any) {
	conditions := make([]string, 0, 5)
	args := make([]any, 0, 6)
	if filter.ClientInstanceID != "" {
		conditions = append(conditions, `client_instance_id=?`)
		args = append(args, filter.ClientInstanceID)
	}
	if filter.OwnerUserID != "" {
		conditions = append(conditions, `owner_user_id=?`)
		args = append(args, filter.OwnerUserID)
	}
	if filter.TokenID != "" {
		conditions = append(conditions, `token_id=?`)
		args = append(args, filter.TokenID)
	}
	if filter.ServerNodeID != "" {
		conditions = append(conditions, `server_node_id=?`)
		args = append(args, filter.ServerNodeID)
	}
	if !filter.IncludeExpired {
		conditions = append(conditions, `expires_at>?`)
		args = append(args, tm(now))
	}
	return conditions, args
}

func boundedAgentFilter(value string) string {
	if len(value) > 128 {
		value = value[:128]
	}
	return strings.ReplaceAll(value, "\x00", "")
}

func decodeCompositeCursor(cursor string) (string, string, bool) {
	decoded := decodeCursor(cursor)
	if decoded == "" {
		return "", "", false
	}
	first, second, ok := strings.Cut(decoded, "\x00")
	return first, second, ok
}

func validateClientConnection(lease ClientConnectionLease) error {
	if lease.ConnectionID == "" || lease.ClientInstanceID == "" || lease.TokenID == "" || lease.OwnerUserID == "" || lease.ServerNodeID == "" {
		return errors.New("client connection identity fields are required")
	}
	if lease.ConnectionEpoch <= 0 || lease.ActiveStreams < 0 || lease.HealthScore < 0 || lease.AcquiredAt.IsZero() || lease.ExpiresAt.IsZero() {
		return errors.New("invalid client connection lease")
	}
	if lease.UpdatedAt.IsZero() {
		lease.UpdatedAt = lease.AcquiredAt
	}
	return nil
}

type rowScanner interface{ Scan(dest ...any) error }

func scanClientInstance(row rowScanner) (ClientInstance, error) {
	var instance ClientInstance
	var reported, seen, expires, updated sql.NullString
	var stale int
	if err := row.Scan(&instance.ID, &instance.OwnerUserID, &instance.InstanceID, &instance.Metadata, &instance.Capabilities,
		&reported, &seen, &expires, &stale, &updated); err != nil {
		return ClientInstance{}, err
	}
	instance.ReportedAt = parseTime(reported.String)
	instance.LastSeenAt = parseTime(seen.String)
	instance.UpdatedAt = parseTime(updated.String)
	instance.ExpiresAt = parseTM(expires)
	instance.Stale = stale != 0
	return instance, nil
}

func scanClientInstanceRows(rows *sql.Rows) (ClientInstance, error) {
	return scanClientInstance(rows)
}

func scanClientConnection(row rowScanner) (ClientConnectionLease, error) {
	var lease ClientConnectionLease
	var acquired, expires, updated sql.NullString
	if err := row.Scan(&lease.ConnectionID, &lease.ClientInstanceID, &lease.TokenID, &lease.OwnerUserID, &lease.ServerNodeID,
		&lease.ConnectionEpoch, &lease.ActiveStreams, &lease.HealthScore, &acquired, &expires, &updated); err != nil {
		return ClientConnectionLease{}, err
	}
	lease.AcquiredAt = parseTime(acquired.String)
	lease.ExpiresAt = parseTime(expires.String)
	lease.UpdatedAt = parseTime(updated.String)
	return lease, nil
}

func scanClientConnectionRows(rows *sql.Rows) (ClientConnectionLease, error) {
	return scanClientConnection(rows)
}
