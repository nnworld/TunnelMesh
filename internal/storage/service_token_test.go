package storage

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func TestServiceTokenRepositoryContractSQLite(t *testing.T) {
	runServiceTokenRepositoryContract(t, newTestDB(t))
}

func runServiceTokenRepositoryContract(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.SQL().ExecContext(ctx, `DELETE FROM service_tokens WHERE id LIKE 'contract-service-token-%' OR token_hash LIKE 'contract-service-token-%'`); err != nil {
		t.Fatalf("clean service token fixtures: %v", err)
	}
	defer func() {
		_, _ = db.SQL().ExecContext(context.Background(), `DELETE FROM service_tokens WHERE id LIKE 'contract-service-token-%' OR token_hash LIKE 'contract-service-token-%'`)
	}()

	repo := db.ServiceTokens()
	createdAt := time.Date(2026, 9, 6, 8, 0, 0, 0, time.UTC)
	expiresAt := createdAt.Add(24 * time.Hour)
	base := ServiceToken{
		ID:          "contract-service-token-10-base",
		OwnerUserID: "owner-main",
		AgentID:     "agent-main",
		Prefix:      "tm_agent_abc",
		TokenHash:   "contract-service-token-hash-base",
		Scope:       `["tunnel:connect"]`,
		Type:        TokenTypeAgent,
		ExpiresAt:   &expiresAt,
		CreatedAt:   createdAt,
		UpdatedAt:   createdAt,
	}
	if err := repo.Create(ctx, base); err != nil {
		t.Fatalf("create service token: %v", err)
	}
	got, err := repo.Get(ctx, base.ID)
	if err != nil {
		t.Fatalf("get service token: %v", err)
	}
	assertServiceToken(t, got, base)
	got, err = repo.GetByHash(ctx, base.TokenHash)
	if err != nil {
		t.Fatalf("get service token by hash: %v", err)
	}
	assertServiceToken(t, got, base)

	duplicate := base
	duplicate.ID = "contract-service-token-11-duplicate"
	if err := repo.Create(ctx, duplicate); err == nil {
		t.Fatal("create duplicate service token hash succeeded")
	}

	filtered := []ServiceToken{
		{ID: "contract-service-token-20-other-owner", OwnerUserID: "owner-other", AgentID: "agent-filter", NodeID: "node-filter", Prefix: "p20", TokenHash: "contract-service-token-hash-20", Scope: `[]`, Type: TokenTypeAgent},
		{ID: "contract-service-token-21-other-type", OwnerUserID: "owner-filter", AgentID: "agent-filter", NodeID: "node-filter", Prefix: "p21", TokenHash: "contract-service-token-hash-21", Scope: `[]`, Type: TokenTypeClient},
		{ID: "contract-service-token-22-other-agent", OwnerUserID: "owner-filter", AgentID: "agent-other", NodeID: "node-filter", Prefix: "p22", TokenHash: "contract-service-token-hash-22", Scope: `[]`, Type: TokenTypeAgent},
		{ID: "contract-service-token-23-other-node", OwnerUserID: "owner-filter", AgentID: "agent-filter", NodeID: "node-other", Prefix: "p23", TokenHash: "contract-service-token-hash-23", Scope: `[]`, Type: TokenTypeAgent},
		{ID: "contract-service-token-24-match", OwnerUserID: "owner-filter", AgentID: "agent-filter", NodeID: "node-filter", Prefix: "p24", TokenHash: "contract-service-token-hash-24", Scope: `[]`, Type: TokenTypeAgent},
		{ID: "contract-service-token-25-match", OwnerUserID: "owner-filter", AgentID: "agent-filter", NodeID: "node-filter", Prefix: "p25", TokenHash: "contract-service-token-hash-25", Scope: `[]`, Type: TokenTypeAgent},
	}
	for _, token := range filtered {
		if err := repo.Create(ctx, token); err != nil {
			t.Fatalf("create filtered service token %s: %v", token.ID, err)
		}
	}
	filter := ServiceTokenFilter{OwnerUserID: "owner-filter", Type: TokenTypeAgent, AgentID: "agent-filter", NodeID: "node-filter"}
	page, err := repo.List(ctx, filter, "", 1)
	if err != nil {
		t.Fatalf("list filtered service tokens: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "contract-service-token-24-match" || !page.HasMore || page.NextCursor == "" {
		t.Fatalf("first filtered page = %+v", page)
	}
	page, err = repo.List(ctx, filter, page.NextCursor, 1)
	if err != nil {
		t.Fatalf("list second filtered service token page: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "contract-service-token-25-match" || page.HasMore || page.NextCursor != "" {
		t.Fatalf("second filtered page = %+v", page)
	}

	lastUsedAt := createdAt.Add(time.Hour)
	if err := repo.TouchLastUsed(ctx, base.ID, lastUsedAt); err != nil {
		t.Fatalf("touch service token last used: %v", err)
	}
	got, err = repo.Get(ctx, base.ID)
	if err != nil {
		t.Fatalf("get touched service token: %v", err)
	}
	if got.LastUsedAt == nil || !got.LastUsedAt.Equal(lastUsedAt) {
		t.Fatalf("last used at = %v, want %v", got.LastUsedAt, lastUsedAt)
	}

	revokedAt := createdAt.Add(2 * time.Hour)
	if err := repo.Revoke(ctx, base.ID, revokedAt); err != nil {
		t.Fatalf("revoke service token: %v", err)
	}
	got, err = repo.Get(ctx, base.ID)
	if err != nil {
		t.Fatalf("get revoked service token: %v", err)
	}
	if got.RevokedAt == nil || !got.RevokedAt.Equal(revokedAt) {
		t.Fatalf("revoked at = %v, want %v", got.RevokedAt, revokedAt)
	}

	old := ServiceToken{ID: "contract-service-token-30-rotate-old", OwnerUserID: "owner-rotate", Prefix: "old", TokenHash: "contract-service-token-hash-30", Scope: `[]`, Type: TokenTypeClient}
	if err := repo.Create(ctx, old); err != nil {
		t.Fatalf("create old rotation token: %v", err)
	}
	replacement := ServiceToken{ID: "contract-service-token-31-rotate-new", OwnerUserID: "owner-rotate", Prefix: "new", TokenHash: "contract-service-token-hash-31", Scope: `["route:read"]`, Type: TokenTypeClient}
	rotationTime := createdAt.Add(3 * time.Hour)
	if err := repo.Rotate(ctx, old.ID, replacement, rotationTime); err != nil {
		t.Fatalf("rotate service token: %v", err)
	}
	rotatedOld, err := repo.Get(ctx, old.ID)
	if err != nil {
		t.Fatalf("get rotated old token: %v", err)
	}
	if rotatedOld.RevokedAt == nil || !rotatedOld.RevokedAt.Equal(rotationTime) {
		t.Fatalf("rotated old revoked at = %v, want %v", rotatedOld.RevokedAt, rotationTime)
	}
	rotatedNew, err := repo.GetByHash(ctx, replacement.TokenHash)
	if err != nil || rotatedNew.ID != replacement.ID {
		t.Fatalf("rotated replacement = %+v, err=%v", rotatedNew, err)
	}
	if err := repo.Rotate(ctx, old.ID, ServiceToken{ID: "contract-service-token-32-revoked-replacement", Prefix: "x", TokenHash: "contract-service-token-hash-32", Scope: `[]`, Type: TokenTypeClient}, rotationTime.Add(time.Minute)); err == nil {
		t.Fatal("rotating an already revoked service token succeeded")
	}
	if _, err := repo.Get(ctx, "contract-service-token-32-revoked-replacement"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("revoked-token replacement lookup error = %v, want sql.ErrNoRows", err)
	}

	rollbackOld := ServiceToken{ID: "contract-service-token-40-rollback-old", Prefix: "old", TokenHash: "contract-service-token-hash-40", Scope: `[]`, Type: TokenTypeServerNode}
	collision := ServiceToken{ID: "contract-service-token-41-collision", Prefix: "collision", TokenHash: "contract-service-token-hash-41", Scope: `[]`, Type: TokenTypeServerNode}
	if err := repo.Create(ctx, rollbackOld); err != nil {
		t.Fatalf("create rollback old token: %v", err)
	}
	if err := repo.Create(ctx, collision); err != nil {
		t.Fatalf("create collision token: %v", err)
	}
	failedReplacement := ServiceToken{ID: "contract-service-token-42-failed-replacement", Prefix: "failed", TokenHash: collision.TokenHash, Scope: `[]`, Type: TokenTypeServerNode}
	if err := repo.Rotate(ctx, rollbackOld.ID, failedReplacement, rotationTime); err == nil {
		t.Fatal("rotation with duplicate replacement hash succeeded")
	}
	rollbackOldAfter, err := repo.Get(ctx, rollbackOld.ID)
	if err != nil {
		t.Fatalf("get rollback old token: %v", err)
	}
	if rollbackOldAfter.RevokedAt != nil {
		t.Fatalf("rollback old token revoked at = %v, want nil", rollbackOldAfter.RevokedAt)
	}
	if _, err := repo.Get(ctx, failedReplacement.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("failed replacement lookup error = %v, want sql.ErrNoRows", err)
	}
}

func TestServiceTokenRepositoryTransactionRunner(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	repo := db.ServiceTokens()
	old := ServiceToken{ID: "contract-service-token-tx-old", Prefix: "old", TokenHash: "contract-service-token-hash-tx-old", Scope: `[]`, Type: TokenTypeAgent}
	if err := repo.Create(ctx, old); err != nil {
		t.Fatal(err)
	}
	replacement := ServiceToken{ID: "contract-service-token-tx-new", Prefix: "new", TokenHash: "contract-service-token-hash-tx-new", Scope: `[]`, Type: TokenTypeAgent}
	when := time.Date(2026, 9, 6, 9, 0, 0, 0, time.UTC)
	if err := db.ServiceTokenTransaction(ctx, func(tokens ServiceTokenRepository, audits AuditRepository) error {
		if err := tokens.Rotate(ctx, old.ID, replacement, when); err != nil {
			return err
		}
		return audits.Create(ctx, AuditLog{ID: "audit-service-token-tx-commit", Action: "rotate", ResourceType: "service_token", ResourceID: old.ID})
	}); err != nil {
		t.Fatalf("commit service token transaction: %v", err)
	}
	if _, err := repo.Get(ctx, replacement.ID); err != nil {
		t.Fatalf("get committed replacement: %v", err)
	}
	assertAuditExists(t, db.Audits(), "audit-service-token-tx-commit", true)

	rollbackOld := ServiceToken{ID: "contract-service-token-tx-rollback-old", Prefix: "old", TokenHash: "contract-service-token-hash-tx-rollback-old", Scope: `[]`, Type: TokenTypeAgent}
	if err := repo.Create(ctx, rollbackOld); err != nil {
		t.Fatal(err)
	}
	rollbackReplacement := ServiceToken{ID: "contract-service-token-tx-rollback-new", Prefix: "new", TokenHash: "contract-service-token-hash-tx-rollback-new", Scope: `[]`, Type: TokenTypeAgent}
	wantErr := errors.New("abort credential lifecycle")
	err := db.ServiceTokenTransaction(ctx, func(tokens ServiceTokenRepository, audits AuditRepository) error {
		if err := tokens.Rotate(ctx, rollbackOld.ID, rollbackReplacement, when); err != nil {
			return err
		}
		if err := audits.Create(ctx, AuditLog{ID: "audit-service-token-tx-rollback", Action: "rotate", ResourceType: "service_token", ResourceID: rollbackOld.ID}); err != nil {
			return err
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("rollback service token transaction error = %v, want %v", err, wantErr)
	}
	rollbackOldAfter, err := repo.Get(ctx, rollbackOld.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rollbackOldAfter.RevokedAt != nil {
		t.Fatalf("rollback old token revoked at = %v, want nil", rollbackOldAfter.RevokedAt)
	}
	if _, err := repo.Get(ctx, rollbackReplacement.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("rollback replacement lookup error = %v, want sql.ErrNoRows", err)
	}
	assertAuditExists(t, db.Audits(), "audit-service-token-tx-rollback", false)
}

func TestServiceTokenMutationTransactionIncludesIdempotency(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	token := ServiceToken{ID: "contract-service-token-mutation-commit", Prefix: "commit", TokenHash: "contract-service-token-hash-mutation-commit", Scope: `{}`, Type: TokenTypeClient}
	if err := db.ServiceTokenMutationTransaction(ctx, func(tokens ServiceTokenRepository, audits AuditRepository, idempotency IdempotencyRepository) error {
		if err := tokens.Create(ctx, token); err != nil {
			return err
		}
		if err := audits.Create(ctx, AuditLog{ID: "audit-service-token-mutation-commit", Action: "token.created", ResourceType: "service_token", ResourceID: token.ID}); err != nil {
			return err
		}
		return idempotency.Put(ctx, IdempotencyRecord{Key: "service-token-mutation-commit", Response: `{"id":"contract-service-token-mutation-commit"}`, StatusCode: 201})
	}); err != nil {
		t.Fatalf("commit mutation transaction: %v", err)
	}
	if _, err := db.ServiceTokens().Get(ctx, token.ID); err != nil {
		t.Fatalf("get committed token: %v", err)
	}
	assertAuditExists(t, db.Audits(), "audit-service-token-mutation-commit", true)
	if _, err := db.Idempotency().Get(ctx, "service-token-mutation-commit"); err != nil {
		t.Fatalf("get committed idempotency record: %v", err)
	}

	rollbackToken := ServiceToken{ID: "contract-service-token-mutation-rollback", Prefix: "rollback", TokenHash: "contract-service-token-hash-mutation-rollback", Scope: `{}`, Type: TokenTypeClient}
	wantErr := errors.New("abort complete mutation")
	err := db.ServiceTokenMutationTransaction(ctx, func(tokens ServiceTokenRepository, audits AuditRepository, idempotency IdempotencyRepository) error {
		if err := tokens.Create(ctx, rollbackToken); err != nil {
			return err
		}
		if err := audits.Create(ctx, AuditLog{ID: "audit-service-token-mutation-rollback", Action: "token.created", ResourceType: "service_token", ResourceID: rollbackToken.ID}); err != nil {
			return err
		}
		if err := idempotency.Put(ctx, IdempotencyRecord{Key: "service-token-mutation-rollback", Response: `{"id":"contract-service-token-mutation-rollback"}`, StatusCode: 201}); err != nil {
			return err
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("rollback mutation transaction error = %v, want %v", err, wantErr)
	}
	if _, err := db.ServiceTokens().Get(ctx, rollbackToken.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("rollback token lookup error = %v, want sql.ErrNoRows", err)
	}
	assertAuditExists(t, db.Audits(), "audit-service-token-mutation-rollback", false)
	if _, err := db.Idempotency().Get(ctx, "service-token-mutation-rollback"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("rollback idempotency lookup error = %v, want sql.ErrNoRows", err)
	}
}

func TestServiceTokenRepositoryRevokeCASPreservesFirstTimestamp(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	token := ServiceToken{ID: "contract-service-token-revoke-cas", Prefix: "revoke-cas", TokenHash: "contract-service-token-hash-revoke-cas", Scope: `{}`, Type: TokenTypeClient}
	if err := db.ServiceTokens().Create(ctx, token); err != nil {
		t.Fatal(err)
	}
	first := time.Date(2026, 9, 6, 10, 0, 0, 0, time.UTC)
	second := first.Add(time.Hour)
	if err := db.ServiceTokens().Revoke(ctx, token.ID, first); err != nil {
		t.Fatalf("first revoke: %v", err)
	}
	if err := db.ServiceTokens().Revoke(ctx, token.ID, second); !errors.Is(err, ErrServiceTokenRevoked) {
		t.Fatalf("second revoke error = %v, want ErrServiceTokenRevoked", err)
	}
	stored, err := db.ServiceTokens().Get(ctx, token.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RevokedAt == nil || !stored.RevokedAt.Equal(first) {
		t.Fatalf("revokedAt = %v, want first transition %v", stored.RevokedAt, first)
	}
}

func TestServiceTokenAuthorizedMutationTransactionBindsResourceRepositories(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user := User{ID: "contract-service-token-auth-user", Username: "contract-service-token-auth-user", PasswordHash: "hash"}
	agent := Agent{ID: "contract-service-token-auth-agent", Name: "contract-service-token-auth-agent", OwnerUserID: user.ID, Enabled: true}
	node := ServerNode{ID: "contract-service-token-auth-node", Address: "127.0.0.1:9443"}
	if err := db.Users().Create(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err := db.Agents().Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	if err := db.Nodes().Create(ctx, node); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("rollback authorized mutation")
	token := ServiceToken{ID: "contract-service-token-auth-rollback", OwnerUserID: user.ID, Prefix: "auth", TokenHash: "contract-service-token-hash-auth-rollback", Scope: `{}`, Type: TokenTypeClient}
	err := db.ServiceTokenAuthorizedMutationTransaction(ctx, func(repos ServiceTokenMutationRepositories) error {
		if got, err := repos.Users.Get(ctx, user.ID); err != nil || got.ID != user.ID {
			t.Fatalf("transaction user = %+v, err=%v", got, err)
		}
		if got, err := repos.Agents.Get(ctx, agent.ID); err != nil || got.OwnerUserID != user.ID || !got.Enabled {
			t.Fatalf("transaction Agent = %+v, err=%v", got, err)
		}
		if got, err := repos.Nodes.Get(ctx, node.ID); err != nil || got.ID != node.ID {
			t.Fatalf("transaction node = %+v, err=%v", got, err)
		}
		if err := repos.Tokens.Create(ctx, token); err != nil {
			return err
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("authorized transaction error = %v, want %v", err, wantErr)
	}
	if _, err := db.ServiceTokens().Get(ctx, token.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("authorized transaction rollback token error = %v, want sql.ErrNoRows", err)
	}
}

func assertServiceToken(t *testing.T, got, want ServiceToken) {
	t.Helper()
	if got.ID != want.ID || got.OwnerUserID != want.OwnerUserID || got.AgentID != want.AgentID || got.NodeID != want.NodeID || got.Prefix != want.Prefix || got.TokenHash != want.TokenHash || got.Scope != want.Scope || got.Type != want.Type {
		t.Fatalf("service token = %+v, want %+v", got, want)
	}
	if got.ExpiresAt == nil || want.ExpiresAt == nil || !got.ExpiresAt.Equal(*want.ExpiresAt) {
		t.Fatalf("expires at = %v, want %v", got.ExpiresAt, want.ExpiresAt)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) || !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Fatalf("timestamps = (%v, %v), want (%v, %v)", got.CreatedAt, got.UpdatedAt, want.CreatedAt, want.UpdatedAt)
	}
}

func assertAuditExists(t *testing.T, repo AuditRepository, id string, want bool) {
	t.Helper()
	page, err := repo.List(context.Background(), "", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, audit := range page.Items {
		if audit.ID == id {
			if !want {
				t.Fatalf("audit %s exists after rollback", id)
			}
			return
		}
	}
	if want {
		t.Fatalf("audit %s does not exist after commit", id)
	}
}
