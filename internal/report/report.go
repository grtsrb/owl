package report

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"owl/internal/catalog"
	"owl/internal/inventory"
	"owl/internal/policy"
	"owl/internal/store"
	"owl/internal/upstream"
)

type ScoredInstance struct {
	inventory.Instance
	Score  Score  `json:"score"`
	Action Action `json:"action"`
}

type UpstreamSummary struct {
	LatestStable   string     `json:"latestStable,omitempty"`
	Retired        bool       `json:"retired"`
	Archived       bool       `json:"archived"`
	LastReleaseAt  *time.Time `json:"lastReleaseAt,omitempty"`
	AdvisorySource string     `json:"advisorySource"`
	Stale          bool       `json:"stale,omitempty"`
	FetchedAt      *time.Time `json:"fetchedAt,omitempty"`
}

type DriftKind string

const (
	DriftPending DriftKind = "pending_promotion" // newer in an earlier stage, not yet promoted
	DriftAnomaly DriftKind = "anomaly"           // a later stage runs a newer version than an earlier one
	DriftOnlyIn  DriftKind = "only_in"           // info: runs in some of the tenant's clusters only
)

// Drift compares clusters of one tenant along the stage order. Clusters names the clusters the entry
// is about, so the UI can show it only when they are all selected.
type Drift struct {
	Kind     DriftKind `json:"kind"`
	Text     string    `json:"text"`
	Tenant   string    `json:"tenant"`
	Clusters []string  `json:"clusters"`
}

type ComponentReport struct {
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	Level     Level            `json:"level"`
	Target    *policy.Target   `json:"target,omitempty"`
	Upstream  UpstreamSummary  `json:"upstream"`
	Instances []ScoredInstance `json:"instances"`
	Drift     []Drift          `json:"drift"`
	Notes     string           `json:"notes,omitempty"`
	// Action is the component's goal across all its instances — the same whatever clusters the UI
	// shows. Per-instance actions say what each cluster needs to get there.
	Action Action `json:"action"`
}

type Report struct {
	GeneratedAt time.Time         `json:"generatedAt"`
	Summary     map[Level]int     `json:"summary"` // components per level
	Policies    map[string]string `json:"policies"`
	Components  []ComponentReport `json:"components"`
	Unpinned    int               `json:"unpinned"`
	InHouse     int               `json:"inHouse"`
	// Unrecognized images can't be scored; the UI lists them for mapping.
	Unrecognized int `json:"unrecognized"`
	// Stages in promotion order (user-defined), for ordering columns.
	Stages []string `json:"stages"`
	// Inventory the report was built from (for notifications; not part of the API).
	Inventory *inventory.Inventory `json:"-"`
}

// Inputs are what Build needs; the server supplies them.
type Inputs struct {
	Catalog  *catalog.Catalog
	Store    store.Store
	Upstream *upstream.Client
	Infos    func(id string) *upstream.Info
	Now      time.Time
}

func Build(ctx context.Context, in Inputs) (*Report, error) {
	inv, err := inventory.Build(ctx, in.Store, in.Catalog)
	if err != nil {
		return nil, err
	}
	pols, err := in.Store.ListPolicies(ctx)
	if err != nil {
		return nil, err
	}
	if pols["global"] == "" {
		pols["global"] = policy.Default
	}
	stages, err := in.Store.ListStages(ctx)
	if err != nil {
		return nil, err
	}
	order := map[string]int{}
	for _, st := range stages {
		order[st.Name] = st.Position
	}
	clusters, err := in.Store.ListClusters(ctx)
	if err != nil {
		return nil, err
	}
	rep := &Report{GeneratedAt: in.Now, Policies: pols, Summary: map[Level]int{}, Stages: stageNames(stages),
		Unpinned: len(inv.Unpinned), InHouse: len(inv.InHouse), Inventory: inv}
	for _, u := range inv.Unrecognized {
		rep.Unrecognized += len(u.Instances)
	}

	for _, cv := range inv.Components {
		comp := in.Catalog.ByID(cv.ID)
		info := in.Infos(cv.ID)
		cr := ComponentReport{ID: cv.ID, Name: cv.Name, Level: LevelOK, Notes: cv.Notes, Drift: []Drift{}}
		if info != nil {
			cr.Upstream = UpstreamSummary{LatestStable: info.LatestStable, Retired: info.Retired, Archived: info.Archived,
				LastReleaseAt: info.LastReleaseAt, AdvisorySource: info.AdvisorySource, Stale: info.Stale, FetchedAt: &info.FetchedAt}
			if sp, note := effectivePolicy(pols, cv.ID); true {
				if t, ok := policy.Resolve(sp, info.Lines, in.Now); ok {
					if note != "" {
						t.Note = strings.TrimPrefix(t.Note+"; "+note, "; ")
					}
					cr.Target = &t
				}
			}
		}
		for _, inst := range cv.Instances {
			var matches []upstream.Match
			if info != nil && in.Upstream != nil && inst.Status == string(catalog.StatusIdentified) {
				matches, _ = in.Upstream.AdvisoriesFor(ctx, comp, info, inst.Version)
			}
			sc := ScoreInstance(inst, info, cr.Target, matches, in.Now)
			cr.Instances = append(cr.Instances, ScoredInstance{Instance: inst, Score: sc, Action: ActionFor(inst, sc, cr.Target, info)})
			if sc.Level.Rank() > cr.Level.Rank() {
				cr.Level = sc.Level
			}
		}
		sort.SliceStable(cr.Instances, func(i, j int) bool {
			a, b := cr.Instances[i], cr.Instances[j]
			if ra, rb := order[a.Stage], order[b.Stage]; ra != rb {
				return ra < rb
			}
			return a.Cluster+a.Namespace+a.Workload < b.Cluster+b.Namespace+b.Workload
		})
		cr.Drift = drift(cr.Instances, order, clusters)
		cr.Action = ComponentAction(cr.Instances, cr.Target)
		rep.Summary[cr.Level]++
		rep.Components = append(rep.Components, cr)
	}
	sort.SliceStable(rep.Components, func(i, j int) bool {
		a, b := rep.Components[i], rep.Components[j]
		if a.Level.Rank() != b.Level.Rank() {
			return a.Level.Rank() > b.Level.Rank()
		}
		return a.Name < b.Name
	})
	return rep, nil
}

