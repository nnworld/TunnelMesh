package auth

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func TestEnsureAdminConcurrentCreatesOneAndPrintsOnce(t *testing.T) {
	db := testDB(t)
	services := []*BootstrapService{NewBootstrapService(db), NewBootstrapService(db)}
	const n = 8
	results := make(chan Credentials, n)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, err := services[i%len(services)].EnsureAdmin(context.Background())
			results <- c
			errs <- err
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var printed []Credentials
	for c := range results {
		if c.Username != "" {
			printed = append(printed, c)
		}
	}
	if len(printed) != 1 {
		t.Fatalf("printed credentials = %d", len(printed))
	}
	page, err := db.Users().List(context.Background(), "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Role != "admin" {
		t.Fatalf("users = %+v", page.Items)
	}
	if page.Items[0].PasswordHash == printed[0].Password {
		t.Fatal("clear password stored")
	}
}

func TestRegenerateRevokesAllAdminSessionsBeyondFirstPage(t *testing.T) {
	db := testDB(t)
	svc := NewBootstrapService(db)
	old, err := svc.EnsureAdmin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	admin, err := db.Users().GetByUsername(context.Background(), old.Username)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 501; i++ {
		if err := db.Tokens().Create(context.Background(), storage.APIToken{UserID: admin.ID, TokenHash: fmt.Sprintf("token-hash-%03d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.RegenerateCredentials(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	cursor := ""
	seen := 0
	for {
		page, err := db.Tokens().List(context.Background(), cursor, 500)
		if err != nil {
			t.Fatal(err)
		}
		for _, tok := range page.Items {
			seen++
			if tok.RevokedAt == nil {
				t.Fatalf("token %s was not revoked", tok.ID)
			}
		}
		if !page.HasMore {
			break
		}
		cursor = page.NextCursor
	}
	if seen != 501 {
		t.Fatalf("tokens seen = %d", seen)
	}
}

func TestRegenerateCredentialsRequiresConfirmAndRevokesSessions(t *testing.T) {
	db := testDB(t)
	svc := NewBootstrapService(db)
	old, err := svc.EnsureAdmin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	auth := NewAuthService(db)
	login, err := auth.Login(context.Background(), old.Username, old.Password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RegenerateCredentials(context.Background(), false); err == nil {
		t.Fatal("regeneration without confirm succeeded")
	}
	next, err := svc.RegenerateCredentials(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if next.Password == old.Password || next.Username != old.Username {
		t.Fatalf("credentials = %+v", next)
	}
	if _, err := auth.ValidateToken(context.Background(), login.Token); err == nil {
		t.Fatal("old token remains valid")
	}
	logs, err := db.Audits().List(context.Background(), "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs.Items) != 1 {
		t.Fatalf("audit logs = %+v", logs.Items)
	}
}
