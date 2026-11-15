package report

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"service-inventory/internal/catalog"
	"service-inventory/internal/inventory"
	"service-inventory/internal/policy"
	"service-inventory/internal/store"
	"service-inventory/internal/upstream"
)

type ScoredInstance struct {
	inventory.Instance
	Score Score `json:"score"`
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
)

type Drift struct {
	Kind DriftKind `json:"kind"`
	Text string    `json:"text"`
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
	rep := &Report{GeneratedAt: in.Now, Policies: pols, Summary: map[Level]int{},
		Unpinned: len(inv.Unpinned), InHouse: len(inv.InHouse)}
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
			cr.Instances = append(cr.Instances, ScoredInstance{Instance: inst, Score: sc})
			if sc.Level.Rank() > cr.Level.Rank() {
				cr.Level = sc.Level
			}
		}
		sort.SliceStable(cr.Instances, func(i, j int) bool {
			a, b := cr.Instances[i], cr.Instances[j]
			if ra, rb := stageRank(a.Stage), stageRank(b.Stage); ra != rb {
				return ra < rb
			}
			return a.Cluster+a.Namespace+a.Workload < b.Cluster+b.Namespace+b.Workload
		})
		cr.Drift = drift(cr.Instances)
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

func stageRank(s string) int { return store.Stage(s).Rank() }

// drift compares stages along staging -> prelive -> live: each cluster of a later stage against the
// newest version of the previous stage, so partially promoted stages are reported per cluster.
func drift(insts []ScoredInstance) []Drift {
	type clusterV struct {
		cluster string
		v       catalog.Version
	}
	newest := map[string]map[string]catalog.Version{} // stage -> cluster -> newest version there
	for _, in := range insts {
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
	for _, st := range []store.Stage{store.StageStaging, store.StagePrelive, store.StageLive} {
		if newest[string(st)] != nil {
			present = append(present, string(st))
		}
	}
	out := []Drift{}
	for i := 0; i+1 < len(present); i++ {
		early, late := present[i], present[i+1]
		var best catalog.Version
		first := true
		for _, v := range newest[early] {
			if first || v.Compare(best) > 0 {
				best, first = v, false
			}
		}
		var clusters []clusterV
		for c, v := range newest[late] {
			clusters = append(clusters, clusterV{c, v})
		}
		sort.Slice(clusters, func(a, b int) bool { return clusters[a].cluster < clusters[b].cluster })
		for _, c := range clusters {
			switch cmp := c.v.Compare(best); {
			case cmp > 0:
				out = append(out, Drift{DriftAnomaly, fmt.Sprintf("%s (%s) runs %s, newer than %s (%s) — changed outside the promotion path?",
					c.cluster, late, c.v, early, best)})
			case cmp < 0:
				out = append(out, Drift{DriftPending, fmt.Sprintf("%s in %s, not yet in %s (%s runs %s)",
					best, early, late, c.cluster, c.v)})
			}
		}
	}
	return out
}