// effectivePolicy: component override, else global, else the default. An invalid stored policy falls
// back to the next level (with a note) instead of silently producing no target.
func effectivePolicy(pols map[string]string, id string) (policy.Spec, string) {
	note := ""
	for _, scope := range []string{id, "global"} {
		raw := pols[scope]
		if raw == "" {
			continue
		}
		sp, err := policy.Parse(raw)
		if err == nil {
			return sp, note
		}
		note = fmt.Sprintf("invalid %s policy %q ignored", scope, raw)
	}
	sp, _ := policy.Parse(policy.Default)
	return sp, note
}

func stageNames(st []store.StageInfo) []string {
	out := make([]string, len(st))
	for i, x := range st {
		out[i] = x.Name
	}
	return out
}

// drift compares stages in their configured order, separately per tenant (IT's staging says nothing
// about DS's prod): each cluster of a later stage against the newest version of the previous stage
// present for that component, so partially promoted stages are reported per cluster. Clusters where
// the component doesn't run are only an info entry — environments legitimately differ.
func drift(insts []ScoredInstance, order map[string]int, clusters []store.Cluster) []Drift {
	type clusterV struct {
		cluster string
		v       catalog.Version
	}
	out := []Drift{}
	byTenant := map[string][]ScoredInstance{}
	for _, in := range insts {
		byTenant[in.Tenant] = append(byTenant[in.Tenant], in)
	}
	tenants := make([]string, 0, len(byTenant))
	for t := range byTenant {
		tenants = append(tenants, t)
	}
	sort.Strings(tenants)

	for _, tenant := range tenants {
		newest := map[string]map[string]catalog.Version{} // stage -> cluster -> newest version there
		running := map[string]bool{}
		for _, in := range byTenant[tenant] {
			running[in.Cluster] = true
			if in.Status != string(catalog.StatusIdentified) {
				continue
			}
			v, ok := catalog.ParseVersion(in.Version, "")
			if !ok {
				continue
			}
			if newest[in.Stage] == nil {
				newest[in.Stage] = map[string]catalog.Version{}
			}
			if cur, ok := newest[in.Stage][in.Cluster]; !ok || v.Compare(cur) > 0 {
				newest[in.Stage][in.Cluster] = v
			}
		}
		var present []string
		for st := range newest {
			present = append(present, st)
		}
		sort.Slice(present, func(i, j int) bool {
			if order[present[i]] != order[present[j]] {
				return order[present[i]] < order[present[j]]
			}
			return present[i] < present[j]
		})
		for i := 0; i+1 < len(present); i++ {
			early, late := present[i], present[i+1]
			var best catalog.Version
			bestCluster := ""
			for c, v := range newest[early] {
				if bestCluster == "" || v.Compare(best) > 0 || v.Compare(best) == 0 && c < bestCluster {
					best, bestCluster = v, c
				}
			}
			var cs []clusterV
			for c, v := range newest[late] {
				cs = append(cs, clusterV{c, v})
			}
			sort.Slice(cs, func(a, b int) bool { return cs[a].cluster < cs[b].cluster })
			for _, c := range cs {
				pair := []string{bestCluster, c.cluster}
				switch cmp := c.v.Compare(best); {
				case cmp > 0:
					out = append(out, Drift{DriftAnomaly, fmt.Sprintf("%s (%s) runs %s, newer than %s (%s) — changed outside the promotion path?",
						c.cluster, late, c.v, early, best), tenant, pair})
				case cmp < 0:
					out = append(out, Drift{DriftPending, fmt.Sprintf("%s in %s, not yet in %s (%s runs %s)",
						best, early, late, c.cluster, c.v), tenant, pair})
				}
			}
		}

		var missing []string
		for _, c := range clusters {
			if c.Tenant == tenant && c.LastSeenAt != nil && !running[c.Name] { // not reported yet = unknown, not missing
				missing = append(missing, c.Name)
			}
		}
		if len(missing) > 0 {
			out = append(out, Drift{DriftOnlyIn, "not running in " + strings.Join(missing, ", "), tenant, missing})
		}
	}
	return out
}
