package inventory

import (
	"context"
	"path/filepath"
	"slices"
	"sort"
	"testing"

	"owl/internal/catalog"
	"owl/internal/snapshot"
	"owl/internal/store"
	"owl/internal/store/sqlite"
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

// Overrides: map decides unrecognized images only (with the component's version rules) and never
// re-maps one the catalog recognizes; in-house and ignore are choices about a repository and win even
// over the catalog, so a catalog that learns an image later doesn't undo them.
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
	for _, img := range []string{"ghcr.io/acme/redis-fork:7.2.4", "ghcr.io/acme/tool:0.4.0", "ghcr.io/acme/noise:1", "nginx:1.25.4", "bitnami/redis:7.2.5", "grafana/grafana:11.1.0"} {
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
		{Repository: "docker.io/library/nginx", Kind: store.OverrideIgnore},                    // recognized: ignore still applies
		{Repository: "docker.io/bitnami/redis", Kind: store.OverrideInHouse},                   // recognized: in-house still applies
		{Repository: "docker.io/grafana/grafana", Kind: store.OverrideMap, Component: "nginx"}, // recognized: map doesn't
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
	if g := byID["grafana"]; len(g.Instances) != 1 || g.Instances[0].Override != "" || len(byID["nginx"].Instances) != 0 {
		t.Errorf("a map override must not re-map a recognized image: grafana %+v nginx %+v", g.Instances, byID["nginx"].Instances)
	}
	repos := func(l []Instance) (out []string) {
		for _, in := range l {
			out = append(out, in.Repository+"/"+in.Override)
		}
		sort.Strings(out)
		return out
	}
	if got := repos(inv.InHouse); !slices.Equal(got, []string{"docker.io/bitnami/redis/inhouse", "ghcr.io/acme/tool/inhouse"}) {
		t.Errorf("in-house: %v", got)
	}
	if got := repos(inv.Ignored); !slices.Equal(got, []string{"docker.io/library/nginx/ignore", "ghcr.io/acme/noise/ignore"}) {
		t.Errorf("ignored: %v", got)
	}
	for _, in := range inv.InHouse {
		if in.Repository == "docker.io/bitnami/redis" && in.Version != "7.2.5" {
			t.Errorf("in-house over a recognized image keeps the tag's version: %q", in.Version)
		}
	}
	if len(inv.Unrecognized) != 0 {
		t.Errorf("nothing should stay unrecognized: %+v", inv.Unrecognized)
	}
}

// A bundled image is marked only in a cluster of the distribution that ships it.
func TestBundledWith(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cat, _ := catalog.Builtin()
	for _, cl := range []struct{ name, version string }{{"rke", "v1.30.4+rke2r1"}, {"k3", "v1.31.2+k3s1"}, {"eks", "v1.31.2-eks-7f9249a"}} {
		c, _ := db.CreateCluster(ctx, cl.name, store.StageLive, "IT")
		snap := &snapshot.Snapshot{SchemaVersion: 1, Cluster: snapshot.Cluster{KubernetesVersion: cl.version}}
		for _, img := range []string{"rancher/hardened-coredns:v1.11.1-build20240305", "coredns/coredns:1.11.3"} {
			snap.Workloads = append(snap.Workloads, snapshot.Workload{Kind: "Deployment", Namespace: "kube-system", Name: img,
				Containers: []snapshot.Container{{Name: "c", Image: img}}})
		}
		if _, err := db.Ingest(ctx, c.ID, snap); err != nil {
			t.Fatal(err)
		}
	}
	inv, err := Build(ctx, db, cat)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, cv := range inv.Components {
		if cv.ID != "coredns" {
			continue
		}
		for _, in := range cv.Instances {
			if in.BundledWith != "" {
				got = append(got, in.Cluster+"/"+in.Image+"/"+in.BundledWith)
			}
		}
	}
	if want := []string{"rke/rancher/hardened-coredns:v1.11.1-build20240305/rke2"}; !slices.Equal(got, want) {
		t.Errorf("bundled instances: %v, want %v", got, want)
	}
}
