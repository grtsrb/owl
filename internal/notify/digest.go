package notify

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"owl/internal/catalog"
	"owl/internal/inventory"
	"owl/internal/mail"
	"owl/internal/report"
	"owl/internal/store"
	"owl/internal/summary"
)

const (
	digestWindow = 24 * time.Hour // a missed digest is sent if at most this late
	digestRetry  = time.Hour
	staleAfter   = 24 * time.Hour
	weekly       = 7 * 24 * time.Hour
	listCap      = 10
)

// digestDue: the slot is inside the catch-up window, after the digest was switched on, and not sent
// (or not tried within the last hour).
func (n *Notifier) digestDue(meta map[string]string, slot time.Time) bool {
	now := n.now()
	if now.Sub(slot) > digestWindow || now.Before(slot) {
		return false
	}
	if t := metaTime(meta[metaDigestEnabled]); t == nil || !slot.After(*t) {
		return false
	}
	if t := metaTime(meta[metaDigestSlot]); t != nil && !t.Before(slot) {
		if t := metaTime(meta[metaDigestMail]); t != nil && !t.Before(slot) || n.Mail == nil {
			return false
		}
	}
	if t := metaTime(meta[metaDigestTry]); t != nil && now.Sub(*t) < digestRetry && t.After(slot) {
		return false
	}
	return true
}

func (n *Notifier) digestIfDue(ctx context.Context) {
	set, err := n.settings(ctx)
	if err != nil || !set.Digest {
		return
	}
	st, err := n.Store.NotifyState(ctx)
	if err != nil {
		n.Log.Error("digest: state", "err", err)
		return
	}
	if st.Meta[metaDigestEnabled] == "" {
		// Switched on without going through the settings page (a restore): count from now.
		_ = n.DigestEnabled(ctx)
		return
	}
	slot := set.LastSlot(n.now())
	if !n.digestDue(st.Meta, slot) {
		return
	}
	n.sendDigest(ctx, slot, st.Meta)
}

// sendDigest sends the slot's digest to whatever hasn't had it yet: Mattermost (retried hourly in the
// window) and email (each recipient once; a failed address is logged, not retried, so the others never
// get it twice).
func (n *Notifier) sendDigest(ctx context.Context, slot time.Time, meta map[string]string) {
	n.digestMu.Lock()
	defer n.digestMu.Unlock()
	slotS := slot.UTC().Format(time.RFC3339)
	_ = n.Store.SetNotifyMeta(ctx, metaDigestTry, n.now().Format(time.RFC3339))
	d, err := n.BuildDigest(ctx)
	if err != nil {
		n.record(ctx, "digest", err)
		return
	}
	if t := metaTime(meta[metaDigestSlot]); n.Webhook != nil && (t == nil || t.Before(slot)) {
		if err := n.post(ctx, "digest", d.Markdown()); err == nil {
			_ = n.Store.SetNotifyMeta(ctx, metaDigestSlot, slotS)
		}
	} else if n.Webhook == nil {
		_ = n.Store.SetNotifyMeta(ctx, metaDigestSlot, slotS) // nothing to send there
	}
	if t := metaTime(meta[metaDigestMail]); n.Mail != nil && (t == nil || t.Before(slot)) {
		n.mailDigest(ctx, d)
		_ = n.Store.SetNotifyMeta(ctx, metaDigestMail, slotS)
	}
	if err := n.Store.PruneResolved(ctx, n.now().Add(-5*weekly)); err != nil {
		n.Log.Warn("prune resolved", "err", err)
	}
}

func (n *Notifier) mailDigest(ctx context.Context, d Doc) {
	users, err := n.Store.ListUsers(ctx)
	if err != nil {
		n.Log.Error("digest email: users", "err", err)
		return
	}
	text, html := d.Text(), d.HTML()
	sent := 0
	for _, u := range users {
		if !u.DigestEmail || !u.Active() {
			continue
		}
		if err := n.Mail.Send(ctx, mail.Message{To: u.Email, Subject: d.Title, Body: text, HTML: html}); err != nil {
			n.Log.Warn("digest email not delivered", "err", err) // address not logged
			n.record(ctx, "digest email", err)
			continue
		}
		sent++
	}
	if sent > 0 {
		n.Log.Info("digest emailed", "recipients", sent)
	}
}

