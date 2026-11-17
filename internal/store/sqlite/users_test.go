package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"owl/internal/store"
)

func openTest(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestUsersSessionsAndTokens(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	a, err := db.CreateUser(ctx, " Admin@Example.com ", store.RoleAdmin)
	if err != nil || a.Email != "admin@example.com" || a.Active() {
		t.Fatalf("create: %+v %v", a, err)
	}
	if _, err := db.CreateUser(ctx, "ADMIN@example.com", store.RoleViewer); !errors.Is(err, store.ErrExists) {
		t.Errorf("duplicate email (case-insensitive): %v", err)
	}

	// Invite link: peek doesn't use it up, consume does (once).
	if err := db.CreateAuthToken(ctx, "inv1", a.ID, store.TokenInvite, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateAuthToken(ctx, "inv2", a.ID, store.TokenInvite, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.PeekAuthToken(ctx, "inv1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a new invite replaces the old one: %v", err)
	}
	if u, kind, err := db.PeekAuthToken(ctx, "inv2"); err != nil || u.ID != a.ID || kind != store.TokenInvite {
		t.Errorf("peek: %+v %s %v", u, kind, err)
	}
	if _, _, err := db.ConsumeAuthToken(ctx, "inv2"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.ConsumeAuthToken(ctx, "inv2"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a link works once: %v", err)
	}
	if err := db.CreateAuthToken(ctx, "old", a.ID, store.TokenReset, time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.PeekAuthToken(ctx, "old"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expired link: %v", err)
	}
	// Reset links accumulate: a "Forgot password?" request can't cancel the one an admin sent.
	_ = db.CreateAuthToken(ctx, "r1", a.ID, store.TokenReset, time.Now().Add(time.Hour))
	_ = db.CreateAuthToken(ctx, "r2", a.ID, store.TokenReset, time.Now().Add(time.Hour))
	if _, _, err := db.PeekAuthToken(ctx, "r1"); err != nil {
		t.Errorf("an earlier reset link must survive a new one: %v", err)
	}

	if err := db.SetPassword(ctx, a.ID, "hash1", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.PeekAuthToken(ctx, "r1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("setting the password voids reset links: %v", err)
	}
	if a, _ = db.GetUser(ctx, a.ID); !a.Active() || !a.HasPassword || a.PasswordHash != "hash1" {
		t.Fatalf("set password activates: %+v", a)
	}

	// Sessions: lookup joins the user; a new password ends other sessions but keeps the given one.
	for _, h := range []string{"s1", "s2"} {
		if err := db.CreateSession(ctx, h, a.ID, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	s, err := db.SessionByHash(ctx, "s1")
	if err != nil || s.User.Email != "admin@example.com" || s.ExpiresAt.Before(time.Now()) || s.LastSeenAt.IsZero() {
		t.Fatalf("session: %+v %v", s, err)
	}
	if err := db.SetPassword(ctx, a.ID, "hash2", "s2"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SessionByHash(ctx, "s1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("other sessions end on a password change: %v", err)
	}
	if _, err := db.SessionByHash(ctx, "s2"); err != nil {
		t.Errorf("own session survives: %v", err)
	}

	// Last admin: can't be demoted, disabled or deleted.
	if _, err := db.UpdateUser(ctx, a.ID, store.RoleViewer, false); !errors.Is(err, store.ErrLastAdmin) {
		t.Errorf("demote last admin: %v", err)
	}
	if _, err := db.UpdateUser(ctx, a.ID, store.RoleAdmin, true); !errors.Is(err, store.ErrLastAdmin) {
		t.Errorf("disable last admin: %v", err)
	}
	if err := db.DeleteUser(ctx, a.ID); !errors.Is(err, store.ErrLastAdmin) {
		t.Errorf("delete last admin: %v", err)
	}
	// A pending (not yet accepted) admin doesn't count as the second admin.
	b, _ := db.CreateUser(ctx, "b@example.com", store.RoleAdmin)
	if _, err := db.UpdateUser(ctx, a.ID, store.RoleViewer, false); !errors.Is(err, store.ErrLastAdmin) {
		t.Errorf("pending admin counted: %v", err)
	}
	if err := db.LinkGoogle(ctx, b.ID, "google-sub-b"); err != nil {
		t.Fatal(err)
	}
	if u, err := db.UserByGoogleSub(ctx, "google-sub-b"); err != nil || !u.Active() || !u.HasGoogle {
		t.Fatalf("google link activates: %+v %v", u, err)
	}
	if err := db.LinkGoogle(ctx, a.ID, "google-sub-b"); !errors.Is(err, store.ErrExists) {
		t.Errorf("one Google account, one user: %v", err)
	}
	if err := db.LinkGoogle(ctx, b.ID, "other"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a linked user can't be re-linked: %v", err)
	}
	// Now a can be disabled: its session ends, and its links are void.
	_ = db.CreateAuthToken(ctx, "r", a.ID, store.TokenReset, time.Now().Add(time.Hour))
	if u, err := db.UpdateUser(ctx, a.ID, store.RoleAdmin, true); err != nil || u.DisabledAt == nil {
		t.Fatalf("disable: %+v %v", u, err)
	}
	if _, err := db.SessionByHash(ctx, "s2"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("disabling ends sessions: %v", err)
	}
	if _, _, err := db.PeekAuthToken(ctx, "r"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("disabling voids links: %v", err)
	}
	if u, err := db.UpdateUser(ctx, a.ID, store.RoleAdmin, false); err != nil || u.DisabledAt != nil {
		t.Errorf("enable: %+v %v", u, err)
	}

	// Prune: idle and expired sessions go.
	_ = db.CreateSession(ctx, "exp", b.ID, time.Now().Add(-time.Second))
	_ = db.CreateSession(ctx, "live", b.ID, time.Now().Add(time.Hour))
	if err := db.PruneSessions(ctx, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SessionByHash(ctx, "exp"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expired session pruned: %v", err)
	}
	if _, err := db.SessionByHash(ctx, "live"); err != nil {
		t.Errorf("live session kept: %v", err)
	}
	if err := db.DeleteUser(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SessionByHash(ctx, "live"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("sessions go with the user: %v", err)
	}
}

// A restore never changes who can sign in, and backups carry no sessions or links.
func TestRestoreKeepsUsers(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	old, _ := db.CreateUser(ctx, "old@example.com", store.RoleAdmin)
	_ = db.SetPassword(ctx, old.ID, "h", "")
	_ = db.CreateSession(ctx, "sess", old.ID, time.Now().Add(time.Hour))
	_ = db.SetSetting(ctx, "authPasswordLogin", "on")
	bak := filepath.Join(t.TempDir(), "b.db")
	if err := db.Backup(ctx, bak); err != nil {
		t.Fatal(err)
	}
	b, _ := Open(bak)
	if _, err := b.SessionByHash(ctx, "sess"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("backup contains a session: %v", err)
	}
	if u, err := b.UserByEmail(ctx, "old@example.com"); err != nil || u.PasswordHash != "" || u.Role != store.RoleAdmin {
		t.Errorf("backup keeps users without password hashes: %+v %v", u, err)
	}
	b.Close()

	_ = db.DeleteSession(ctx, "sess")
	_, _ = db.CreateUser(ctx, "new@example.com", store.RoleViewer)
	_ = db.SetSetting(ctx, "authPasswordLogin", "off")
	if err := db.Restore(ctx, bak); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UserByEmail(ctx, "new@example.com"); err != nil {
		t.Errorf("restore removed a user: %v", err)
	}
	if m, _ := db.GetSettings(ctx); m["authPasswordLogin"] != "off" {
		t.Errorf("restore changed the password sign-in setting: %q", m["authPasswordLogin"])
	}
}
