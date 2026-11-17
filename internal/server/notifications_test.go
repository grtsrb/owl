package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"owl/internal/notify"
	"owl/internal/report"
)

func TestNotificationEndpoints(t *testing.T) {
	e := newSigninEnv(t)
	var mu sync.Mutex
	var posts []string
	mm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		posts = append(posts, string(b))
		mu.Unlock()
	}))
	defer mm.Close()
	hook := mm.URL + "/hooks/supersecretkey"
	e.app.Notify = &notify.Notifier{Store: e.db, Catalog: e.app.Catalog, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Report:  func(context.Context) (*report.Report, error) { return &report.Report{}, nil },
		Webhook: &notify.Webhook{URL: hook, Username: "Owl"}, Mail: e.mail, PublicURL: e.app.Auth.PublicURL}
	admin := asAdmin(t, e.srv.URL)
	viewer := browser(t)
	u, _ := url.Parse(e.srv.URL)
	viewer.Jar.SetCookies(u, []*http.Cookie{{Name: "owl_session", Value: testViewerToken}})

	// Admin only; the webhook URL never appears.
	if code, _ := doJSON(t, viewer, "GET", e.srv.URL+"/api/v1/notifications", ""); code != http.StatusForbidden {
		t.Fatalf("viewer GET: %d", code)
	}
	code, out := doJSON(t, admin, "GET", e.srv.URL+"/api/v1/notifications", "")
	if code != 200 || strings.Contains(toJSON(out), "supersecretkey") {
		t.Fatalf("GET: %d %v", code, out)
	}
	st := out["status"].(map[string]any)
	set := out["settings"].(map[string]any)
	if st["webhookConfigured"] != true || set["digest"] != false || set["digestDay"] != "monday" || set["digestTimeZone"] != "Europe/Belgrade" {
		t.Fatalf("defaults: %v", out)
	}

	for _, bad := range []string{`{"digestDay":"funday","digestTime":"09:00","digestTimeZone":"UTC"}`,
		`{"digestDay":"monday","digestTime":"25:00","digestTimeZone":"UTC"}`,
		`{"digestDay":"monday","digestTime":"09:00","digestTimeZone":"Nowhere/Land"}`} {
		if code, _ := doJSON(t, admin, "PUT", e.srv.URL+"/api/v1/notifications", bad); code != http.StatusBadRequest {
			t.Errorf("accepted %s: %d", bad, code)
		}
	}
	code, out = doJSON(t, admin, "PUT", e.srv.URL+"/api/v1/notifications",
		`{"digest":true,"critical":true,"digestDay":"Friday","digestTime":"7:30","digestTimeZone":"UTC"}`)
	if code != 200 || out["settings"].(map[string]any)["digestDay"] != "friday" || out["status"].(map[string]any)["nextDigest"] == nil {
		t.Fatalf("PUT: %d %v", code, out)
	}
	if code, _ := doJSON(t, viewer, "PUT", e.srv.URL+"/api/v1/notifications", `{"digestDay":"monday","digestTime":"09:00","digestTimeZone":"UTC"}`); code != http.StatusForbidden {
		t.Fatalf("viewer PUT: %d", code)
	}

	if code, out := doJSON(t, admin, "POST", e.srv.URL+"/api/v1/notifications/test", `{}`); code != 200 {
		t.Fatalf("test: %d %v", code, out)
	}
	if code, out := doJSON(t, admin, "POST", e.srv.URL+"/api/v1/notifications/digest", `{}`); code != 200 {
		t.Fatalf("digest: %d %v", code, out)
	}
	mu.Lock()
	if len(posts) != 2 || !strings.Contains(posts[0], "Owl test message") || !strings.Contains(posts[1], "weekly digest") {
		t.Errorf("posts: %q", posts)
	}
	mu.Unlock()

	// Audit log names the actions, never the URL.
	es, _ := e.db.ListAudit(context.Background(), 10)
	var actions []string
	for _, a := range es {
		actions = append(actions, a.Action)
		if strings.Contains(a.Details, "supersecretkey") {
			t.Errorf("audit leaks the URL: %+v", a)
		}
	}
	if !strings.Contains(strings.Join(actions, ","), "notification settings changed") || !strings.Contains(strings.Join(actions, ","), "digest sent manually") {
		t.Errorf("audit: %v", actions)
	}
	if strings.Contains(e.logs.String(), "supersecretkey") {
		t.Error("log leaks the webhook URL")
	}

	// Digest email: any signed-in user, their own flag; needs SMTP.
	code, out = doJSON(t, viewer, "PUT", e.srv.URL+"/api/v1/auth/digest-email", `{"on":true}`)
	if code != 200 || out["digestEmail"] != true {
		t.Fatalf("opt in: %d %v", code, out)
	}
	if _, me := doJSON(t, viewer, "GET", e.srv.URL+"/api/v1/auth/me", ""); me["digestEmail"] != true {
		t.Fatalf("me: %v", me)
	}
	e.app.Auth.Mail = nil
	if code, _ := doJSON(t, viewer, "PUT", e.srv.URL+"/api/v1/auth/digest-email", `{"on":true}`); code != http.StatusConflict {
		t.Fatalf("opt in without SMTP: %d", code)
	}
	if code, _ := doJSON(t, viewer, "PUT", e.srv.URL+"/api/v1/auth/digest-email", `{"on":false}`); code != 200 {
		t.Fatalf("opt out without SMTP: %d", code)
	}
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
