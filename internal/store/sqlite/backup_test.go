package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"owl/internal/store"
)

func TestBackupRestoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := open(t)
	c, _ := db.CreateClusterWithToken(ctx, "prod", store.StageLive, "IT", "h1", "p")
	_ = db.SetPolicy(ctx, "global", "latest")
	_ = db.PutCache(ctx, store.CacheEntry{Key: "big", FetchedAt: time.Now(), Body: make([]byte, 1<<20)})
	_ = db.AddAudit(ctx, store.AuditEntry{Action: "before backup"})

	bak := filepath.Join(t.TempDir(), "b.db")
	if err := db.Backup(ctx, bak); err != nil {
		t.Fatal(err)
	}
	if err := db.Backup(ctx, bak); err == nil {
		t.Error("backup must not overwrite an existing file")
	}
	if st, _ := os.Stat(bak); st.Size() > 512<<10 {
		t.Errorf("upstream cache should be left out of backups: %d bytes", st.Size())
	}

	// Changes after the backup.
	_ = db.DeleteCluster(ctx, "prod")
	_, _ = db.CreateCluster(ctx, "new", store.StageStaging, "DS")
	_ = db.SetPolicy(ctx, "global", "n-2")
	_ = db.AddAudit(ctx, store.AuditEntry{Action: "after backup"})

	cp := filepath.Join(t.TempDir(), "restore.db")
	raw, _ := os.ReadFile(bak)
	_ = os.WriteFile(cp, raw, 0o600)
	if err := db.Restore(ctx, cp); err != nil {
		t.Fatal(err)
	}
	if got, err := db.GetCluster(ctx, "prod"); err != nil || got.ID != c.ID {
		t.Errorf("cluster not restored: %v %v", got, err)
	}
	if _, err := db.ClusterByTokenHash(ctx, "h1"); !errors.Is(err, store.ErrNotFound) {
		t.Error("prod was deleted after the backup: its old token must stay revoked (rotate to reconnect)")
	}
	if _, err := db.GetCluster(ctx, "new"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("cluster created after the backup must be gone: %v", err)
	}
	if p, _ := db.ListPolicies(ctx); p["global"] != "latest" {
		t.Errorf("policies: %v", p)
	}
	if ce, _ := db.GetCache(ctx, "big"); ce == nil {
		t.Error("current upstream cache must be kept")
	}
	audit, _ := db.ListAudit(ctx, 10)
	if len(audit) != 2 || audit[0].Action != "after backup" {
		t.Errorf("audit log must be kept as is: %+v", audit)
	}
	if _, err := db.CreateCluster(ctx, "again", store.StageLive, "IT"); err != nil {
		t.Errorf("database must be writable after restore: %v", err)
	}
}

func TestRestoreRejectsGarbage(t *testing.T) {
	db := open(t)
	p := filepath.Join(t.TempDir(), "x.db")
	_ = os.WriteFile(p, []byte("not a database"), 0o600)
	if err := db.Restore(context.Background(), p); err == nil {
		t.Error("garbage must be rejected")
	}
}

func TestImportAndAuditPrune(t *testing.T) {
	ctx := context.Background()
	db := open(t)
	imp := store.ClusterImport{Name: "ds-prod", Stage: "qa", Tenant: "DS", KubernetesVersion: "v1.33.6+rke2r1",
		Images:  []store.ImageRow{{Namespace: "n", Kind: "Deployment", Workload: "w", Container: "c", Image: "nginx:1.25.4"}},
		Changes: []store.Change{{At: time.Now().Add(-time.Hour), Type: store.ChangeClusterFirstIn}}}
	c, err := db.ImportCluster(ctx, imp, "h2", "p")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ImportCluster(ctx, imp, "h3", "p"); !errors.Is(err, store.ErrExists) {
		t.Errorf("second import: %v", err)
	}
	st, _ := db.ListStages(ctx)
	if st[len(st)-1].Name != "qa" {
		t.Errorf("missing stage must be appended: %+v", st)
	}
	if imgs, _ := db.CurrentImages(ctx, c.ID); len(imgs) != 1 {
		t.Error("images not imported")
	}
	if got, err := db.ClusterByTokenHash(ctx, "h2"); err != nil || got.Name != "ds-prod" {
		t.Error("new token must work")
	}
	_ = db.AddAudit(ctx, store.AuditEntry{Action: "a"})
	if n, _ := db.PruneAudit(ctx, time.Now().Add(time.Hour)); n != 1 {
		t.Errorf("prune: %d", n)
	}
}