// SendDigestNow posts the digest to Mattermost right away (Settings → Notifications). It doesn't count
// as the scheduled one and sends no email.
func (n *Notifier) SendDigestNow(ctx context.Context) error {
	if n.Webhook == nil {
		return errors.New("OWL_MATTERMOST_WEBHOOK_URL is not set")
	}
	n.digestMu.Lock()
	defer n.digestMu.Unlock()
	d, err := n.BuildDigest(ctx)
	if err != nil {
		return err
	}
	return n.post(ctx, "digest (manual)", d.Markdown())
}

// BuildDigest assembles the weekly digest from the current report, the last week's change history and
// the pairs that left Critical. Summary headlines come only from summaries already stored.
func (n *Notifier) BuildDigest(ctx context.Context) (Doc, error) {
	rep, err := n.Report(ctx)
	if err != nil {
		return Doc{}, err
	}
	clusters, err := n.Store.ListClusters(ctx)
	if err != nil {
		return Doc{}, err
	}
	cat := n.cat() // one snapshot for the whole digest
	now := n.now()
	since := now.Add(-weekly)
	changes, err := n.Store.ChangesSince(ctx, since)
	if err != nil {
		return Doc{}, err
	}
	resolved, err := n.Store.ListResolved(ctx, since)
	if err != nil {
		return Doc{}, err
	}
	ovs, err := n.Store.ListOverrides(ctx)
	if err != nil {
		return Doc{}, err
	}
	sums, err := n.Store.ListSummaries(ctx)
	if err != nil {
		return Doc{}, err
	}
	in := digestInputs{rep: rep, clusters: clusters, since: since, now: now, resolved: resolved,
		identify: inventory.Identifier(cat, ovs), cat: cat, headlines: headlines(sums)}
	in.changes = changes
	set, _ := n.settings(ctx)
	loc, err := loadTZ(set.DigestTZ)
	if err != nil {
		loc = time.UTC
	}
	return n.digestDoc(in, loc), nil
}

type digestInputs struct {
	rep       *report.Report
	clusters  []store.Cluster
	changes   []store.Change // last 7 days, newest first
	resolved  []store.ResolvedMark
	since     time.Time
	now       time.Time
	identify  func(image string) (catalog.Identification, string)
	cat       *catalog.Catalog
	headlines map[[3]string]string // component, from, to -> headline
}

func headlines(sums []store.Summary) map[[3]string]string {
	out := map[[3]string]string{}
	for _, s := range sums {
		var st summary.Stored
		if json.Unmarshal(s.Content, &st) == nil && st.Headline != "" {
			out[[3]string{s.Component, s.FromVersion, s.ToVersion}] = st.Headline
		}
	}
	return out
}

func (n *Notifier) digestDoc(in digestInputs, loc *time.Location) Doc {
	d := Doc{Title: "Owl weekly digest — " + in.now.In(loc).Format("Mon 2 Jan 2006")}
	tenants := map[string][]string{}
	for _, c := range in.clusters {
		tenants[c.Tenant] = append(tenants[c.Tenant], c.Name)
	}
	var names []string
	for t := range tenants {
		names = append(names, t)
	}
	sort.Strings(names)
	if len(in.clusters) == 0 {
		d.Intro = []Line{Ln(T("No clusters are registered yet."))}
		return d
	}
	if len(names) == 1 {
		d.Sections = []Section{n.digestSection("", nil, in)}
	} else {
		d.Intro = []Line{Ln(T(plural(len(in.clusters), "cluster") + " in " + itoa(len(names)) + " tenants."))}
		for _, t := range names {
			sc := scope{}
			for _, c := range tenants[t] {
				sc[c] = true
			}
			d.Sections = append(d.Sections, n.digestSection("Tenant "+t, sc, in))
		}
	}
	d.Footer = n.footer("Open the Overview", "/")
	return d
}

