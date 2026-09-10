package storage

import (
	"context"
	"fmt"
	"time"
)

type DashboardSummary struct {
	AgentsTotal        int
	AgentsOnline       int
	ActiveTunnels      int
	ManagedRoutes      int
	ValidServiceTokens int
	RecentEvents       []AuditLog
}

type DashboardRepository interface {
	Summary(context.Context, string, int) (DashboardSummary, error)
}

type dashboardRepo struct{ db dbExecutor }

func (r *dashboardRepo) Summary(ctx context.Context, ownerUserID string, recentLimit int) (DashboardSummary, error) {
	if recentLimit <= 0 || recentLimit > 50 {
		recentLimit = 10
	}
	now := tm(time.Now().UTC())
	result := DashboardSummary{}
	counts := []struct {
		target *int
		all    string
		owned  string
		args   []any
	}{
		{&result.AgentsTotal, `SELECT COUNT(*) FROM agents`, `SELECT COUNT(*) FROM agents WHERE owner_user_id=?`, nil},
		{&result.AgentsOnline, `SELECT COUNT(DISTINCT l.agent_id) FROM agent_connection_leases l JOIN agents a ON a.id=l.agent_id WHERE l.expires_at>? AND a.enabled=1`, `SELECT COUNT(DISTINCT l.agent_id) FROM agent_connection_leases l JOIN agents a ON a.id=l.agent_id WHERE l.expires_at>? AND a.enabled=1 AND a.owner_user_id=?`, []any{now}},
		{&result.ActiveTunnels, `SELECT COUNT(*) FROM tunnels WHERE status='active'`, `SELECT COUNT(*) FROM tunnels t JOIN agents a ON a.id=t.agent_id WHERE t.status='active' AND a.owner_user_id=?`, nil},
		{&result.ManagedRoutes, `SELECT COUNT(*) FROM tunnels WHERE domain IS NOT NULL AND domain<>''`, `SELECT COUNT(*) FROM tunnels t JOIN agents a ON a.id=t.agent_id WHERE t.domain IS NOT NULL AND t.domain<>'' AND a.owner_user_id=?`, nil},
		{&result.ValidServiceTokens,
			`SELECT COUNT(*) FROM service_tokens s JOIN users u ON u.id=s.owner_user_id WHERE u.disabled=0 AND u.deleted_at IS NULL AND s.revoked_at IS NULL AND (s.expires_at IS NULL OR s.expires_at>?) AND (s.token_type='client' OR (s.token_type='agent' AND EXISTS(SELECT 1 FROM agents a WHERE a.id=s.agent_id AND a.owner_user_id=s.owner_user_id AND a.enabled=1)) OR (s.token_type='server_node' AND EXISTS(SELECT 1 FROM server_nodes n WHERE n.id=s.node_id AND (n.expires_at IS NULL OR n.expires_at>?))))`,
			`SELECT COUNT(*) FROM service_tokens s JOIN users u ON u.id=s.owner_user_id WHERE u.disabled=0 AND u.deleted_at IS NULL AND s.revoked_at IS NULL AND (s.expires_at IS NULL OR s.expires_at>?) AND (s.token_type='client' OR (s.token_type='agent' AND EXISTS(SELECT 1 FROM agents a WHERE a.id=s.agent_id AND a.owner_user_id=s.owner_user_id AND a.enabled=1)) OR (s.token_type='server_node' AND EXISTS(SELECT 1 FROM server_nodes n WHERE n.id=s.node_id AND (n.expires_at IS NULL OR n.expires_at>?)))) AND s.owner_user_id=?`, []any{now, now}},
	}
	for _, count := range counts {
		statement := count.all
		args := append([]any(nil), count.args...)
		if ownerUserID != "" {
			statement = count.owned
			args = append(args, ownerUserID)
		}
		if err := r.db.QueryRowContext(ctx, statement, args...).Scan(count.target); err != nil {
			return DashboardSummary{}, fmt.Errorf("dashboard count: %w", err)
		}
	}

	statement := `SELECT id,actor_user_id,action,resource_type,resource_id,details,created_at FROM audit_logs`
	args := make([]any, 0, 2)
	if ownerUserID != "" {
		statement += ` WHERE actor_user_id=?`
		args = append(args, ownerUserID)
	}
	statement += ` ORDER BY created_at DESC,id DESC LIMIT ?`
	args = append(args, recentLimit)
	rows, err := r.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return DashboardSummary{}, fmt.Errorf("dashboard recent events: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var event AuditLog
		var created string
		if err := rows.Scan(&event.ID, &event.ActorUserID, &event.Action, &event.ResourceType, &event.ResourceID, &event.Details, &created); err != nil {
			return DashboardSummary{}, err
		}
		event.CreatedAt = parseTime(created)
		result.RecentEvents = append(result.RecentEvents, event)
	}
	if err := rows.Err(); err != nil {
		return DashboardSummary{}, err
	}
	return result, nil
}
