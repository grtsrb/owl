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

// Overrides decide unrecognized images only: map uses the component's version rules, in-house and
// ignore move the image out of the unrecognized list, and a catalog-recognized image is never overridden.
func TestOverrides(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cat, _ := catalog.Builtin()
	c, _ := db.CreateCluster(ctx, "prod", store.StageLive, "IT")
	snap := &snapshot.Snapshot{SchemaVersion: 1}
	for _, img := range []string{"ghcr.io/acme/redis-fork:7.2.4", "ghcr.io/acme/tool:0.4.0", "ghcr.io/acme/noise:1", "nginx:1.25.4"} {
		snap.Workloads = append(snap.Workloads, snapshot.Workload{Kind: "Deployment", Namespace: "ns", Name: img,
			Containers: []snapshot.Container{{Name: "c", Image: img}}})
	}
	if _, err := db.Ingest(ctx, c.ID, snap); err != nil {
		t.Fatal(err)
	}
	for _, o := range []store.CatalogOverride{
		{Repository: "ghcr.io/acme/redis-fork", Kind: store.OverrideMap, Component: "redis"},
		{Repository: "ghcr.io/acme/tool", Kind: store.OverrideInHouse},
		{Repository: "ghcr.io/acme/noise", Kind: store.OverrideIgnore},
		{Repository: "docker.io/library/nginx", Kind: store.OverrideIgnore}, // recognized: must not apply
	} {
		if err := db.SetOverride(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	inv, err := Build(ctx, db, cat)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]ComponentView{}
	for _, cv := range inv.Components {
		byID[cv.ID] = cv
	}
	if r := byID["redis"]; len(r.Instances) != 1 || r.Instances[0].Version != "7.2.4" || r.Instances[0].Override != "map" {
		t.Errorf("mapped redis: %+v", r.Instances)
	}
	if len(byID["nginx"].Instances) != 1 {
		t.Error("an override must not hide a recognized image")
	}
	if len(inv.InHouse) != 1 || inv.InHouse[0].Repository != "ghcr.io/acme/tool" || inv.InHouse[0].Override != "inhouse" {
		t.Errorf("in-house: %+v", inv.InHouse)
	}
	if len(inv.Ignored) != 1 || inv.Ignored[0].Repository != "ghcr.io/acme/noise" {
		t.Errorf("ignored: %+v", inv.Ignored)
	}
	if len(inv.Unrecognized) != 0 {
		t.Errorf("nothing should stay unrecognized: %+v", inv.Unrecognized)
	}
}
