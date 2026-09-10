package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

const (
	ServerNodeAuditUpdated  = "server_node.updated"
	ServerNodeAuditDeleted  = "server_node.deleted"
	ServerNodeAuditRestored = "server_node.restored"
)

var ErrInvalidServerNodeInput = errors.New("invalid server node input")

// ServerNodeView is the non-secret management representation. Secret material
// and relay certificates are never copied into API responses.
type ServerNodeView struct {
	ID                string     `json:"id"`
	Name              string     `json:"name"`
	Address           string     `json:"address"`
	Epoch             int64      `json:"epoch"`
	Enabled           bool       `json:"enabled"`
	DeletedAt         *time.Time `json:"deletedAt"`
	LastSeenAt        *time.Time `json:"lastSeenAt"`
	ExpiresAt         *time.Time `json:"expiresAt"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
	Status            string     `json:"status"`
	ActiveConnections int64      `json:"activeConnections"`
	ActiveStreams     int64      `json:"activeStreams"`
	HealthScore       int64      `json:"healthScore"`
}

type ServerNodePage struct {
	Items      []ServerNodeView `json:"items"`
	NextCursor string           `json:"nextCursor"`
	HasMore    bool             `json:"hasMore"`
}

type UpdateServerNodeInput struct {
	Name    *string
	Enabled *bool
}

type ServerNodeService struct {
	nodes  storage.NodeRepository
	audits storage.AuditRepository
	now    func() time.Time
}

func NewServerNodeService(db *storage.DB) *ServerNodeService {
	service := &ServerNodeService{now: func() time.Time { return time.Now().UTC() }}
	if db != nil {
		service.nodes, service.audits = db.Nodes(), db.Audits()
	}
	return service
}

func (s *ServerNodeService) List(ctx context.Context, cursor string, limit int) (ServerNodePage, error) {
	if s == nil || s.nodes == nil {
		return ServerNodePage{}, sql.ErrConnDone
	}
	page, err := s.nodes.List(ctx, cursor, limit)
	if err != nil {
		return ServerNodePage{}, err
	}
	ids := make([]string, 0, len(page.Items))
	for _, node := range page.Items {
		ids = append(ids, node.ID)
	}
	stats, err := s.nodes.StatsByNodeIDs(ctx, ids)
	if err != nil {
		return ServerNodePage{}, err
	}
	out := ServerNodePage{Items: make([]ServerNodeView, 0, len(page.Items)), NextCursor: page.NextCursor, HasMore: page.HasMore}
	for _, node := range page.Items {
		out.Items = append(out.Items, newServerNodeView(node, stats[node.ID], s.now()))
	}
	return out, nil
}

func (s *ServerNodeService) Get(ctx context.Context, id string) (ServerNodeView, error) {
	if s == nil || s.nodes == nil {
		return ServerNodeView{}, sql.ErrConnDone
	}
	node, err := s.nodes.Get(ctx, id)
	if err != nil {
		return ServerNodeView{}, err
	}
	stats, err := s.nodes.StatsByNodeIDs(ctx, []string{node.ID})
	if err != nil {
		return ServerNodeView{}, err
	}
	return newServerNodeView(node, stats[node.ID], s.now()), nil
}

func (s *ServerNodeService) Update(ctx context.Context, id string, input UpdateServerNodeInput, actor auth.Principal) (ServerNodeView, error) {
	if s == nil || s.nodes == nil {
		return ServerNodeView{}, sql.ErrConnDone
	}
	if input.Name == nil && input.Enabled == nil {
		return ServerNodeView{}, fmt.Errorf("%w: name or enabled is required", ErrInvalidServerNodeInput)
	}
	node, err := s.nodes.Get(ctx, id)
	if err != nil {
		return ServerNodeView{}, err
	}
	if input.Name != nil {
		name := strings.TrimSpace(*input.Name)
		if name == "" || len(name) > 255 {
			return ServerNodeView{}, fmt.Errorf("%w: name must be 1-255 characters", ErrInvalidServerNodeInput)
		}
		node.Name = name
	}
	if input.Enabled != nil {
		node.Enabled = *input.Enabled
	}
	node.UpdatedAt = s.now()
	if err := s.nodes.Update(ctx, node); err != nil {
		return ServerNodeView{}, err
	}
	if err := s.writeAudit(ctx, actor, ServerNodeAuditUpdated, node); err != nil {
		return ServerNodeView{}, err
	}
	return s.Get(ctx, id)
}

func (s *ServerNodeService) Delete(ctx context.Context, id string, actor auth.Principal) (ServerNodeView, error) {
	if s == nil || s.nodes == nil {
		return ServerNodeView{}, sql.ErrConnDone
	}
	node, err := s.nodes.Get(ctx, id)
	if err != nil {
		return ServerNodeView{}, err
	}
	deletedAt := s.now()
	node.DeletedAt, node.Enabled, node.UpdatedAt = &deletedAt, false, deletedAt
	if err := s.nodes.Update(ctx, node); err != nil {
		return ServerNodeView{}, err
	}
	if err := s.writeAudit(ctx, actor, ServerNodeAuditDeleted, node); err != nil {
		return ServerNodeView{}, err
	}
	return s.Get(ctx, id)
}

func (s *ServerNodeService) Restore(ctx context.Context, id string, actor auth.Principal) (ServerNodeView, error) {
	if s == nil || s.nodes == nil {
		return ServerNodeView{}, sql.ErrConnDone
	}
	node, err := s.nodes.Get(ctx, id)
	if err != nil {
		return ServerNodeView{}, err
	}
	node.DeletedAt, node.Enabled, node.UpdatedAt = nil, true, s.now()
	if err := s.nodes.Update(ctx, node); err != nil {
		return ServerNodeView{}, err
	}
	if err := s.writeAudit(ctx, actor, ServerNodeAuditRestored, node); err != nil {
		return ServerNodeView{}, err
	}
	return s.Get(ctx, id)
}

func (s *ServerNodeService) writeAudit(ctx context.Context, actor auth.Principal, action string, node storage.ServerNode) error {
	if s.audits == nil {
		return sql.ErrConnDone
	}
	details, err := json.Marshal(map[string]any{"name": node.Name, "enabled": node.Enabled, "address": node.Address})
	if err != nil {
		return err
	}
	return s.audits.Create(ctx, storage.AuditLog{
		ActorUserID: actor.UserID, Action: action, ResourceType: "server_node",
		ResourceID: node.ID, Details: string(details),
	})
}

func newServerNodeView(node storage.ServerNode, stats storage.ServerNodeStats, now time.Time) ServerNodeView {
	status := "offline"
	switch {
	case node.DeletedAt != nil:
		status = "deleted"
	case !node.Enabled:
		status = "disabled"
	case node.ExpiresAt != nil && node.ExpiresAt.After(now):
		status = "online"
	}
	return ServerNodeView{
		ID: node.ID, Name: node.Name, Address: node.Address, Epoch: node.Epoch,
		Enabled: node.Enabled, DeletedAt: node.DeletedAt, LastSeenAt: node.LastSeenAt,
		ExpiresAt: node.ExpiresAt, CreatedAt: node.CreatedAt, UpdatedAt: node.UpdatedAt,
		Status: status, ActiveConnections: stats.ActiveConnections,
		ActiveStreams: stats.ActiveStreams, HealthScore: stats.HealthScore,
	}
}
