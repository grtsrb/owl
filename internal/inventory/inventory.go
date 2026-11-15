// Package inventory combines the stored cluster state with the catalog into a per-component view.
// It's computed on read, so catalog changes apply immediately without re-ingesting.
package inventory

import (
	"context"
	"sort"

	"service-inventory/internal/catalog"
	"service-inventory/internal/snapshot"
	"service-inventory/internal/store"
)

// Instance is one place a component runs.
type Instance struct {
	Cluster   string `json:"cluster"`
	Stage     string `json:"stage"`
	Tenant    string `json:"tenant"`
	Namespace string `json:"namespace,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Workload  string `json:"workload,omitempty"`
	Container string `json:"container,omitempty"`
	Image     string `json:"image"`
	Version   string `json:"version,omitempty"`
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
	// Replicas / running pods, so scaled-to-0 and half-done rollouts are visible.
	DesiredReplicas int32                   `json:"desiredReplicas"`
	Running         []snapshot.RunningImage `json:"running,omitempty"`
}

type ComponentView struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	GitHub    string     `json:"github,omitempty"`
	EOL       string     `json:"eol,omitempty"`
	Notes     string     `json:"notes,omitempty"`
	Instances []Instance `json:"instances"`
	// VersionsByCluster: cluster -> distinct versions there (several = mixed versions in one cluster).
	VersionsByCluster map[string][]string `json:"versionsByCluster"`
}

type Inventory struct {
	Components   []ComponentView `json:"components"`
	Unpinned     []Instance      `json:"unpinned"`
	InHouse      []Instance      `json:"inHouse"`
	Unrecognized []Unrecognized  `json:"unrecognized"`
}

// Unrecognized groups unknown images by repository, so the user maps each once.
type Unrecognized struct {
	Repository string     `json:"repository"` // registry/repository
	Instances  []Instance `json:"instances"`
}

func Build(ctx context.Context, st store.Store, cat *catalog.Catalog) (*Inventory, error) {
	clusters, err := st.ListClusters(ctx)
	if err != nil {
		return nil, err
	}
	comps := map[string]*ComponentView{}
	unknown := map[string]*Unrecognized{}
	inv := &Inventory{Components: []ComponentView{}, Unpinned: []Instance{}, InHouse: []Instance{}}

	add := func(id catalog.Identification, in Instance) {
		in.Status, in.Reason = string(id.Status), id.Reason
		if id.Version != nil {
			in.Version = id.Version.String()
		}
		switch id.Status {
		case catalog.StatusInHouse:
			inv.InHouse = append(inv.InHouse, in)
			return
		case catalog.StatusUnrecognized:
			name := id.Ref.Name()
			u := unknown[name]
			if u == nil {
				u = &Unrecognized{Repository: name}
				unknown[name] = u
			}
			u.Instances = append(u.Instances, in)
			return
		case catalog.StatusUnpinned:
			inv.Unpinned = append(inv.Unpinned, in)
		}
		c := comps[id.Component.ID]
		if c == nil {
			c = &ComponentView{ID: id.Component.ID, Name: id.Component.Name, GitHub: id.Component.GitHub,
				EOL: id.Component.EOL, Notes: id.Component.Notes, VersionsByCluster: map[string][]string{}}
			comps[id.Component.ID] = c
		}
		c.Instances = append(c.Instances, in)
		if in.Version != "" && !contains(c.VersionsByCluster[in.Cluster], in.Version) {
			c.VersionsByCluster[in.Cluster] = append(c.VersionsByCluster[in.Cluster], in.Version)
		}
	}

	for _, cl := range clusters {
		base := Instance{Cluster: cl.Name, Stage: string(cl.Stage), Tenant: cl.Tenant}
		if cl.KubernetesVersion != "" {
			for _, id := range cat.ClusterVersions(cl.KubernetesVersion) {
				in := base
				in.Image = cl.KubernetesVersion
				add(id, in)
			}
		}
		rows, err := st.CurrentImages(ctx, cl.ID)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			in := base
			in.Namespace, in.Kind, in.Workload, in.Container = r.Namespace, r.Kind, r.Workload, r.Container
			in.Image, in.DesiredReplicas, in.Running = r.Image, r.DesiredReplicas, r.Running
			add(cat.Identify(r.Image), in)
		}
	}

	for _, c := range comps {
		for _, vs := range c.VersionsByCluster {
			sort.Slice(vs, func(i, j int) bool { return versionLess(vs[i], vs[j]) })
		}
		inv.Components = append(inv.Components, *c)
	}
	sort.Slice(inv.Components, func(i, j int) bool { return inv.Components[i].Name < inv.Components[j].Name })
	for _, u := range unknown {
		inv.Unrecognized = append(inv.Unrecognized, *u)
	}
	sort.Slice(inv.Unrecognized, func(i, j int) bool { return inv.Unrecognized[i].Repository < inv.Unrecognized[j].Repository })
	if inv.Unrecognized == nil {
		inv.Unrecognized = []Unrecognized{}
	}
	return inv, nil
}

// versionLess orders version strings by version (1.9.0 < 1.10.0), falling back to text.
func versionLess(a, b string) bool {
	va, okA := catalog.ParseVersion(a, "")
	vb, okB := catalog.ParseVersion(b, "")
	if okA && okB {
		if c := va.Compare(vb); c != 0 {
			return c < 0
		}
	}
	return a < b
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
