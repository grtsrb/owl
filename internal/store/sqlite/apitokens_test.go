package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"owl/internal/store"
)

func TestAPITokensStoreBackupAndRestore(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	u, _ := db.CreateUser(ctx, "a@example.com", store.RoleAdmin)
	_ = db.SetPassword(ctx, u.ID, "$argon2id$x", "")
	soon := time.Now().Add(2 * 24 * time.Hour)
	tok, err := db.CreateAPIToken(ctx, store.APIToken{UserID: u.ID, Name: "ci", Prefix: "owl_pat_abc", Scope: store.ScopeWrite, ExpiresAt: &soon}, "h1")
	if err != nil || tok.UserEmail != "a@example.com" || tok.Scope != store.ScopeWrite {
		t.Fatalf("create: %+v %v", tok, err)
	}
	if _, err := db.CreateAPIToken(ctx, store.APIToken{UserID: u.ID, Name: "ci", Prefix: "p", Scope: store.ScopeRead}, "h2"); !errors.Is(err, store.ErrExists) {
		t.Fatalf("duplicate name: %v", err)
	}
	if exp, _ := db.ExpiringAPITokens(ctx, time.Now().Add(7*24*time.Hour)); len(exp) != 1 {
		t.Fatalf("expiring: %+v", exp)
	}
	_ = db.MarkAPITokenWarned(ctx, tok.ID)
	if exp, _ := db.ExpiringAPITokens(ctx, time.Now().Add(7*24*time.Hour)); len(exp) != 0 {
		t.Fatalf("warned twice: %+v", exp)
	}

	// Backups carry no API tokens.
	bak := filepath.Join(t.TempDir(), "b.db")
	if err := db.Backup(ctx, bak); err != nil {
		t.Fatal(err)
	}
	raw, _ := sql.Open("sqlite", "file:"+bak+"?mode=ro")
	var n int
	if err := raw.QueryRow("SELECT count(*) FROM api_tokens").Scan(&n); err != nil || n != 0 {
		t.Fatalf("tokens in backup: %d %v", n, err)
	}
	raw.Close()

	// A token created after the backup survives restoring it.
	later, _ := db.CreateAPIToken(ctx, store.APIToken{UserID: u.ID, Name: "later", Prefix: "p", Scope: store.ScopeRead}, "h3")
	cp := filepath.Join(t.TempDir(), "r.db")
	b, _ := os.ReadFile(bak)
	_ = os.WriteFile(cp, b, 0o600)
	if err := db.Restore(ctx, cp); err != nil {
		t.Fatal(err)
	}
	if got, _, err := db.APITokenByHash(ctx, "h3"); err != nil || got.ID != later.ID {
		t.Fatalf("restore dropped a live token: %v", err)
	}
	if ts, _ := db.ListAPITokens(ctx, u.ID); len(ts) != 2 {
		t.Fatalf("tokens after restore: %+v", ts)
	}

	// Deleting the user deletes their tokens.
	other, _ := db.CreateUser(ctx, "b@example.com", store.RoleAdmin)
	_ = db.SetPassword(ctx, other.ID, "$argon2id$x", "")
	if err := db.DeleteUser(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.APITokenByHash(ctx, "h3"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("token outlived its user: %v", err)
	}
}
