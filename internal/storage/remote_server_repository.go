package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

const remoteServerColumns = `id,owner_user_id,name,host,port,default_username,credential_id,agent_id,enabled,deleted_at,last_connected_at,last_result,last_error_class,created_at,updated_at`

type remoteServerRepo struct{ db *sql.DB }

func NewRemoteServerRepository(db *sql.DB) RemoteServerRepository {
	return &remoteServerRepo{db: db}
}

func (r *remoteServerRepo) Create(ctx context.Context, server RemoteServer) (RemoteServer, error) {
	if err := validateRemoteServer(server); err != nil {
		return RemoteServer{}, err
	}
	server.ID, server.CreatedAt, server.UpdatedAt = stamp(server.ID, server.CreatedAt, server.UpdatedAt, "remote-server")
	_, err := r.db.ExecContext(ctx, `INSERT INTO remote_servers(`+remoteServerColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		server.ID, server.OwnerUserID, server.Name, server.Host, server.Port, server.DefaultUsername, nullableString(server.CredentialID),
		server.AgentID, boolInt(server.Enabled), nullableTime(server.DeletedAt), nullableTime(server.LastConnectedAt),
		nullableString(server.LastResult), nullableString(server.LastErrorClass), tm(server.CreatedAt), tm(server.UpdatedAt))
	if err != nil {
		return RemoteServer{}, err
	}
	return server, nil
}

func (r *remoteServerRepo) Get(ctx context.Context, id string) (RemoteServer, error) {
	if id == "" {
		return RemoteServer{}, sql.ErrNoRows
	}
	return scanRemoteServer(r.db.QueryRowContext(ctx, `SELECT `+remoteServerColumns+` FROM remote_servers WHERE id=?`, id))
}

func (r *remoteServerRepo) Update(ctx context.Context, server RemoteServer) error {
	if err := validateRemoteServer(server); err != nil {
		return err
	}
	if server.UpdatedAt.IsZero() {
		server.UpdatedAt = time.Now().UTC()
	}
	res, err := r.db.ExecContext(ctx, `UPDATE remote_servers SET owner_user_id=?,name=?,host=?,port=?,default_username=?,credential_id=?,agent_id=?,enabled=?,deleted_at=?,updated_at=? WHERE id=?`,
		server.OwnerUserID, server.Name, server.Host, server.Port, server.DefaultUsername, nullableString(server.CredentialID),
		server.AgentID, boolInt(server.Enabled), nullableTime(server.DeletedAt), tm(server.UpdatedAt), server.ID)
	return checkAffected(res, err)
}

func (r *remoteServerRepo) Delete(ctx context.Context, id string, when time.Time) error {
	when = timeOrNow(when)
	res, err := r.db.ExecContext(ctx, `UPDATE remote_servers SET deleted_at=CASE WHEN deleted_at IS NULL THEN ? ELSE deleted_at END,enabled=0,updated_at=? WHERE id=?`, tm(when), tm(when), id)
	return checkAffected(res, err)
}

func (r *remoteServerRepo) Restore(ctx context.Context, id string) error {
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `UPDATE remote_servers SET deleted_at=NULL,updated_at=? WHERE id=?`, tm(now), id)
	return checkAffected(res, err)
}

func (r *remoteServerRepo) List(ctx context.Context, filter RemoteServerFilter, cursor string, limit int) (Page[RemoteServer], error) {
	cursor, limit = pageArgs(cursor, limit)
	conditions, args := remoteServerConditions(filter)
	if id := decodeCursor(cursor); id != "" {
		conditions = append(conditions, `id>?`)
		args = append(args, id)
	}
	query := `SELECT ` + remoteServerColumns + ` FROM remote_servers`
	if len(conditions) > 0 {
		query += ` WHERE ` + strings.Join(conditions, ` AND `)
	}
	query += ` ORDER BY id LIMIT ?`
	args = append(args, limit+1)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return Page[RemoteServer]{}, err
	}
	defer rows.Close()
	page := Page[RemoteServer]{}
	for rows.Next() {
		server, err := scanRemoteServer(rows)
		if err != nil {
			return Page[RemoteServer]{}, err
		}
		page.Items = append(page.Items, server)
	}
	if err := rows.Err(); err != nil {
		return Page[RemoteServer]{}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.HasMore = true
		page.NextCursor = encodeCursor(page.Items[len(page.Items)-1].ID)
	}
	return page, nil
}

func (r *remoteServerRepo) UpdateConnectionResult(ctx context.Context, id, result, errorClass string, connectedAt time.Time) error {
	connectedAt = timeOrNow(connectedAt)
	res, err := r.db.ExecContext(ctx, `UPDATE remote_servers SET last_connected_at=?,last_result=?,last_error_class=?,updated_at=? WHERE id=?`,
		tm(connectedAt), result, errorClass, tm(connectedAt), id)
	return checkAffected(res, err)
}

func validateRemoteServer(server RemoteServer) error {
	if server.OwnerUserID == "" || strings.TrimSpace(server.Name) == "" || server.Host == "" || server.AgentID == "" || server.DefaultUsername == "" {
		return errors.New("remote server owner, name, host, username and agent are required")
	}
	if server.Port < 1 || server.Port > 65535 {
		return errors.New("remote server port is invalid")
	}
	return nil
}

func remoteServerConditions(filter RemoteServerFilter) ([]string, []any) {
	conditions := make([]string, 0, 4)
	args := make([]any, 0, 5)
	if filter.OwnerUserID != "" {
		conditions = append(conditions, `owner_user_id=?`)
		args = append(args, filter.OwnerUserID)
	}
	if filter.AgentID != "" {
		conditions = append(conditions, `agent_id=?`)
		args = append(args, filter.AgentID)
	}
	switch filter.Status {
	case RemoteServerStatusEnabled:
		conditions = append(conditions, `deleted_at IS NULL`, `enabled=1`)
	case RemoteServerStatusDisabled:
		conditions = append(conditions, `deleted_at IS NULL`, `enabled=0`)
	case RemoteServerStatusDeleted:
		conditions = append(conditions, `deleted_at IS NOT NULL`)
	case RemoteServerStatusAll, "":
		conditions = append(conditions, `deleted_at IS NULL`)
	default:
		conditions = append(conditions, `1=0`)
	}
	if filter.Keyword != "" {
		keyword := boundedAgentFilter(filter.Keyword)
		conditions = append(conditions, `(INSTR(name, ?)>0 OR INSTR(host, ?)>0 OR INSTR(default_username, ?)>0)`)
		args = append(args, keyword, keyword, keyword)
	}
	return conditions, args
}

func scanRemoteServer(row rowScanner) (RemoteServer, error) {
	var server RemoteServer
	var credentialID sql.NullString
	var deleted, connected, created, updated sql.NullString
	var result, errorClass sql.NullString
	var enabled int
	if err := row.Scan(&server.ID, &server.OwnerUserID, &server.Name, &server.Host, &server.Port, &server.DefaultUsername,
		&credentialID, &server.AgentID, &enabled, &deleted, &connected, &result, &errorClass, &created, &updated); err != nil {
		return RemoteServer{}, err
	}
	server.CredentialID = credentialID.String
	server.Enabled = enabled != 0
	server.DeletedAt = parseTM(deleted)
	server.LastConnectedAt = parseTM(connected)
	server.LastResult = result.String
	server.LastErrorClass = errorClass.String
	server.CreatedAt = parseTime(created.String)
	server.UpdatedAt = parseTime(updated.String)
	return server, nil
}
