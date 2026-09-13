package server

import (
	"context"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func remoteServerFixture(t *testing.T) (*storage.DB, *RemoteServerService, auth.Principal, auth.Principal) {
	t.Helper()
	db, err := storage.Open(context.Background(), storage.DriverSQLite, "file:remote-server-service-"+t.Name()+"?mode=memory&cache=shared", true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	owner := auth.Principal{UserID: "user-a", Username: "alice", Role: "user"}
	other := auth.Principal{UserID: "user-b", Username: "bob", Role: "user"}
	for _, agent := range []storage.Agent{
		{ID: "agent-a", Name: "agent-a", OwnerUserID: owner.UserID, Enabled: true},
		{ID: "agent-b", Name: "agent-b", OwnerUserID: other.UserID, Enabled: true},
		{ID: "agent-disabled", Name: "agent-disabled", OwnerUserID: owner.UserID, Enabled: false},
	} {
		if err := db.Agents().Create(ctx, agent); err != nil {
			t.Fatalf("create agent %s: %v", agent.ID, err)
		}
	}
	if _, err := db.Credentials().Create(ctx, storage.Credential{
		ID: "credential-a", OwnerUserID: owner.UserID, Name: "credential-a",
		Type: storage.CredentialTypeSSHPublicKey, PublicKey: "ssh-ed25519 AAAATEST", Fingerprint: "SHA256:test", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Credentials().Create(ctx, storage.Credential{
		ID: "credential-disabled", OwnerUserID: owner.UserID, Name: "credential-disabled",
		Type: storage.CredentialTypeSSHPublicKey, PublicKey: "ssh-ed25519 AAAATEST", Fingerprint: "SHA256:test", Enabled: false,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Credentials().Create(ctx, storage.Credential{
		ID: "credential-other", OwnerUserID: other.UserID, Name: "credential-other",
		Type: storage.CredentialTypeSSHPublicKey, PublicKey: "ssh-ed25519 AAAATEST", Fingerprint: "SHA256:test", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Policies().Create(ctx, storage.AgentPolicy{
		ID: "policy-a", AgentID: "agent-a", TargetHost: "10.0.0.8", TargetPort: 22, Protocol: "tcp", AllowedCIDRs: "10.0.0.0/24", AllowedPorts: "22",
	}); err != nil {
		t.Fatal(err)
	}
	return db, NewRemoteServerService(db.RemoteServers(), db.Credentials(), db.Agents(), db.Policies(), db.Audits(), db.Leases()), owner, other
}

func TestRemoteServerServiceValidatesOwnershipAndAgentPolicy(t *testing.T) {
	db, service, owner, other := remoteServerFixture(t)
	ctx := context.Background()
	input := RemoteServerInput{
		Name: "web-1", Host: "10.0.0.8", Port: 22, DefaultUsername: "deploy",
		CredentialID: "credential-a", AgentID: "agent-a", Enabled: true,
	}
	created, err := service.Create(ctx, owner, input)
	if err != nil {
		t.Fatalf("create remote server: %v", err)
	}
	if created.OwnerUserID != owner.UserID || created.AgentID != input.AgentID || created.CredentialID != input.CredentialID {
		t.Fatalf("created remote server = %+v", created)
	}
	if _, err := service.Get(ctx, other, created.ID); err == nil {
		t.Fatal("other owner can read remote server")
	}
	page, err := service.List(ctx, other, storage.RemoteServerFilter{OwnerUserID: other.UserID}, "", 10)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("other owner page = %+v, err = %v", page, err)
	}

	cases := []RemoteServerInput{
		{Name: "wrong credential", Host: "10.0.0.8", Port: 22, DefaultUsername: "deploy", CredentialID: "credential-other", AgentID: "agent-a", Enabled: true},
		{Name: "disabled credential", Host: "10.0.0.8", Port: 22, DefaultUsername: "deploy", CredentialID: "credential-disabled", AgentID: "agent-a", Enabled: true},
		{Name: "wrong agent", Host: "10.0.0.8", Port: 22, DefaultUsername: "deploy", CredentialID: "credential-a", AgentID: "agent-b", Enabled: true},
		{Name: "disabled agent", Host: "10.0.0.8", Port: 22, DefaultUsername: "deploy", CredentialID: "credential-a", AgentID: "agent-disabled", Enabled: true},
		{Name: "policy denied", Host: "10.0.0.9", Port: 2222, DefaultUsername: "deploy", CredentialID: "credential-a", AgentID: "agent-a", Enabled: true},
		{Name: "invalid port", Host: "10.0.0.8", Port: 70000, DefaultUsername: "deploy", CredentialID: "credential-a", AgentID: "agent-a", Enabled: true},
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			if _, err := service.Create(ctx, owner, tc); err == nil {
				t.Fatal("expected invalid remote server to be rejected")
			}
		})
	}

	if err := service.Delete(ctx, owner, created.ID); err != nil {
		t.Fatalf("delete remote server: %v", err)
	}
	found, err := service.Get(ctx, owner, created.ID)
	if err != nil || found.DeletedAt == nil {
		t.Fatalf("deleted record must be preserved, found=%+v err=%v", found, err)
	}
	if _, err := service.Restore(ctx, owner, created.ID); err != nil {
	}
	audits, err := db.Audits().List(ctx, storage.AuditFilter{ResourceID: created.ID}, "", 100)
	if err != nil || len(audits.Items) == 0 {
		t.Fatalf("remote server audits = %+v, err = %v", audits.Items, err)
	}
}