// A restore must not bring back a token that was rotated or a cluster that was deleted after the
// backup (the token may have leaked — that's why it was rotated).
func TestRestoreKeepsLaterRevocations(t *testing.T) {
	ctx := context.Background()
	db := open(t)
	a, _ := db.CreateClusterWithToken(ctx, "a", store.StageLive, "IT", "leaked", "p")
	_, _ = db.CreateClusterWithToken(ctx, "b", store.StageLive, "IT", "b-token", "p")
	bak := filepath.Join(t.TempDir(), "b.db")
	if err := db.Backup(ctx, bak); err != nil {
		t.Fatal(err)
	}
	_ = db.RotateToken(ctx, a.ID, "fresh", "p")
	_ = db.DeleteCluster(ctx, "b")
	if err := db.Restore(ctx, bak); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ClusterByTokenHash(ctx, "leaked"); !errors.Is(err, store.ErrNotFound) {
		t.Error("a token rotated after the backup must stay revoked")
	}
	if _, err := db.ClusterByTokenHash(ctx, "b-token"); !errors.Is(err, store.ErrNotFound) {
		t.Error("a deleted cluster's token must stay revoked")
	}
	if _, err := db.GetCluster(ctx, "b"); err != nil {
		t.Errorf("the cluster itself is restored (only its token stays dead): %v", err)
	}
}

// A valid SQLite file that isn't an Owl database (or an empty one) must not be "migrated" into an
// empty Owl schema and then restored over the real data.
func TestRestoreRejectsForeignSQLite(t *testing.T) {
	ctx := context.Background()
	db := open(t)
	_, _ = db.CreateCluster(ctx, "prod", store.StageLive, "IT")
	for name, ddl := range map[string]string{"empty": "", "other app": "CREATE TABLE notes(id INTEGER PRIMARY KEY, body TEXT)"} {
		p := filepath.Join(t.TempDir(), "foreign.db")
		other, err := sql.Open("sqlite", "file:"+p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := other.Exec("PRAGMA user_version = 0; " + ddl + ";"); err != nil {
			t.Fatal(err)
		}
		other.Close()
		if err := db.Restore(ctx, p); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", name, err)
		}
		if _, err := db.GetCluster(ctx, "prod"); err != nil {
			t.Fatalf("%s: current data was replaced: %v", name, err)
		}
	}
}

// Summaries cost money and the request log is what the spending caps count: a restore keeps both.
func TestRestoreKeepsSummariesAndSpend(t *testing.T) {
	ctx := context.Background()
	db := open(t)
	_, _ = db.CreateCluster(ctx, "prod", store.StageLive, "IT")
	bak := filepath.Join(t.TempDir(), "b.db")
	if err := db.Backup(ctx, bak); err != nil {
		t.Fatal(err)
	}
	_ = db.SetSetting(ctx, "summaryPaused", "3 failed requests in a row")
	_ = db.SetSetting(ctx, "summaryDailyCapMicros", "1000000")
	_ = db.PutSummary(ctx, store.Summary{Component: "grafana", FromVersion: "1", ToVersion: "2", Model: "m", PromptVersion: 1, Content: []byte(`{}`)})
	_ = db.AddSummaryRequest(ctx, store.SummaryRequest{Component: "grafana", Model: "m", CostMicros: 4_000_000, Result: "ok"})
	if err := db.Restore(ctx, bak); err != nil {
		t.Fatal(err)
	}
	if all, _ := db.ListSummaries(ctx); len(all) != 1 {
		t.Error("summaries must survive a restore")
	}
	if spent, _ := db.SummarySpend(ctx, time.Time{}); spent != 4_000_000 {
		t.Errorf("a restore must not reset the spend counter: %d", spent)
	}
	if s, _ := db.GetSettings(ctx); s["summaryPaused"] == "" || s["summaryDailyCapMicros"] != "1000000" {
		t.Errorf("a restore must not unpause summaries or change their caps: %v", s)
	}
}