func (n *Notifier) digestSection(heading string, sc scope, in digestInputs) Section {
	comps := componentsIn(in.rep, sc)
	sec := Section{Heading: heading}
	sec.Blocks = append(sec.Blocks, Block{Title: "Components", Items: []Line{Ln(T(levelCounts(comps)))}})

	top := Block{Title: "Top actions", Empty: "Nothing above Low."}
	for _, ci := range topActions(comps, 5, nil) {
		h := n.headlineFor(ci, sc, in)
		top.Items = append(top.Items, n.actionLine(ci, &h, true))
	}
	sec.Blocks = append(sec.Blocks, top)

	drift := Block{Title: "Drift between stages"}
	var dl []Line
	for _, c := range in.rep.Components {
		for _, dr := range c.Drift {
			if dr.Kind == report.DriftOnlyIn || !allIn(sc, dr.Clusters) {
				continue
			}
			mark := ""
			if dr.Kind == report.DriftAnomaly {
				mark = "⚠ "
			}
			dl = append(dl, Ln(T(mark), B(c.Name), T(": "+dr.Text)))
		}
	}
	shown, more := capLines(len(dl), listCap)
	drift.Items, drift.More, drift.MoreLink = dl[:shown], more, n.link("/")
	sec.Blocks = append(sec.Blocks, drift)

	up, down := n.versionChanges(sc, in)
	shown, more = capLines(len(up), listCap)
	sec.Blocks = append(sec.Blocks, Block{Title: "Upgraded this week", Empty: "No version changes.", Items: up[:shown], More: more, MoreLink: n.link("/changes")})
	if len(down) > 0 {
		shown, more = capLines(len(down), listCap)
		sec.Blocks = append(sec.Blocks, Block{Title: "⚠ Rolled back this week", Items: down[:shown], More: more, MoreLink: n.link("/changes")})
	}

	var res []Line
	seen := map[pair]bool{}
	names := map[string]string{}
	for _, c := range in.rep.Components {
		names[c.ID] = c.Name
	}
	for i := len(in.resolved) - 1; i >= 0; i-- { // newest first
		r := in.resolved[i]
		k := pair{r.Component, r.Cluster}
		if seen[k] || !sc.has(r.Cluster) || !clusterExists(in.clusters, r.Cluster) {
			continue
		}
		if levelOn(in.rep, r.Component, r.Cluster) == report.LevelCritical {
			continue // Critical again since: it's not "no longer Critical"
		}
		seen[k] = true
		name := names[r.Component]
		if name == "" {
			if c := in.cat.ByID(r.Component); c != nil {
				name = c.Name
			} else {
				name = r.Component
			}
		}
		now := "no longer running"
		if l := levelOn(in.rep, r.Component, r.Cluster); l != "" {
			now = "now " + levelName(l)
		}
		res = append(res, Ln(B(name), T(" on "+r.Cluster+" — "+now)))
	}
	shown, more = capLines(len(res), listCap)
	sec.Blocks = append(sec.Blocks, Block{Title: "No longer Critical", Items: res[:shown], More: more})

	var stale []Line
	for _, c := range in.clusters {
		if !sc.has(c.Name) {
			continue
		}
		switch {
		case c.LastSeenAt == nil:
			stale = append(stale, Ln(B(c.Name), T(" — hasn't reported yet")))
		case in.now.Sub(*c.LastSeenAt) > staleAfter:
			stale = append(stale, Ln(B(c.Name), T(" — last report "+ago(in.now.Sub(*c.LastSeenAt)))))
		}
	}
	sec.Blocks = append(sec.Blocks, Block{Title: "Clusters not reporting", Items: stale})
	return sec
}

