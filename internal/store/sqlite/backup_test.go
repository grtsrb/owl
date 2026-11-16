package sqlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