// Notification state isn't restored: an old backup must not make Owl repeat a digest or re-announce.
// Settings (the switches) are.
func TestRestoreKeepsNotifyState(t *testing.T) {
	ctx := context.Background()
	db := open(t)
	_ = db.SetSetting(ctx, "notifyCritical", "")
	bak := filepath.Join(t.TempDir(), "b.db")
	if err := db.Backup(ctx, bak); err != nil {
		t.Fatal(err)
	}
	_ = db.SetSetting(ctx, "notifyCritical", "on")
	if err := db.ApplyNotifyDelta(ctx, store.NotifyDelta{AddCritical: []store.CriticalMark{{Component: "redis", Cluster: "a"}},
		Clusters: []string{"a@x"}, Repos: []string{"r"}, Meta: map[string]string{"digestSlot": "2026-11-16T08:00:00Z"}}); err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyNotifyDelta(ctx, store.NotifyDelta{Resolved: []store.CriticalMark{{Component: "redis", Cluster: "a"}}}); err != nil {
		t.Fatal(err)
	}
	if err := db.Restore(ctx, bak); err != nil {
		t.Fatal(err)
	}
	st, err := db.NotifyState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	res, _ := db.ListResolved(ctx, time.Time{})
	if !st.Clusters["a@x"] || !st.Repos["r"] || st.Meta["digestSlot"] == "" || len(res) != 1 {
		t.Errorf("notify state lost in restore: %+v %+v", st, res)
	}
	if m, _ := db.GetSettings(ctx); m["notifyCritical"] != "" {
		t.Error("settings should come from the backup")
	}
}

// Custom components are configuration: restored with a backup. Deleting one takes its policy and the
// overrides mapping to it along, atomically.
func TestCustomComponents(t *testing.T) {
	ctx := context.Background()
	db := open(t)
	if err := db.CreateCustomComponent(ctx, "sftp", []byte(`{"name":"SFTP"}`)); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateCustomComponent(ctx, "sftp", []byte(`{}`)); !errors.Is(err, store.ErrExists) {
		t.Fatalf("duplicate: %v", err)
	}
	if err := db.UpdateCustomComponent(ctx, "nope", []byte(`{}`)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("update missing: %v", err)
	}
	_ = db.SetPolicy(ctx, "sftp", "n-1")
	_ = db.SetOverride(ctx, store.CatalogOverride{Repository: "harbor.example.com/x/sftp", Kind: store.OverrideMap, Component: "sftp"})
	_ = db.SetOverride(ctx, store.CatalogOverride{Repository: "harbor.example.com/x/redis", Kind: store.OverrideMap, Component: "redis"})
	bak := filepath.Join(t.TempDir(), "b.db")
	if err := db.Backup(ctx, bak); err != nil {
		t.Fatal(err)
	}
	imp, err := db.CustomDeleteImpact(ctx, "sftp")
	if err != nil || !imp.Policy || len(imp.Overrides) != 1 {
		t.Fatalf("impact %+v %v", imp, err)
	}
	if _, err := db.DeleteCustomComponent(ctx, "sftp", true); err != nil {
		t.Fatal(err)
	}
	pols, _ := db.ListPolicies(ctx)
	ovs, _ := db.ListOverrides(ctx)
	cs, _ := db.ListCustomComponents(ctx)
	if pols["sftp"] != "" || len(ovs) != 1 || ovs[0].Component != "redis" || len(cs) != 0 {
		t.Fatalf("after delete: %v %+v %+v", pols, ovs, cs)
	}
	if err := db.Restore(ctx, bak); err != nil {
		t.Fatal(err)
	}
	if cs, _ := db.ListCustomComponents(ctx); len(cs) != 1 || string(cs[0].Data) != `{"name":"SFTP"}` {
		t.Fatalf("not restored: %+v", cs)
	}
}

