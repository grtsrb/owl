package server

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"owl/internal/store"
	"owl/internal/upstream"
)

func TestCustomComponentEndpoints(t *testing.T) {
	e := newSigninEnv(t)
	if err := e.app.ReloadCatalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	admin := asAdmin(t, e.srv.URL)
	viewer := browser(t)
	u, _ := url.Parse(e.srv.URL)
	viewer.Jar.SetCookies(u, []*http.Cookie{{Name: "owl_session", Value: testViewerToken}})
	base := e.srv.URL + "/api/v1/catalog/custom"
	body := `{"component":{"id":"sftp","name":"SFTP","images":["atmoz/sftp"],"github":"atmoz/sftp"}}`

	if code, _ := doJSON(t, viewer, "POST", base, body); code != http.StatusForbidden {
		t.Fatalf("viewer create: %d", code)
	}
	if code, out := doJSON(t, admin, "POST", base, body); code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, out)
	}
	if c := e.app.cat().ByID("sftp"); c == nil || !c.Custom {
		t.Fatal("not in the live catalog")
	}
	if id := e.app.cat().Identify("atmoz/sftp:1.2.3"); id.Component == nil || id.Component.ID != "sftp" {
		t.Fatalf("not identified: %+v", id)
	}
	for name, b := range map[string]string{
		"same id":        body,
		"built-in id":    `{"component":{"id":"redis","name":"R","images":["a/b"],"github":"a/b"}}`,
		"built-in image": `{"component":{"id":"myredis","name":"R","images":["redis"],"github":"a/b"}}`,
		"custom's image": `{"component":{"id":"sftp2","name":"S","images":["docker.io/atmoz/sftp"],"github":"a/b"}}`,
	} {
		if code, out := doJSON(t, admin, "POST", base, b); code != http.StatusConflict {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	if code, _ := doJSON(t, admin, "POST", base, `{"component":{"id":"x","name":"x","images":["a/b"],"github":"a/b","osv":{"ecosystem":"Go","name":"x"}}}`); code != http.StatusBadRequest {
		t.Errorf("osv accepted: %d", code)
	}
	// Edit (id from the path), map an override to it, then delete: the override goes too.
	if code, out := doJSON(t, admin, "PUT", base+"/sftp", `{"component":{"name":"SFTP server","images":["atmoz/sftp"],"github":"atmoz/sftp","versionParts":2}}`); code != 200 {
		t.Fatalf("update: %d %v", code, out)
	}
	if c := e.app.cat().ByID("sftp"); c.Name != "SFTP server" || c.VersionParts != 2 {
		t.Fatalf("update not live: %+v", c)
	}
	if code, out := doJSON(t, admin, "PUT", e.srv.URL+"/api/v1/catalog/overrides", `{"repository":"harbor.example.com/x/sftp","kind":"map","component":"sftp"}`); code != 200 {
		t.Fatalf("map to custom: %d %v", code, out)
	}
	code, imp := doJSON(t, admin, "GET", base+"/sftp/impact", "")
	if code != 200 || len(imp["overrides"].([]any)) != 1 {
		t.Fatalf("impact: %d %v", code, imp)
	}
	if code, _ := doJSON(t, viewer, "DELETE", base+"/sftp", ""); code != http.StatusForbidden {
		t.Fatalf("viewer delete: %d", code)
	}
	if code, out := doJSON(t, admin, "DELETE", base+"/sftp", ""); code != 200 {
		t.Fatalf("delete: %d %v", code, out)
	}
	if e.app.cat().ByID("sftp") != nil {
		t.Fatal("still in the catalog")
	}
	if ovs, _ := e.db.ListOverrides(context.Background()); len(ovs) != 0 {
		t.Fatalf("override kept: %+v", ovs)
	}
	es, _ := e.db.ListAudit(context.Background(), 20)
	var acts []string
	for _, a := range es {
		acts = append(acts, a.Action)
	}
	if j := strings.Join(acts, ","); !strings.Contains(j, "component added") || !strings.Contains(j, "component deleted") {
		t.Errorf("audit: %s", j)
	}

	// Preview without upstream: tags of running images and a warning, nothing saved.
	if code, out := doJSON(t, admin, "POST", e.srv.URL+"/api/v1/catalog/preview", body); code != 200 || out["warnings"] == nil {
		t.Fatalf("preview: %d %v", code, out)
	}
	if cs, _ := e.db.ListCustomComponents(context.Background()); len(cs) != 0 {
		t.Fatal("preview saved")
	}
}

func TestMatchProducts(t *testing.T) {
	ps := []upstream.EOLProduct{{Slug: "postgresql", Label: "PostgreSQL", Aliases: []string{"postgres"}}, {Slug: "redis"}, {Slug: "valkey"}}
	got := matchProducts(ps, "postgres")
	if len(got) != 1 || got[0].Slug != "postgresql" {
		t.Errorf("%+v", got)
	}
	if got := matchProducts(ps, "sftp"); len(got) != 0 {
		t.Errorf("sftp: %+v", got)
	}
}

// A custom component whose id a built-in took over: deleting the stale entry must not delete the
// built-in's policy and overrides. Stored rows are validated like API input.
func TestCustomReviewFixes(t *testing.T) {
	e := newSigninEnv(t)
	ctx := context.Background()
	_ = e.db.CreateCustomComponent(ctx, "redis", []byte(`{"name":"Old Redis","images":["example.org/x/redis"],"github":"a/b"}`))
	_ = e.db.CreateCustomComponent(ctx, "sneaky", []byte(`{"name":"S","images":["example.org/x/s"],"github":"a/b","source":"cluster"}`))
	_ = e.db.SetPolicy(ctx, "redis", "n-1")
	_ = e.db.SetOverride(ctx, store.CatalogOverride{Repository: "harbor.example.com/x/redis", Kind: store.OverrideMap, Component: "redis"})
	if err := e.app.ReloadCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	for _, v := range e.app.customViews() {
		if v.State.Active {
			t.Errorf("%s active: %+v", v.Component.ID, v.State)
		}
	}
	admin := asAdmin(t, e.srv.URL)
	if code, imp := doJSON(t, admin, "GET", e.srv.URL+"/api/v1/catalog/custom/redis/impact", ""); code != 200 || imp["policy"] != false {
		t.Fatalf("impact: %d %v", code, imp)
	}
	if code, out := doJSON(t, admin, "DELETE", e.srv.URL+"/api/v1/catalog/custom/redis", ""); code != 200 {
		t.Fatalf("delete: %d %v", code, out)
	}
	pols, _ := e.db.ListPolicies(ctx)
	ovs, _ := e.db.ListOverrides(ctx)
	if pols["redis"] != "n-1" || len(ovs) != 1 {
		t.Fatalf("the built-in's policy/overrides were deleted: %v %+v", pols, ovs)
	}
	// Changing the repository drops summaries written from the old project's notes.
	if code, _ := doJSON(t, admin, "POST", e.srv.URL+"/api/v1/catalog/custom", `{"component":{"id":"tool","name":"T","images":["example.org/x/tool"],"github":"wrong/tool"}}`); code != 201 {
		t.Fatal("create")
	}
	_ = e.db.PutSummary(ctx, store.Summary{Component: "tool", FromVersion: "1.0.0", ToVersion: "1.1.0", Model: "m", Content: []byte(`{}`)})
	if code, _ := doJSON(t, admin, "PUT", e.srv.URL+"/api/v1/catalog/custom/tool", `{"component":{"name":"T","images":["example.org/x/tool"],"github":"right/tool"}}`); code != 200 {
		t.Fatal("update")
	}
	if ss, _ := e.db.ListSummaries(ctx); len(ss) != 0 {
		t.Fatalf("stale summaries kept: %+v", ss)
	}
}