// headlineFor finds a stored summary for the component's jump to its target from a version running in
// the scope (never requests one).
func (n *Notifier) headlineFor(ci compIn, sc scope, in digestInputs) string {
	if ci.C.Target == nil {
		return ""
	}
	for _, inst := range ci.C.Instances {
		if !sc.has(inst.Cluster) {
			continue
		}
		if h := in.headlines[[3]string{ci.C.ID, inst.Version, ci.C.Target.Version}]; h != "" {
			return h
		}
	}
	return ""
}

// versionChanges turns the week's template image changes (and Kubernetes version changes) into
// "component old → new on cluster — now <level>" lines, upgrades and rollbacks apart. A workload that
// changed several times shows its first and last version of the week.
func (n *Notifier) versionChanges(sc scope, in digestInputs) (up, down []Line) {
	type key struct{ cluster, component, where string }
	type span struct {
		name, from, to string
		at             time.Time
	}
	spans := map[key]*span{}
	var order []key
	for i := len(in.changes) - 1; i >= 0; i-- { // oldest first
		ch := in.changes[i]
		if !sc.has(ch.Cluster) {
			continue
		}
		var comp, name, from, to string
		switch ch.Type {
		case store.ChangeImageChanged:
			a, _ := in.identify(ch.OldValue)
			b, _ := in.identify(ch.NewValue)
			if a.Component == nil || b.Component == nil || a.Component.ID != b.Component.ID || a.Version == nil || b.Version == nil {
				continue
			}
			comp, name, from, to = b.Component.ID, b.Component.Name, a.Version.String(), b.Version.String()
		case store.ChangeK8sVersion:
			ids := in.cat.ClusterVersions(ch.NewValue)
			old := in.cat.ClusterVersions(ch.OldValue)
			if len(ids) == 0 || len(old) == 0 || ids[0].Component == nil || ids[0].Version == nil || old[0].Version == nil {
				continue
			}
			comp, name, from, to = ids[0].Component.ID, ids[0].Component.Name, old[0].Version.String(), ids[0].Version.String()
		default:
			continue
		}
		k := key{ch.Cluster, comp, ch.Namespace + "/" + ch.Workload + "/" + ch.Container}
		if s := spans[k]; s != nil {
			s.to, s.at = to, ch.At
			continue
		}
		spans[k] = &span{name: name, from: from, to: to, at: ch.At}
		order = append(order, k)
	}
	seen := map[string]bool{}
	for i := len(order) - 1; i >= 0; i-- { // newest first
		k := order[i]
		s := spans[k]
		if s.from == s.to {
			continue
		}
		text := s.name + " " + s.from + " → " + s.to + " on " + k.cluster
		if seen[text] {
			continue // several workloads of one component changed the same way
		}
		seen[text] = true
		l := Ln(B(s.name), T(" "), C(s.from), T(" → "), C(s.to), T(" on "+k.cluster))
		if lvl := levelOn(in.rep, k.component, k.cluster); lvl != "" {
			l = append(l, T(" — now "+levelName(lvl)))
		}
		a, okA := catalog.ParseVersion(s.from, "")
		b, okB := catalog.ParseVersion(s.to, "")
		if okA && okB && b.Compare(a) < 0 {
			down = append(down, l)
		} else {
			up = append(up, l)
		}
	}
	return up, down
}

func allIn(sc scope, clusters []string) bool {
	for _, c := range clusters {
		if !sc.has(c) {
			return false
		}
	}
	return true
}

func clusterExists(cs []store.Cluster, name string) bool {
	for _, c := range cs {
		if c.Name == name {
			return true
		}
	}
	return false
}

func ago(d time.Duration) string {
	switch {
	case d < 48*time.Hour:
		return itoa(int(d.Hours())) + " hours ago"
	default:
		return itoa(int(d.Hours()/24)) + " days ago"
	}
}
