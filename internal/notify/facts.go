package notify

import (
	"sort"
	"strings"

	"owl/internal/report"
)

// pair is one component on one cluster.
type pair struct{ Component, Cluster string }

// critical is a (component, cluster) pair that is Critical now, with what the message says about it.
type critical struct {
	pair
	Name     string
	Stage    string
	Versions []string
	Why      string
	Action   string
}

// criticalPairs lists every Critical (component, cluster) pair of the report, sorted.
func criticalPairs(rep *report.Report) map[pair]*critical {
	out := map[pair]*critical{}
	for _, c := range rep.Components {
		for _, in := range c.Instances {
			if in.Score.Level != report.LevelCritical {
				continue
			}
			k := pair{c.ID, in.Cluster}
			cr := out[k]
			if cr == nil {
				cr = &critical{pair: k, Name: c.Name, Stage: in.Stage, Action: in.Action.Text, Why: why(in)}
				out[k] = cr
			}
			if in.Version != "" && !contains(cr.Versions, in.Version) {
				cr.Versions = append(cr.Versions, in.Version)
			}
		}
	}
	return out
}

// why is the instance's most important reason for its level, falling back to the action's reasons.
func why(in report.ScoredInstance) string {
	if in.Action.Why != "" {
		return in.Action.Why
	}
	for _, r := range in.Score.Reasons {
		if r.Level == in.Score.Level {
			return r.Text
		}
	}
	return ""
}

func sortedCriticals(m map[pair]*critical, keep func(pair) bool) []*critical {
	var out []*critical
	for k, c := range m {
		if keep == nil || keep(k) {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Cluster < out[j].Cluster
	})
	return out
}

// scope is a set of clusters (a tenant, one new cluster, or everything).
type scope map[string]bool

func (s scope) has(cluster string) bool { return s == nil || s[cluster] }

// compIn is a component restricted to a scope: its worst level there, where it runs, and the action
// for those instances only (a tenant's section must not give reasons that come from another tenant).
type compIn struct {
	C        *report.ComponentReport
	Level    report.Level
	Clusters []string
	InLast   bool // runs in the last stage (live)
	Action   report.Action
}

func componentsIn(rep *report.Report, sc scope) []compIn {
	last := ""
	if len(rep.Stages) > 0 {
		last = rep.Stages[len(rep.Stages)-1]
	}
	var out []compIn
	for i := range rep.Components {
		c := &rep.Components[i]
		ci := compIn{C: c, Level: report.LevelOK}
		seen := map[string]bool{}
		var insts []report.ScoredInstance
		for _, in := range c.Instances {
			if !sc.has(in.Cluster) {
				continue
			}
			insts = append(insts, in)
			if in.Score.Level.Rank() > ci.Level.Rank() {
				ci.Level = in.Score.Level
			}
			if !seen[in.Cluster] {
				seen[in.Cluster] = true
				ci.Clusters = append(ci.Clusters, in.Cluster)
			}
			if in.Stage == last {
				ci.InLast = true
			}
		}
		if len(insts) > 0 {
			sort.Strings(ci.Clusters)
			ci.Action = c.Action
			if sc != nil {
				ci.Action = report.ComponentAction(insts, c.Target)
			}
			out = append(out, ci)
		}
	}
	return out
}

// topActions orders like the Overview's "Top actions": Medium or worse, by level, then running in the
// last stage, then number of clusters affected.
func topActions(cs []compIn, n int, skip func(compIn) bool) []compIn {
	var out []compIn
	for _, c := range cs {
		if c.Level.Rank() >= report.LevelMedium.Rank() && (skip == nil || !skip(c)) {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Level.Rank() != b.Level.Rank() {
			return a.Level.Rank() > b.Level.Rank()
		}
		if a.InLast != b.InLast {
			return a.InLast
		}
		if len(a.Clusters) != len(b.Clusters) {
			return len(a.Clusters) > len(b.Clusters)
		}
		return a.C.Name < b.C.Name
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

var levelOrder = []report.Level{report.LevelCritical, report.LevelHigh, report.LevelMedium, report.LevelLow, report.LevelUnknown, report.LevelOK}

var levelNames = map[report.Level]string{report.LevelCritical: "Critical", report.LevelHigh: "High", report.LevelMedium: "Medium",
	report.LevelLow: "Low", report.LevelUnknown: "Unknown", report.LevelOK: "OK"}

func levelName(l report.Level) string {
	if n, ok := levelNames[l]; ok {
		return n
	}
	return string(l)
}

// levelCounts: "2 Critical · 3 High · 14 OK" (levels with no components left out).
func levelCounts(cs []compIn) string {
	n := map[report.Level]int{}
	for _, c := range cs {
		n[c.Level]++
	}
	var parts []string
	for _, l := range levelOrder {
		if n[l] > 0 {
			parts = append(parts, itoa(n[l])+" "+levelName(l))
		}
	}
	if len(parts) == 0 {
		return "no components identified"
	}
	return strings.Join(parts, " · ")
}

// levelOn is the component's worst level on one cluster ("" if it doesn't run there).
func levelOn(rep *report.Report, component, cluster string) report.Level {
	var lvl report.Level
	for _, c := range rep.Components {
		if c.ID != component {
			continue
		}
		for _, in := range c.Instances {
			if in.Cluster == cluster && (lvl == "" || in.Score.Level.Rank() > lvl.Rank()) {
				lvl = in.Score.Level
			}
		}
	}
	return lvl
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
