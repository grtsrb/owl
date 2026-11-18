package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"owl/internal/store"
)

func TestUsersFileFlow(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	read := map[string]string{"OWL_SERVER": e.url, "OWL_TOKEN": e.token("admin@example.com", store.ScopeRead)}
	write := map[string]string{"OWL_SERVER": e.url, "OWL_TOKEN": e.token("admin@example.com", store.ScopeWrite)}

	// Export: the current users, in the file format.
	code, exported, errs := e.run("", read, "users", "export")
	if code != 0 || !strings.Contains(exported, "email: admin@example.com") || !strings.Contains(exported, "role: viewer") {
		t.Fatalf("export: %d %s %s", code, exported, errs)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "users.yaml")
	file := exported + "- email: new@example.com\n  role: viewer\n"
	_ = os.WriteFile(path, []byte(file), 0o600)

	// Plan (read token is enough): two to manage, one to invite; --detailed-exitcode = 2.
	code, out, errs := e.run("", read, "users", "plan", "-f", path, "--detailed-exitcode")
	if code != 2 || !strings.Contains(out, "+ new@example.com") || !strings.Contains(out, "start managing") {
		t.Fatalf("plan: %d %s %s", code, out, errs)
	}
	// Apply needs confirmation (no terminal here: --yes) and a write token.
	if code, _, errs := e.run("", write, "users", "apply", "-f", path); code != 1 || !strings.Contains(errs, "--yes") {
		t.Fatalf("apply without --yes: %d %s", code, errs)
	}
	if code, _, errs := e.run("", read, "users", "apply", "-f", path, "--yes"); code != 1 || !strings.Contains(errs, "read-only") {
		t.Fatalf("apply with read token: %d %s", code, errs)
	}
	code, out, errs = e.run("", write, "users", "apply", "-f", path, "--yes")
	if code != 0 || !strings.Contains(out, "Applied 3 change(s)") || !strings.Contains(out, "Send invite again") || strings.Contains(out, "/invite#") {
		t.Fatalf("apply: %d %s %s", code, out, errs)
	}
	if code, out, _ := e.run("", read, "users", "plan", "-f", path, "--detailed-exitcode"); code != 0 || !strings.Contains(out, "No changes") {
		t.Fatalf("plan after apply: %d %s", code, out)
	}
	if _, out, _ := e.run("", read, "users", "list"); !strings.Contains(out, "file") {
		t.Fatalf("list: %s", out)
	}

	// One-off change of a managed user: works, warns about drift, and plan shows it.
	if code, _, errs := e.run("", write, "users", "set-role", "viewer@example.com", "--role", "admin"); code != 0 || !strings.Contains(errs, "drift") {
		t.Fatalf("set-role: %d %s", code, errs)
	}
	if code, out, _ := e.run("", read, "users", "plan", "-f", path, "--detailed-exitcode"); code != 2 || !strings.Contains(out, "role admin → viewer") {
		t.Fatalf("drift: %d %s", code, out)
	}
	if code, _, _ := e.run("", write, "users", "disable", "viewer@example.com"); code != 0 {
		t.Fatal("disable")
	}
	if u, _ := e.db.UserByEmail(ctx, "viewer@example.com"); u.DisabledAt == nil {
		t.Fatal("not disabled")
	}
	if code, out, _ := e.run("", write, "users", "add", "other@example.com", "--role", "viewer"); code != 0 || !strings.Contains(out, "Invited other@example.com") {
		t.Fatalf("add: %d %s", code, out)
	}

	// Bad files: every problem at once, nothing sent.
	_ = os.WriteFile(path, []byte("users:\n- email: a@example.com\n  role: root\n- email: a@example.com\n  role: viewer\n- email: b@example.com\n  role: viewer\n  name: B\n"), 0o600)
	if code, _, errs := e.run("", read, "users", "plan", "-f", path); code != 1 || !strings.Contains(errs, "unknown field") {
		t.Fatalf("unknown field: %d %s", code, errs)
	}
	_ = os.WriteFile(path, []byte("users:\n- email: a@example.com\n  role: root\n- email: A@example.com\n  role: viewer\n"), 0o600)
	if code, _, errs := e.run("", read, "users", "plan", "-f", path); code != 1 || !strings.Contains(errs, "role must be") || !strings.Contains(errs, "already entry 1") {
		t.Fatalf("bad file: %d %s", code, errs)
	}
}

func TestSourceLabelAndDashDash(t *testing.T) {
	for in, want := range map[string]string{"users.yaml": "users.yaml", "team users.yaml": "team_users.yaml", "_x.yaml": "x.yaml", "": "users file"} {
		if got := sourceLabel(in); got != want {
			t.Errorf("sourceLabel(%q) = %q, want %q", in, got, want)
		}
	}
	a := &App{Stderr: io.Discard}
	f := a.newFlags("t", "t", true)
	pos, err := f.parse([]string{"x", "-o", "json", "--", "-a", "-b"})
	if err != nil || len(pos) != 3 || pos[1] != "-a" || !f.json() {
		t.Fatalf("parse: %v %v", pos, err)
	}
}
