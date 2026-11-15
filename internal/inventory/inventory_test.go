package inventory

import (
	"context"
	"path/filepath"
	"testing"

	"service-inventory/internal/catalog"
	"service-inventory/internal/snapshot"
	"service-inventory/internal/store"
	"service-inventory/internal/store/sqlite"
)

func TestBuild(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cat, _ := catalog.Builtin()

	inv, err := Build(ctx, db, cat)
	if err != nil || inv.Components == nil || len(inv.Components) != 0 {
		t.Fatalf("empty inventory must have components=[]: %+v %v", inv, err)
	}

	c, _ := db.CreateCluster(ctx, "prod", store.StageLive, "IT")
	snap := &snapshot.Snapshot{SchemaVersion: 1, Cluster: snapshot.Cluster{KubernetesVersion: "v1.30.4+rke2r1"}}
	for _, img := range []string{"nginx:1.10.0", "nginx:1.9.0", "redis:latest", "ghcr.io/acme/x:1"} {
		snap.Workloads = append(snap.Workloads, snapshot.Workload{Kind: "Deployment", Namespace: "ns", Name: img,
			Containers: []snapshot.Container{{Name: "c", Image: img}}})
	}
	if _, err := db.Ingest(ctx, c.ID, snap); err != nil {
		t.Fatal(err)
	}
	inv, err = Build(ctx, db, cat)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]ComponentView{}
	for _, cv := range inv.Components {
		byID[cv.ID] = cv
	}
	if got := byID["nginx"].VersionsByCluster["prod"]; len(got) != 2 || got[0] != "1.9.0" || got[1] != "1.10.0" {
		t.Errorf("versions must sort by version: %v", got)
	}
	if got := byID["rke2"].VersionsByCluster["prod"]; len(got) != 1 || got[0] != "1.30.4+rke2r1" {
		t.Errorf("rke2: %v", got)
	}
	if len(inv.Unpinned) != 1 || len(inv.Unrecognized) != 1 {
		t.Errorf("unpinned %d, unrecognized %d", len(inv.Unpinned), len(inv.Unrecognized))
	}
}
