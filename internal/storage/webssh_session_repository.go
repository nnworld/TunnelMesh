package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

var ErrWebSSHTicketInvalid = errors.New("webssh ticket is invalid or expired")

const webSSHSessionColumns = `id,owner_user_id,remote_server_id,agent_id,owner_node_id,ticket_hash,ticket_expires_at,status,created_at,expires_at,connected_at,closed_at,close_reason`

type webSSHSessionRepo struct{ db *sql.DB }

func NewWebSSHSessionRepository(db *sql.DB) WebSSHSessionRepository {
	return &webSSHSessionRepo{db: db}
}

func (r *webSSHSessionRepo) Create(ctx context.Context, session WebSSHSession) (WebSSHSession, error) {
	if err := validateWebSSHSession(session); err != nil {
		return WebSSHSession{}, err
	}
	session.ID, session.CreatedAt, _ = stamp(session.ID, session.CreatedAt, time.Time{}, "webssh")
	_, err := r.db.ExecContext(ctx, `INSERT INTO webssh_sessions(`+webSSHSessionColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		session.ID, session.OwnerUserID, session.RemoteServerID, session.AgentID, session.OwnerNodeID, session.TicketHash,
		tm(session.TicketExpiresAt), string(session.Status), tm(session.CreatedAt), tm(session.ExpiresAt),
		nullableTime(session.ConnectedAt), nullableTime(session.ClosedAt), session.CloseReason)
	if err != nil {
		return WebSSHSession{}, err
	}
	return session, nil
}

func (r *webSSHSessionRepo) Get(ctx context.Context, id string) (WebSSHSession, error) {
	if id == "" {
		return WebSSHSession{}, sql.ErrNoRows
	}
	return scanWebSSHSession(r.db.QueryRowContext(ctx, `SELECT `+webSSHSessionColumns+` FROM webssh_sessions WHERE id=?`, id))
}

func (r *webSSHSessionRepo) ConsumeTicket(ctx context.Context, id, ticketHash string, now time.Time) (WebSSHSession, error) {
	now = timeOrNow(now)
	res, err := r.db.ExecContext(ctx, `UPDATE webssh_sessions SET status=?,connected_at=? WHERE id=? AND ticket_hash=? AND status=? AND ticket_expires_at>?`,
		string(WebSSHSessionActive), tm(now), id, ticketHash, string(WebSSHSessionPending), tm(now))
	if err != nil {
		return WebSSHSession{}, err
	}
	if affected, err := res.RowsAffected(); err != nil {
		return WebSSHSession{}, err
	} else if affected == 0 {
		return WebSSHSession{}, ErrWebSSHTicketInvalid
	}
	return r.Get(ctx, id)
}

func (r *webSSHSessionRepo) Close(ctx context.Context, id, reason string, now time.Time) error {
	now = timeOrNow(now)
	res, err := r.db.ExecContext(ctx, `UPDATE webssh_sessions SET status=?,closed_at=?,close_reason=?,expires_at=CASE WHEN expires_at>? THEN expires_at ELSE ? END WHERE id=? AND status IN (?,?)`,
		string(WebSSHSessionClosed), tm(now), reason, tm(now), tm(now), id, string(WebSSHSessionPending), string(WebSSHSessionActive))
	return checkAffected(res, err)
}

