package server

import (
	"context"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// auditActorNames labels the actors of an audit page in one batch query.
//
// An audit row only stores the actor's identifier, which is right for a durable
// record but unreadable in the console, where "who did this" is the first question.
// The names resolve over the rows the caller is already allowed to read - the audit
// list is administrator-only and the overview is filtered to the caller's own
// resources in SQL - so this adds a label, not a new way to read the user table.
//
// Resolution is deliberately best effort: an account deleted after the event, or a
// failing lookup, leaves that actor unnamed instead of turning a historical list
// into an error.
func (a *API) auditActorNames(ctx context.Context, items []storage.AuditLog) map[string]string {
	names := map[string]string{}
	if a == nil || len(items) == 0 {
		return names
	}
	actors := make([]string, 0, len(items))
	for _, item := range items {
		if item.ActorUserID != "" {
			actors = append(actors, item.ActorUserID)
		}
	}
	if len(actors) == 0 {
		return names
	}
	batch, ok := a.users.(storage.UserBatchRepository)
	if !ok {
		return names
	}
	users, err := batch.GetByIDs(ctx, actors)
	if err != nil {
		return names
	}
	for id, user := range users {
		names[id] = user.Username
	}
	return names
}