// Scan results are regenerable: never in a backup, untouched by a restore. Acceptances are decisions:
// restored. The daily-rescan marker describes this server's scanner: kept.
func TestBackupScansAndAcceptances(t *testing.T) {
	ctx := context.Background()
	db := open(t)
	d := "sha256:" + string(make([]byte, 0))
	at := time.Now()
	_ = db.PutImageScan(ctx, store.ImageScan{Digest: d + "a", Ref: "r@a", ScannedAt: &at, Result: []byte(`{"vulns":[]}`), AttemptAt: at})
	_ = db.PutVulnAcceptance(ctx, store.VulnAcceptance{Repository: "reg/app", VulnID: "CVE-1", Reason: "not reachable", CreatedBy: "a@x"})
	_ = db.SetSetting(ctx, "scanLastDaily", "before")
	id, _ := db.AddRegistryLogin(ctx, store.RegistryLogin{Name: "team", Username: "u", Secret: []byte("sealed-1"), SealID: "s1", UpdatedBy: "a@x", Scopes: []string{"reg/team", "reg/team2"}})

	bak := filepath.Join(t.TempDir(), "b.db")
	if err := db.Backup(ctx, bak); err != nil {
		t.Fatal(err)
	}
	raw, _ := sql.Open("sqlite", "file:"+bak)
	var n int
	_ = raw.QueryRow("SELECT count(*) FROM image_scans").Scan(&n)
	raw.Close()
	if n != 0 {
		t.Errorf("backup has %d image scans", n)
	}

	_ = db.DeleteVulnAcceptance(ctx, "reg/app", "CVE-1")
	_ = db.PutVulnAcceptance(ctx, store.VulnAcceptance{Repository: "reg/app", VulnID: "CVE-2", Reason: "later", CreatedBy: "a@x"})
	_ = db.PutImageScan(ctx, store.ImageScan{Digest: d + "b", Ref: "r@b", Error: "auth", AttemptAt: at})
	_ = db.SetSetting(ctx, "scanLastDaily", "after")
	_ = db.UpdateRegistryLogin(ctx, store.RegistryLogin{ID: id, Name: "team", Username: "u2", Secret: []byte("sealed-2"), SealID: "s1", UpdatedBy: "a@x"})
	_ = db.DeleteRegistryScope(ctx, "reg/team2")
	_, _ = db.AddRegistryLogin(ctx, store.RegistryLogin{Name: "other", Username: "u", Secret: []byte("x"), SealID: "s2", UpdatedBy: "a@x", Scopes: []string{"reg/other"}})
	cp := filepath.Join(t.TempDir(), "restore.db")
	b, _ := os.ReadFile(bak)
	_ = os.WriteFile(cp, b, 0o600)
	if err := db.Restore(ctx, cp); err != nil {
		t.Fatal(err)
	}
	as, _ := db.ListVulnAcceptances(ctx)
	if len(as) != 1 || as[0].VulnID != "CVE-1" || as[0].Reason != "not reachable" {
		t.Errorf("acceptances = %+v", as)
	}
	// Registry credentials are kept in backups (sealed) and come back with a restore.
	if rc, _ := db.ListRegistryLogins(ctx); len(rc) != 1 || rc[0].Username != "u" || string(rc[0].Secret) != "sealed-1" ||
		!slices.Equal(rc[0].Scopes, []string{"reg/team", "reg/team2"}) {
		t.Errorf("registry credentials = %+v", rc)
	}
	scans, _ := db.ListImageScans(ctx)
	if len(scans) != 2 {
		t.Errorf("a restore keeps this server's scans: %+v", scans)
	}
	if s, _ := db.GetSettings(ctx); s["scanLastDaily"] != "after" {
		t.Errorf("scanLastDaily = %q", s["scanLastDaily"])
	}
}

func TestImageScansAndAcceptances(t *testing.T) {
	ctx := context.Background()
	db := open(t)
	at := time.Now().Add(-time.Hour)
	for _, d := range []string{"a", "b", "c"} {
		if err := db.PutImageScan(ctx, store.ImageScan{Digest: d, Ref: "r@" + d, AttemptAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	later := time.Now()
	_ = db.PutImageScan(ctx, store.ImageScan{Digest: "a", Ref: "r2@a", ScannedAt: &later, Result: []byte(`{"os":"x"}`), AttemptAt: later})
	if n, err := db.DeleteImageScansExcept(ctx, []string{"a", "c"}); err != nil || n != 1 {
		t.Fatalf("delete: %d %v", n, err)
	}
	scans, _ := db.ListImageScans(ctx)
	if len(scans) != 2 || scans[0].Digest != "a" || scans[0].Ref != "r2@a" || string(scans[0].Result) != `{"os":"x"}` ||
		scans[0].ScannedAt == nil || scans[1].ScannedAt != nil {
		t.Errorf("scans = %+v", scans)
	}
	if n, _ := db.DeleteImageScansExcept(ctx, nil); n != 2 {
		t.Errorf("delete all: %d", n)
	}

	exp := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	_ = db.PutVulnAcceptance(ctx, store.VulnAcceptance{Repository: "reg/app", VulnID: "CVE-1", Reason: "r1", CreatedBy: "a"})
	_ = db.PutVulnAcceptance(ctx, store.VulnAcceptance{Repository: "reg/app", VulnID: "CVE-1", Reason: "r2", ExpiresAt: &exp, CreatedBy: "b"})
	as, _ := db.ListVulnAcceptances(ctx)
	if len(as) != 1 || as[0].Reason != "r2" || as[0].ExpiresAt == nil || !as[0].ExpiresAt.Equal(exp) || as[0].CreatedBy != "b" {
		t.Errorf("acceptances = %+v", as)
	}
	if err := db.DeleteVulnAcceptance(ctx, "reg/app", "CVE-9"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("delete missing: %v", err)
	}
}
