package store

import (
	"sort"

	"service-inventory/internal/snapshot"
)

// RowsFromSnapshot flattens a snapshot into one row per workload container.
func RowsFromSnapshot(s *snapshot.Snapshot) []ImageRow {
	var rows []ImageRow
	for _, w := range s.Workloads {
		for _, c := range w.Containers {
			row := ImageRow{
				Namespace: w.Namespace, Kind: w.Kind, Workload: w.Name, Container: c.Name,
				Image: c.Image, Init: c.Init, DesiredReplicas: w.DesiredReplicas,
			}
			for _, r := range w.Running {
				if r.Container == c.Name {
					row.Running = append(row.Running, r)
				}
			}
			rows = append(rows, row)
		}
	}
	return rows
}

type rowKey struct{ ns, kind, workload, container string }

func keyOf(r ImageRow) rowKey { return rowKey{r.Namespace, r.Kind, r.Workload, r.Container} }

// Diff compares the intended images of two states. Running-pod details and replica counts are
// deliberately ignored: they fluctuate (scaling, restarts) and would flood the history.
func Diff(old, new []ImageRow) []Change {
	before := map[rowKey]ImageRow{}
	for _, r := range old {
		before[keyOf(r)] = r
	}
	after := map[rowKey]ImageRow{}
	for _, r := range new {
		after[keyOf(r)] = r
	}

	var out []Change
	mk := func(t ChangeType, k rowKey, o, n string) Change {
		return Change{Type: t, Namespace: k.ns, Kind: k.kind, Workload: k.workload, Container: k.container, OldValue: o, NewValue: n}
	}
	for k, n := range after {
		o, ok := before[k]
		switch {
		case !ok:
			out = append(out, mk(ChangeAdded, k, "", n.Image))
		case o.Image != n.Image:
			out = append(out, mk(ChangeImageChanged, k, o.Image, n.Image))
		}
	}
	for k, o := range before {
		if _, ok := after[k]; !ok {
			out = append(out, mk(ChangeRemoved, k, o.Image, ""))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Workload != b.Workload {
			return a.Workload < b.Workload
		}
		if a.Container != b.Container {
			return a.Container < b.Container
		}
		return a.Type < b.Type
	})
	return out
}
