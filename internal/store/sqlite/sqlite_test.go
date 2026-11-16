package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"service-inventory/internal/snapshot"
	"service-inventory/internal/store"
)

// A database at schema v3 (before user-defined stages) must keep its clusters, current state, history
// and tokens through the clusters-table rebuild — and foreign keys must still cascade afterwards.
func TestMigrationKeepsData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=foreign_keys(1)", path))
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if _, err := raw.Exec(migrations[i]); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		"PRAGMA user_version = 3",
		"INSERT INTO clusters(id, name, stage, tenant, created_at) VALUES (1, 'prod', 'live', 'IT', 'x'), (2, 'stg', 'staging', 'DS', 'x')",
		"INSERT INTO agent_tokens(cluster_id, token_hash, prefix, created_at) VALUES (1, 'h1', 'p', 'x')",
		"INSERT INTO current_images(cluster_id, namespace, kind, workload, container, image) VALUES (1, 'ns', 'Deployment', 'w', 'c', 'nginx:1.25.4')",
		"INSERT INTO changes(cluster_id, at, type) VALUES (1, '2026-01-01T00:00:00.000Z', 'first_snapshot')",
	} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	raw.Close()

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c, err := db.GetCluster(ctx, "prod")
	if err != nil || c.Stage != "live" || c.Tenant != "IT" {
		t.Fatalf("cluster after migration: %+v %v", c, err)
	}
	if imgs, _ := db.CurrentImages(ctx, c.ID); len(imgs) != 1 {
		t.Errorf("current images lost: %v", imgs)
	}
	if ch, _ := db.AllChanges(ctx, c.ID); len(ch) != 1 {
		t.Errorf("history lost: %v", ch)
	}
	if got, err := db.ClusterByTokenHash(ctx, "h1"); err != nil || got.Name != "prod" {
		t.Errorf("token lost: %v %v", got, err)
	}
	tenants, _ := db.ListTenants(ctx)
	if len(tenants) != 2 || tenants[0].Name != "DS" || tenants[1].Name != "IT" {
		t.Errorf("tenants seeded from clusters: %+v", tenants)
	}
	// Cascades still work after the rebuild.
	if err := db.DeleteCluster(ctx, "prod"); err != nil {
		t.Fatal(err)
	}
	var n int
	db.db.QueryRow("SELECT count(*) FROM current_images").Scan(&n)
	if n != 0 {
		t.Errorf("delete didn't cascade to current_images: %d rows", n)
	}
	var fk int
	db.db.QueryRow("PRAGMA foreign_keys").Scan(&fk)
	if fk != 1 {
		t.Error("foreign keys must be back on after migrating")
	}
}

func open(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestStagesAndTenants(t *testing.T) {
	ctx := context.Background()
	db := open(t)

	if _, err := db.CreateCluster(ctx, "x", "qa", "IT"); !errors.Is(err, store.ErrUnknownStage) {
		t.Fatalf("unknown stage must be rejected: %v", err)
	}
	if err := db.CreateStage(ctx, "dev"); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateStage(ctx, "dev"); !errors.Is(err, store.ErrExists) {
		t.Errorf("duplicate stage: %v", err)
	}
	if err := db.ReorderStages(ctx, []string{"dev", "staging", "prelive", "live"}); err != nil {
		t.Fatal(err)
	}
	if err := db.ReorderStages(ctx, []string{"dev", "staging"}); err == nil {
		t.Error("partial reorder must fail")
	}
	st, _ := db.ListStages(ctx)
	if st[0].Name != "dev" || st[0].Position != 1 || st[3].Name != "live" {
		t.Errorf("order: %+v", st)
	}

	c, err := db.CreateCluster(ctx, "prod", store.StageLive, "IT") // creates tenant IT
	if err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteStage(ctx, "live"); !errors.Is(err, store.ErrInUse) {
		t.Errorf("stage in use: %v", err)
	}
	if err := db.DeleteTenant(ctx, "IT"); !errors.Is(err, store.ErrInUse) {
		t.Errorf("tenant in use: %v", err)
	}
	if err := db.RenameTenant(ctx, "IT", "Platform"); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.GetCluster(ctx, "prod"); got.Tenant != "Platform" {
		t.Errorf("rename must move clusters along: %q", got.Tenant)
	}
	if _, err := db.UpdateCluster(ctx, "prod", "dev", "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown tenant on update: %v", err)
	}
	if err := db.CreateTenant(ctx, "DS"); err != nil {
		t.Fatal(err)
	}
	got, err := db.UpdateCluster(ctx, "prod", "dev", "DS")
	if err != nil || got.Stage != "dev" || got.Tenant != "DS" || got.ID != c.ID {
		t.Fatalf("update: %+v %v", got, err)
	}
	if err := db.DeleteTenant(ctx, "Platform"); err != nil {
		t.Errorf("unused tenant must be deletable: %v", err)
	}
	if err := db.DeleteStage(ctx, "live"); err != nil {
		t.Errorf("unused stage must be deletable: %v", err)
	}
}

func TestOverridesAndSettings(t *testing.T) {
	ctx := context.Background()
	db := open(t)
	o := store.CatalogOverride{Repository: "ghcr.io/acme/x", Kind: store.OverrideMap, Component: "redis"}
	if err := db.SetOverride(ctx, o); err != nil {
		t.Fatal(err)
	}
	o.Kind, o.Component = store.OverrideIgnore, ""
	if err := db.SetOverride(ctx, o); err != nil { // upsert
		t.Fatal(err)
	}
	list, _ := db.ListOverrides(ctx)
	if len(list) != 1 || list[0].Kind != store.OverrideIgnore || list[0].UpdatedAt.IsZero() {
		t.Errorf("overrides: %+v", list)
	}
	if err := db.DeleteOverride(ctx, "ghcr.io/acme/x"); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteOverride(ctx, "ghcr.io/acme/x"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
	_ = db.SetSetting(ctx, "agentImage", "a:1")
	_ = db.SetSetting(ctx, "agentImage", "a:2")
	if s, _ := db.GetSettings(ctx); s["agentImage"] != "a:2" {
		t.Errorf("settings: %v", s)
	}
}

// AllChanges returns a cluster's history oldest first, unlike ListChanges.
func TestAllChangesOrder(t *testing.T) {
	ctx := context.Background()
	db := open(t)
	c, _ := db.CreateCluster(ctx, "c", store.StageStaging, "IT")
	for _, img := range []string{"nginx:1.0.0", "nginx:1.1.0"} {
		s := &snapshot.Snapshot{SchemaVersion: snapshot.SchemaVersion, Workloads: []snapshot.Workload{{Kind: "Deployment",
			Namespace: "n", Name: "w", Containers: []snapshot.Container{{Name: "c", Image: img}}}}}
		if _, err := db.Ingest(ctx, c.ID, s); err != nil {
			t.Fatal(err)
		}
	}
	ch, _ := db.AllChanges(ctx, c.ID)
	if len(ch) != 2 || ch[0].Type != store.ChangeClusterFirstIn || ch[1].Type != store.ChangeImageChanged {
		t.Errorf("history: %+v", ch)
	}
}
