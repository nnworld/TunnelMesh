package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func TestStreamAuthorizerRechecksTokenScopeAndAgentPolicyForEveryOpen(t *testing.T) {
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, "file:stream-authorizer?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authService := auth.NewAuthService(db)
	owner, err := authService.CreateUser(ctx, "stream-owner", "stream-password", "user")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Agents().Create(ctx, storage.Agent{ID: "agent-a", Name: "agent-a", OwnerUserID: owner.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := db.Policies().Create(ctx, storage.AgentPolicy{ID: "allow-ssh", AgentID: "agent-a", TargetHost: "10.0.0.8", TargetPort: 22, Protocol: "tcp", AllowedCIDRs: "10.0.0.0/24", AllowedPorts: "22", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	credentials := auth.NewCredentialService(db)
	defer credentials.Close()
	created, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeClient, OwnerUserID: owner.ID, Scope: auth.TokenScope{AgentIDs: []string{"agent-a"}, Protocols: []string{"tcp"}, TargetCIDRs: []string{"10.0.0.0/24"}, TargetPorts: []int{22}}})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := credentials.ValidateAs(ctx, created.Secret, storage.TokenTypeClient)
	if err != nil {
		t.Fatal(err)
	}
	authorizer := NewCredentialStreamAuthorizer(credentials)
	principal := ClientSessionPrincipal{ConnectionID: "connection-a", Identity: identity}
	allowed := protocol.StreamOpenPayload{AgentID: "agent-a", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22}
	if err := authorizer.Authorize(ctx, principal, allowed); err != nil {
		t.Fatalf("Authorize(allowed) error = %v", err)
	}

	denied := allowed
	denied.TargetPort = 23
	if err := authorizer.Authorize(ctx, principal, denied); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("Authorize(policy denied) error = %v, want ErrForbidden", err)
	}
	if err := credentials.Revoke(ctx, created.TokenID); err != nil {
		t.Fatal(err)
	}
	if err := authorizer.Authorize(ctx, principal, allowed); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("Authorize(after revoke) error = %v, want ErrForbidden", err)
	}
}
