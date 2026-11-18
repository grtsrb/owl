package server

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"owl/internal/store"
)

func TestPlanUsers(t *testing.T) {
	now := time.Now()
	cur := []store.User{
		{ID: 1, Email: "admin@example.com", Role: store.RoleAdmin, ActivatedAt: &now},
		{ID: 2, Email: "dev@example.com", Role: store.RoleViewer, ActivatedAt: &now, Managed: true},
		{ID: 3, Email: "old@example.com", Role: store.RoleViewer, ActivatedAt: &now, DisabledAt: &now, Managed: true},
		{ID: 4, Email: "extra@example.com", Role: store.RoleViewer, ActivatedAt: &now},
		{ID: 5, Email: "gone@example.com", Role: store.RoleViewer, ActivatedAt: &now, Managed: true},
	}
	desired := []fileUser{
		{"admin@example.com", store.RoleAdmin}, // matches, not managed yet -> manage
		{"dev@example.com", store.RoleAdmin},   // role change
		{"old@example.com", store.RoleViewer},  // disabled -> enable
		{"new@example.com", store.RoleViewer},  // invite
	}
	acts := func(p UsersPlan) map[string]string {
		m := map[string]string{}
		for _, c := range p.Changes {
			m[c.Email] = strings.Join(c.Actions, ",")
		}
		return m
	}
	p := planUsers(cur, desired, false)
	want := map[string]string{"admin@example.com": "manage", "dev@example.com": "role", "old@example.com": "enable",
		"new@example.com": "invite", "gone@example.com": "unmanage"}
	if got := acts(p); !mapsEqual(got, want) {
		t.Errorf("plan: %v, want %v", got, want)
	}
	if !slices.Equal(p.NotInFile, []string{"extra@example.com", "gone@example.com"}) {
		t.Errorf("not in file: %v", p.NotInFile)
	}
	pp := planUsers(cur, desired, true)
	if got := acts(pp); got["extra@example.com"] != "disable" || got["gone@example.com"] != "disable" || len(pp.NotInFile) != 0 {
		t.Errorf("prune: %v %v", got, pp.NotInFile)
	}
	if p.ID == pp.ID || p.ID != planUsers(cur, desired, false).ID {
		t.Error("plan ids must differ between plans and be stable")
	}
	// Applied state: nothing to do.
	if p := planUsers([]store.User{{ID: 1, Email: "a@example.com", Role: store.RoleAdmin, Managed: true, ActivatedAt: &now}}, []fileUser{{"a@example.com", store.RoleAdmin}}, true); len(p.Changes) != 0 {
		t.Errorf("no-op plan: %+v", p)
	}
	// Invited, never signed in: fine while the invite works; a new invite once it expired.
	pending := []store.User{{ID: 1, Email: "a@example.com", Role: store.RoleViewer, Managed: true, InvitePending: true}}
	if p := planUsers(pending, []fileUser{{"a@example.com", store.RoleViewer}}, false); len(p.Changes) != 0 {
		t.Errorf("pending invite: %+v", p)
	}
	pending[0].InvitePending = false
	if got := acts(planUsers(pending, []fileUser{{"a@example.com", store.RoleViewer}}, false)); got["a@example.com"] != "reinvite" {
		t.Errorf("expired invite: %v", got)
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestUsersPlanApplyAPI(t *testing.T) {
	e := newSigninEnv(t)
	base := e.srv.URL
	ctx := context.Background()
	cli := &http.Client{}
	read, _ := newToken(t, asAdmin(t, base), base, `{"name":"ci-read"}`)
	write, _ := newToken(t, asAdmin(t, base), base, `{"name":"ci-write","scope":"write"}`)

	file := `{"source":"users.yaml","users":[{"email":"admin@example.com","role":"admin"},{"email":"New@Example.com","role":"viewer"}]}`
	// A read-only token may plan, not apply.
	code, plan := doJSON(t, cli, "POST", base+"/api/v1/users/plan", file, bearer(read)...)
	if code != 200 || len(plan["changes"].([]any)) != 2 || plan["notInFile"].([]any)[0] != "viewer@example.com" {
		t.Fatalf("plan: %d %v", code, plan)
	}
	if code, _ := doJSON(t, cli, "POST", base+"/api/v1/users/apply", file, bearer(read)...); code != 403 {
		t.Fatalf("read token applied: %d", code)
	}
	// Bad files are refused with every problem listed.
	bad := `{"users":[{"email":"x","role":"admin"},{"email":"a@example.com","role":"root"},{"email":"a@example.com","role":"viewer"}]}`
	if code, out := doJSON(t, cli, "POST", base+"/api/v1/users/plan", bad, bearer(read)...); code != 400 ||
		!strings.Contains(out["error"].(string), "plain email") || !strings.Contains(out["error"].(string), "listed twice") || !strings.Contains(out["error"].(string), "role must be") {
		t.Fatalf("bad file: %d %v", code, out)
	}
	// A stale plan id is refused.
	if code, _ := doJSON(t, cli, "POST", base+"/api/v1/users/apply", strings.Replace(file, `{"source"`, `{"expect":"0000","source"`, 1), bearer(write)...); code != 409 {
		t.Fatalf("stale plan: %d", code)
	}
	// Apply with the plan's id: invite + manage, audited "via users.yaml", invite link emailed.
	withID := strings.Replace(file, `{"source"`, `{"expect":"`+plan["id"].(string)+`","source"`, 1)
	code, out := doJSON(t, cli, "POST", base+"/api/v1/users/apply", withID, bearer(write)...)
	if code != 200 || len(out["links"].([]any)) != 1 {
		t.Fatalf("apply: %d %v", code, out)
	}
	nu, err := e.db.UserByEmail(ctx, "new@example.com")
	if err != nil || !nu.Managed || nu.Role != store.RoleViewer {
		t.Fatalf("invited user: %+v %v", nu, err)
	}
	if ad, _ := e.db.UserByEmail(ctx, "admin@example.com"); !ad.Managed {
		t.Fatal("admin not marked managed")
	}
	audit, _ := e.db.ListAudit(ctx, 20)
	found := false
	for _, a := range audit {
		if a.Action == "user invited" && strings.Contains(a.Details, "via=users.yaml") && a.Actor == "admin@example.com (token: ci-write)" {
			found = true
		}
	}
	if !found {
		t.Fatalf("audit: %+v", audit)
	}
	if msgs := e.mail.all(); len(msgs) != 1 || msgs[0].To != "new@example.com" {
		t.Fatalf("invite mail: %+v", msgs)
	}
	// Applying again: nothing to do.
	if _, again := doJSON(t, cli, "POST", base+"/api/v1/users/plan", file, bearer(read)...); len(again["changes"].([]any)) != 0 {
		t.Fatalf("second plan: %v", again)
	}

	// Prune disables the viewer who isn't in the file; never the person applying.
	if code, _ := doJSON(t, cli, "POST", base+"/api/v1/users/apply", `{"prune":true,"users":[{"email":"admin@example.com","role":"admin"},{"email":"new@example.com","role":"viewer"}]}`, bearer(write)...); code != 200 {
		t.Fatalf("prune: %d", code)
	}
	if v, _ := e.db.UserByEmail(ctx, "viewer@example.com"); v.DisabledAt == nil {
		t.Fatal("prune didn't disable")
	}
	self := `{"prune":true,"users":[{"email":"admin@example.com","role":"viewer"}]}`
	if code, out := doJSON(t, cli, "POST", base+"/api/v1/users/apply", self, bearer(write)...); code != 409 || !strings.Contains(out["error"].(string), "make you") {
		t.Fatalf("self demote: %d %v", code, out)
	}
	if code, out := doJSON(t, cli, "POST", base+"/api/v1/users/apply", `{"prune":true,"users":[]}`, bearer(write)...); code != 409 || !strings.Contains(out["error"].(string), "disable you") {
		t.Fatalf("self prune: %d %v", code, out)
	}
}

func TestApplyUserChangesGuards(t *testing.T) {
	e := newSigninEnv(t)
	ctx := context.Background()
	a, _ := e.db.UserByEmail(ctx, "admin@example.com")
	// The plan was made when admin was a viewer: refused, nothing changed.
	if _, err := e.db.ApplyUserChanges(ctx, []store.UserChange{
		{Invite: true, Email: "x@example.com", Role: store.RoleViewer},
		{UserID: a.ID, Email: a.Email, Role: store.RoleAdmin, Managed: true, WasRole: store.RoleViewer},
	}); err == nil {
		t.Fatal("stale change applied")
	}
	if _, err := e.db.UserByEmail(ctx, "x@example.com"); err == nil {
		t.Fatal("partial apply: invite kept")
	}
	// Disabling the only active admin: ErrLastAdmin.
	if _, err := e.db.ApplyUserChanges(ctx, []store.UserChange{
		{UserID: a.ID, Email: a.Email, Role: store.RoleAdmin, Disabled: true, WasRole: store.RoleAdmin},
	}); err != store.ErrLastAdmin {
		t.Fatalf("last admin: %v", err)
	}
}

func TestUsersApplyKeepsAGoogleAdmin(t *testing.T) {
	e := newSigninEnv(t)
	ctx := context.Background()
	e.app.Auth.Google = &GoogleConfig{}
	_ = e.db.SetSetting(ctx, settingPasswordLogin, "off")
	for _, em := range []string{"g1@example.com", "g2@example.com"} {
		u, _ := e.db.CreateUser(ctx, em, store.RoleAdmin)
		if err := e.db.LinkGoogle(ctx, u.ID, "sub-"+em); err != nil {
			t.Fatal(err)
		}
	}
	write, _ := newToken(t, asAdmin(t, e.srv.URL), e.srv.URL, `{"name":"ci","scope":"write"}`)
	// Each demotion alone would be fine; together they leave no Google admin.
	file := `{"users":[{"email":"admin@example.com","role":"admin"},{"email":"viewer@example.com","role":"viewer"},
		{"email":"g1@example.com","role":"viewer"},{"email":"g2@example.com","role":"viewer"}]}`
	if code, out := doJSON(t, &http.Client{}, "POST", e.srv.URL+"/api/v1/users/apply", file, bearer(write)...); code != 409 || !strings.Contains(out["error"].(string), "Google") {
		t.Fatalf("all Google admins demoted: %d %v", code, out)
	}
	ok := strings.Replace(file, `"g2@example.com","role":"viewer"`, `"g2@example.com","role":"admin"`, 1)
	if code, out := doJSON(t, &http.Client{}, "POST", e.srv.URL+"/api/v1/users/apply", ok, bearer(write)...); code != 200 {
		t.Fatalf("one Google admin kept: %d %v", code, out)
	}
}