func (r *webSSHSessionRepo) CountActiveByOwner(ctx context.Context, ownerUserID string, now time.Time) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM webssh_sessions WHERE owner_user_id=? AND status=? AND expires_at>?`,
		ownerUserID, string(WebSSHSessionActive), tm(timeOrNow(now))).Scan(&count)
	return count, err
}

func (r *webSSHSessionRepo) ListActiveByOwner(ctx context.Context, ownerUserID string, now time.Time) ([]WebSSHSession, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+webSSHSessionColumns+` FROM webssh_sessions WHERE owner_user_id=? AND status=? AND expires_at>? ORDER BY created_at,id`,
		ownerUserID, string(WebSSHSessionActive), tm(timeOrNow(now)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sessions []WebSSHSession
	for rows.Next() {
		session, err := scanWebSSHSession(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

// ListActivePage pages one owner's active sessions ordered by ID. The admin
// console uses it so a user can disconnect a stuck session and release the
// per-user active-session quota. Rows whose expires_at has passed are excluded:
// the sweeper reclaims them without a bridge, so offering them would disconnect
// nothing. The cursor semantics mirror credentialRepo.List to keep the API
// contract uniform.
func (r *webSSHSessionRepo) ListActivePage(ctx context.Context, ownerUserID string, now time.Time, cursor string, limit int) (Page[WebSSHSession], error) {
	cursor, limit = pageArgs(cursor, limit)
	conditions := []string{`owner_user_id=?`, `status=?`, `expires_at>?`}
	args := []any{ownerUserID, string(WebSSHSessionActive), tm(timeOrNow(now))}
	if id := decodeCursor(cursor); id != "" {
		conditions = append(conditions, `id>?`)
		args = append(args, id)
	}
	query := `SELECT ` + webSSHSessionColumns + ` FROM webssh_sessions WHERE ` + strings.Join(conditions, ` AND `) + ` ORDER BY id LIMIT ?`
	args = append(args, limit+1)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return Page[WebSSHSession]{}, err
	}
	defer rows.Close()
	page := Page[WebSSHSession]{}
	for rows.Next() {
		session, err := scanWebSSHSession(rows)
		if err != nil {
			return Page[WebSSHSession]{}, err
		}
		page.Items = append(page.Items, session)
	}
	if err := rows.Err(); err != nil {
		return Page[WebSSHSession]{}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.HasMore = true
		page.NextCursor = encodeCursor(page.Items[len(page.Items)-1].ID)
	}
	return page, nil
}

func (r *webSSHSessionRepo) ExpirePending(ctx context.Context, now time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE webssh_sessions SET status=?,closed_at=?,close_reason=? WHERE status=? AND ticket_expires_at<=?`,
		string(WebSSHSessionExpired), tm(timeOrNow(now)), "ticket_expired", string(WebSSHSessionPending), tm(timeOrNow(now)))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (r *webSSHSessionRepo) CloseExpiredActive(ctx context.Context, now time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE webssh_sessions SET status=?,closed_at=?,close_reason=? WHERE status=? AND expires_at<=?`,
		string(WebSSHSessionExpired), tm(timeOrNow(now)), "session_expired", string(WebSSHSessionActive), tm(timeOrNow(now)))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CloseActiveByNode closes active sessions owned by one server node. It runs
// when that node starts: a restarted process cannot hold bridges for sessions
// persisted by its predecessor, and leaving them active would exhaust the
// per-user active-session quota with dead rows.
func (r *webSSHSessionRepo) CloseActiveByNode(ctx context.Context, nodeID string, now time.Time, reason string) (int64, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE webssh_sessions SET status=?,closed_at=?,close_reason=? WHERE status=? AND owner_node_id=?`,
		string(WebSSHSessionClosed), tm(timeOrNow(now)), reason, string(WebSSHSessionActive), nodeID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func validateWebSSHSession(session WebSSHSession) error {
	if session.OwnerUserID == "" || session.RemoteServerID == "" || session.AgentID == "" || session.OwnerNodeID == "" {
		return errors.New("webssh session owner, server, agent and node are required")
	}
	if session.TicketHash == "" || session.TicketExpiresAt.IsZero() || session.ExpiresAt.IsZero() || session.Status == "" {
		return errors.New("webssh session ticket and lifecycle fields are required")
	}
	return nil
}

func scanWebSSHSession(row rowScanner) (WebSSHSession, error) {
	var session WebSSHSession
	var status string
	var ticketExpires, created, expires sql.NullString
	var connected, closed sql.NullString
	var reason sql.NullString
	if err := row.Scan(&session.ID, &session.OwnerUserID, &session.RemoteServerID, &session.AgentID, &session.OwnerNodeID,
		&session.TicketHash, &ticketExpires, &status, &created, &expires, &connected, &closed, &reason); err != nil {
		return WebSSHSession{}, err
	}
	session.Status = WebSSHSessionStatus(status)
	session.TicketExpiresAt = parseTime(ticketExpires.String)
	session.CreatedAt = parseTime(created.String)
	session.ExpiresAt = parseTime(expires.String)
	session.ConnectedAt = parseTM(connected)
	session.ClosedAt = parseTM(closed)
	session.CloseReason = reason.String
	return session, nil
}
