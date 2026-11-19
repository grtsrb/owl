package notify

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"owl/internal/catalog"
	"owl/internal/inventory"
	"owl/internal/mail"
	"owl/internal/report"
	"owl/internal/store"
)

// Meta keys in notify_meta.
const (
	metaInitialized   = "initialized" // the first check ran (it only records, never posts)
	metaLastSentAt    = "lastSentAt"  // any message delivered
	metaLastSentKind  = "lastSentKind"
	metaLastErrorAt   = "lastErrorAt"
	metaLastError     = "lastError"
	metaDigestSlot    = "digestSlot"     // scheduled time of the last digest sent to Mattermost (RFC 3339)
	metaDigestMail    = "digestMailSlot" // … and by email
	metaDigestTry     = "digestTriedAt"  // last attempt (retry hourly)
	metaDigestEnabled = "digestEnabledAt"
	metaCatalog       = "catalogBuiltins" // built-in component ids the last check knew (comma separated)
	metaLearning      = "catalogLearning" // id@RFC3339 since: running components a catalog upgrade taught Owl, upstream data pending
)

// learnFor bounds how long a newly recognized component waits quietly for complete upstream data (an
// upstream that keeps failing must not silence it forever).
const learnFor = 24 * time.Hour

// builtinsBeforeM13 is the built-in catalog of the releases that didn't record metaCatalog yet: an
// upgrade from one of them learns everything else quietly too.
var builtinsBeforeM13 = []string{"kubernetes", "rke2", "k3s", "ingress-nginx", "nginx", "haproxy", "haproxy-kubernetes-ingress",
	"haproxy-ingress", "grafana", "coredns", "longhorn", "redis", "valkey", "postgresql", "redpanda", "metrics-server"}

// Notifier checks for changes worth an alert after every agent push and upstream refresh (Kick), and
// sends the weekly digest on schedule (Run). Without a webhook it still keeps its state current, so
// switching a notification on later starts quietly from the present.
type Notifier struct {
	Store   store.Store
	Catalog *catalog.Catalog
	// CatalogFn, when set, gives the current catalog (built-in + components added in the UI).
	CatalogFn func() *catalog.Catalog
	Report    func(ctx context.Context) (*report.Report, error)
	// Ready reports whether upstream data has loaded: before that, levels built on it would look
	// resolved and alert again once it arrives, so checks wait.
	Ready func() bool
	// Incomplete reports whether a component's last upstream refresh had errors (rate limit, outage).
	// Its levels may be lower than they really are, so its Critical pairs aren't counted as resolved
	// until a complete refresh — otherwise they'd be announced as "newly Critical" once it recovers.
	Incomplete func(componentID string) bool
	// Fetched reports whether upstream data for a component has loaded. Components a catalog upgrade
	// taught Owl stay quiet until it has, completely (see Incomplete), or for learnFor at most
	// (nil = always loaded).
	Fetched   func(componentID string) bool
	Webhook   *Webhook    // nil = OWL_MATTERMOST_WEBHOOK_URL not set
	Mail      mail.Sender // nil = no SMTP: no digest email
	PublicURL *url.URL    // links; nil = messages without links
	Log       *slog.Logger
	Now       func() time.Time

	mu       sync.Mutex // one check at a time (and a restore with its quiet check)
	digestMu sync.Mutex // one digest at a time
	kick     chan struct{}
	kickOne  sync.Once
	// quietNext: a quiet check was asked for before upstream data loaded; the first real check is quiet.
	quietNext bool
}

func (n *Notifier) cat() *catalog.Catalog {
	if n.CatalogFn != nil {
		return n.CatalogFn()
	}
	return n.Catalog
}

func (n *Notifier) now() time.Time {
	if n.Now != nil {
		return n.Now().UTC()
	}
	return time.Now().UTC()
}

func (n *Notifier) kicks() chan struct{} {
	n.kickOne.Do(func() { n.kick = make(chan struct{}, 1) })
	return n.kick
}

// Kick asks the loop to check soon (after a push, an upstream refresh or a settings change).
func (n *Notifier) Kick() {
	select {
	case n.kicks() <- struct{}{}:
	default:
	}
}

// Run checks on every kick and looks at the digest schedule every minute until ctx is done.
func (n *Notifier) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-n.kicks():
			if err := n.Check(ctx, false); err != nil && ctx.Err() == nil {
				n.Log.Error("notification check failed", "err", err)
			}
		case <-t.C:
			n.digestIfDue(ctx)
		}
	}
}

func (n *Notifier) settings(ctx context.Context) (Settings, error) {
	m, err := n.Store.GetSettings(ctx)
	if err != nil {
		return Settings{}, err
	}
	return ParseSettings(m), nil
}

func (n *Notifier) link(path string, query ...string) string {
	if n.PublicURL == nil {
		return ""
	}
	u := *n.PublicURL
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = ""
	if len(query) == 2 {
		u.RawQuery = url.Values{query[0]: {query[1]}}.Encode()
	}
	u.Fragment = ""
	return u.String()
}

// message is one post a check decided to send.
type message struct{ kind, md string }

// Check compares the current report with what notifications last saw, records the difference and posts
// what's switched on. quiet records without posting (the first check ever, and after a restore). Posting
// happens after the lock is released, so a slow or unreachable Mattermost never holds up the next check
// or a restore.
func (n *Notifier) Check(ctx context.Context, quiet bool) error {
	n.mu.Lock()
	msgs, err := n.check(ctx, quiet)
	n.mu.Unlock()
	for _, m := range msgs {
		_ = n.post(ctx, m.kind, m.md) // recorded; a failed alert is dropped (the digest covers it)
	}
	return err
}

// WhileQuiet runs fn (a restore) with checks held off, then records the resulting state without posting,
// before any other check can see it — an old backup must not cause an alert storm.
func (n *Notifier) WhileQuiet(ctx context.Context, fn func() error) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := fn(); err != nil {
		return err
	}
	if _, err := n.check(ctx, true); err != nil {
		n.Log.Error("notification state after restore", "err", err)
	}
	return nil
}

func (n *Notifier) check(ctx context.Context, quiet bool) ([]message, error) {
	if n.Ready != nil && !n.Ready() {
		n.quietNext = n.quietNext || quiet
		return nil, nil
	}
	quiet, n.quietNext = quiet || n.quietNext, false
	st, err := n.Store.NotifyState(ctx)
	if err != nil {
		return nil, err
	}
	set, err := n.settings(ctx)
	if err != nil {
		return nil, err
	}
	// Which components have complete upstream data — decided before the report is built, so a fetch that
	// lands in between can't mark a component learned against a report without its data.
	complete := map[string]bool{}
	for _, c := range n.cat().Components {
		complete[c.ID] = (n.Fetched == nil || n.Fetched(c.ID)) && (n.Incomplete == nil || !n.Incomplete(c.ID))
	}
	rep, err := n.Report(ctx)
	if err != nil {
		return nil, err
	}
	inv := rep.Inventory
	if inv == nil { // report built without its inventory (tests)
		if inv, err = inventory.Build(ctx, n.Store, n.cat()); err != nil {
			return nil, err
		}
	}
	clusters, err := n.Store.ListClusters(ctx)
	if err != nil {
		return nil, err
	}
	if st.Meta[metaInitialized] == "" {
		quiet = true
	}
	now := n.now()
	delta := store.NotifyDelta{At: now, Meta: map[string]string{metaInitialized: "1"}}

	// Clusters: announce each once, on its first report.
	exists := map[string]bool{}
	currentKeys := map[string]bool{}
	var fresh []store.Cluster
	for _, c := range clusters {
		exists[c.Name] = true
		k := store.ClusterKey(c.Name, c.CreatedAt)
		currentKeys[k] = true
		if c.LastSeenAt != nil && !st.Clusters[k] {
			fresh = append(fresh, c)
			delta.Clusters = append(delta.Clusters, k)
		}
	}
	for k := range st.Clusters {
		if !currentKeys[k] {
			delta.DropClusters = append(delta.DropClusters, k)
		}
	}
	sort.Slice(fresh, func(i, j int) bool { return fresh[i].Name < fresh[j].Name })

	// Components a newer catalog recognizes that already ran (unrecognized before): recorded quietly
	// once their upstream data is in, then one summary instead of an alert per component.
	learning, learned, err := n.catalogLearned(ctx, st, inv, complete, &delta)
	if err != nil {
		return nil, err
	}

	// Critical pairs: new ones alert, ones that left are recorded for the digest, pairs of deleted
	// clusters are forgotten silently.
	crit := criticalPairs(rep)
	known := map[pair]bool{}
	for _, m := range st.Critical {
		k := pair{m.Component, m.Cluster}
		known[k] = true
		switch {
		case !exists[m.Cluster] || n.cat().ByID(m.Component) == nil:
			// cluster deleted, or the component left the catalog (a custom one deleted or replaced by a
			// built-in, which takes over its pairs): not "no longer Critical"
			delta.Dropped = append(delta.Dropped, m)
		case crit[k] != nil || n.Incomplete != nil && n.Incomplete(m.Component):
			// still Critical, or can't tell (incomplete upstream data): keep it
		case quiet:
			delta.Dropped = append(delta.Dropped, m)
		default:
			delta.Resolved = append(delta.Resolved, m)
		}
	}
	var newCrit []*critical
	learnedCrit := map[string]bool{}
	for _, c := range sortedCriticals(crit, func(k pair) bool { return !known[k] }) {
		switch {
		case learning[c.Component]:
			continue // no upstream data yet: not recorded, so it's decided once the data is in
		case learned[c.Component]:
			learnedCrit[c.Component] = true // recorded, announced in the summary
		default:
			newCrit = append(newCrit, c)
		}
		delta.AddCritical = append(delta.AddCritical, store.CriticalMark{Component: c.Component, Cluster: c.Cluster, Since: now})
	}

	// Unrecognized repositories: alert the first time any cluster runs one.
	unrec := unrecognizedByRepo(inv)
	var newRepos []string
	for repo := range unrec {
		if !st.Repos[repo] {
			newRepos = append(newRepos, repo)
		}
	}
	sort.Strings(newRepos)
	delta.Repos = newRepos

	if err := n.Store.ApplyNotifyDelta(ctx, delta); err != nil {
		return nil, err
	}
	if quiet || n.Webhook == nil {
		return nil, nil
	}
	var msgs []message

	// A new cluster gets one combined message, which replaces the Critical and unrecognized alerts for it.
	covered := map[string]bool{}
	if set.NewCluster {
		for _, c := range fresh {
			covered[c.Name] = true
			msgs = append(msgs, message{"new cluster", n.newClusterDoc(c, rep, crit, unrec, st.Repos).Markdown()})
		}
	}
	if set.Critical {
		var list []*critical
		for _, c := range newCrit {
			if !covered[c.Cluster] {
				list = append(list, c)
			}
		}
		if len(list) > 0 {
			msgs = append(msgs, message{"critical alert", n.criticalDoc(list).Markdown()})
		}
	}
	if len(learned) > 0 && (set.Critical || set.Unrecognized) {
		msgs = append(msgs, message{"catalog update", n.learnedDoc(learned, learnedCrit).Markdown()})
	}
	if set.Unrecognized {
		var list []string
		for _, repo := range newRepos {
			for _, cl := range unrec[repo] {
				if !covered[cl] {
					list = append(list, repo)
					break
				}
			}
		}
		if len(list) > 0 {
			msgs = append(msgs, message{"unrecognized alert", n.unrecognizedDoc(list, unrec, covered).Markdown()})
		}
	}
	return msgs, nil
}

// catalogLearned compares the built-in catalog with the one the last check knew. Components that are new
// in it and already run somewhere were unrecognized images until now: they're "learning" until their
// upstream data is complete (or for learnFor), then "learned" in exactly one check. Components deployed
// after the upgrade aren't affected (they weren't running when the catalog changed), and neither is a
// built-in that replaces a custom component of the same id (Owl tracked it already). Updates delta's meta.
func (n *Notifier) catalogLearned(ctx context.Context, st *store.NotifyState, inv *inventory.Inventory, complete map[string]bool,
	delta *store.NotifyDelta) (learning, learned map[string]bool, err error) {
	cat := n.cat()
	var builtins []string
	for _, c := range cat.Components {
		if !c.Custom {
			builtins = append(builtins, c.ID)
		}
	}
	sort.Strings(builtins)
	knew := map[string]bool{}
	switch {
	case st.Meta[metaCatalog] != "":
		for _, id := range strings.Split(st.Meta[metaCatalog], ",") {
			knew[id] = true
		}
	case st.Meta[metaInitialized] == "": // first check ever: quiet anyway, nothing to learn
		for _, id := range builtins {
			knew[id] = true
		}
	default:
		for _, id := range builtinsBeforeM13 {
			knew[id] = true
		}
	}
	custom, err := n.Store.ListCustomComponents(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, c := range custom {
		knew[c.ID] = true
	}
	running := map[string]bool{}
	for _, cv := range inv.Components {
		running[cv.ID] = true
	}
	now := n.now()
	since := map[string]time.Time{}
	if st.Meta[metaLearning] != "" {
		for _, e := range strings.Split(st.Meta[metaLearning], ",") {
			id, at, _ := strings.Cut(e, "@")
			t, err := time.Parse(time.RFC3339, at)
			if err != nil {
				t = now
			}
			since[id] = t
		}
	}
	for _, id := range builtins {
		if _, ok := since[id]; !ok && !knew[id] && running[id] {
			since[id] = now
		}
	}
	learning, learned = map[string]bool{}, map[string]bool{}
	var still []string
	for id, t := range since {
		switch {
		case !running[id] || cat.ByID(id) == nil:
			// removed meanwhile: nothing to announce
		case complete[id] || now.Sub(t) >= learnFor:
			learned[id] = true
		default:
			learning[id] = true
			still = append(still, id+"@"+t.Format(time.RFC3339))
		}
	}
	sort.Strings(still)
	delta.Meta[metaCatalog] = strings.Join(builtins, ",")
	delta.Meta[metaLearning] = strings.Join(still, ",")
	return learning, learned, nil
}

// learnedDoc: "Owl now recognizes 3 more components" after a catalog upgrade, with the Critical ones.
func (n *Notifier) learnedDoc(learned, critical map[string]bool) Doc {
	cat := n.cat()
	name := func(id string) string {
		if c := cat.ByID(id); c != nil {
			return c.Name
		}
		return id
	}
	ids := make([]string, 0, len(learned))
	for id := range learned {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if critical[ids[i]] != critical[ids[j]] {
			return critical[ids[i]]
		}
		return name(ids[i]) < name(ids[j])
	})
	title := "Owl now recognizes " + report.Plural(len(ids), "more component")
	if len(critical) > 0 {
		title += "; " + strconv.Itoa(len(critical)) + " Critical"
	}
	b := Block{Title: "Recognized after the catalog update"}
	shown, more := capLines(len(ids), 15)
	for _, id := range ids[:shown] {
		l := Ln(B(name(id)))
		if critical[id] {
			l = append(l, T(" — "), B("Critical"))
		}
		if link := n.link("/components/" + url.PathEscape(id)); link != "" {
			l = append(l, T(" · "), L("details", link))
		}
		b.Items = append(b.Items, l)
	}
	b.More, b.MoreLink = more, n.link("/")
	return Doc{Title: "🦉 " + title, Sections: []Section{{Blocks: []Block{b}}}, Footer: n.footer("Open the Overview", "/")}
}

// unrecognizedByRepo: repository -> clusters running it (sorted).
func unrecognizedByRepo(inv *inventory.Inventory) map[string][]string {
	out := map[string][]string{}
	for _, u := range inv.Unrecognized {
		for _, in := range u.Instances {
			if !slices.Contains(out[u.Repository], in.Cluster) {
				out[u.Repository] = append(out[u.Repository], in.Cluster)
			}
		}
		sort.Strings(out[u.Repository])
	}
	return out
}

// post delivers one Mattermost message (with retries) and records the outcome. A failed alert is
// dropped: the digest covers it.
func (n *Notifier) post(ctx context.Context, kind, md string) error {
	if n.Webhook == nil {
		return errors.New("OWL_MATTERMOST_WEBHOOK_URL is not set")
	}
	more := "…cut to fit Mattermost's message size."
	if l := n.link("/"); l != "" {
		more = "…cut to fit Mattermost's message size — [open Owl](" + l + ")"
	}
	err := n.Webhook.Post(ctx, Fit(md, more))
	n.record(ctx, kind, err)
	return err
}

func (n *Notifier) record(ctx context.Context, kind string, err error) {
	now := n.now().Format(time.RFC3339)
	m := map[string]string{}
	if err != nil {
		n.Log.Warn("notification not delivered", "kind", kind, "err", err)
		m[metaLastErrorAt], m[metaLastError] = now, kind+": "+err.Error()
	} else {
		n.Log.Info("notification sent", "kind", kind)
		m[metaLastSentAt], m[metaLastSentKind] = now, kind
	}
	if e := n.Store.ApplyNotifyDelta(context.WithoutCancel(ctx), store.NotifyDelta{Meta: m}); e != nil {
		n.Log.Error("notification status not saved", "err", e)
	}
}

func (n *Notifier) footer(text, path string, query ...string) []Line {
	if l := n.link(path, query...); l != "" {
		return []Line{Ln(L(text, l))}
	}
	return nil
}

func (n *Notifier) criticalDoc(list []*critical) Doc {
	// Count components and clusters separately: one component on two clusters isn't "2 components".
	comps, clusters := map[string]bool{}, map[string]bool{}
	for _, c := range list {
		comps[c.Component], clusters[c.Cluster] = true, true
	}
	title := report.Plural(len(comps), "component") + " became Critical"
	if len(comps) == 1 {
		title = list[0].Name + " became Critical"
	}
	if len(clusters) > 1 {
		title += " on " + report.Plural(len(clusters), "cluster")
	}
	b := Block{Title: "Newly Critical"}
	shown, more := capLines(len(list), 10)
	for _, c := range list[:shown] {
		b.Items = append(b.Items, n.criticalLine(c, true))
	}
	b.More, b.MoreLink = more, n.link("/")
	return Doc{Title: "🔴 " + title, Sections: []Section{{Blocks: []Block{b}}}, Footer: n.footer("Open the Overview", "/")}
}

func (n *Notifier) criticalLine(c *critical, withCluster bool) Line {
	l := Ln(B(c.Name))
	if withCluster {
		l = append(l, T(" on "), B(c.Cluster))
		if c.Stage != "" {
			l = append(l, D(" ("+c.Stage+")"))
		}
	}
	if len(c.Versions) > 0 {
		l = append(l, T(" — "), C(strings.Join(c.Versions, ", ")))
	}
	if c.Why != "" {
		l = append(l, T(" · "+c.Why))
	}
	if c.Action != "" {
		l = append(l, T(" · "), B(c.Action))
	}
	if link := n.link("/components/" + url.PathEscape(c.Component)); link != "" {
		l = append(l, T(" · "), L("details", link))
	}
	return l
}

func (n *Notifier) unrecognizedDoc(repos []string, where map[string][]string, covered map[string]bool) Doc {
	b := Block{Title: "Not in Owl's catalog"}
	shown, more := capLines(len(repos), 10)
	for _, repo := range repos[:shown] {
		var cls []string
		for _, c := range where[repo] {
			if !covered[c] {
				cls = append(cls, c)
			}
		}
		l := Ln(C(repo), T(" on "))
		for i, c := range cls {
			if i > 0 {
				l = append(l, T(", "))
			}
			if link := n.link("/clusters/"+url.PathEscape(c), "show", "unrecognized"); link != "" {
				l = append(l, L(c, link))
			} else {
				l = append(l, B(c))
			}
		}
		if link := n.link("/settings/catalog", "new", repo); link != "" {
			l = append(l, T(" · "), L("add as component", link))
		}
		b.Items = append(b.Items, l)
	}
	b.More, b.MoreLink = more, n.link("/clusters")
	title := "New unrecognized image"
	if len(repos) != 1 {
		title = strconv.Itoa(len(repos)) + " new unrecognized images"
	}
	return Doc{Title: title, Intro: []Line{Ln(T("Owl can't score these yet. Add one as a component, or on the cluster's page map it to an existing one, mark it in-house, or ignore it."))},
		Sections: []Section{{Blocks: []Block{b}}}}
}

func (n *Notifier) newClusterDoc(c store.Cluster, rep *report.Report, crit map[pair]*critical, unrec map[string][]string, knownRepos map[string]bool) Doc {
	sc := scope{c.Name: true}
	comps := componentsIn(rep, sc)
	d := Doc{Title: "New cluster connected: " + c.Name}
	d.Intro = append(d.Intro, Ln(T("Stage "), B(string(c.Stage)), T(" · tenant "), B(c.Tenant), T(" · first report received")))
	for _, ci := range comps {
		if c := n.cat().ByID(ci.C.ID); c == nil || c.Source != "cluster" {
			continue // only the cluster's own versions (kubernetes, rke2, k3s) here
		}
		for _, in := range ci.C.Instances {
			if in.Cluster == c.Name {
				l := Ln(T(ci.C.Name+" "), C(in.Version), T(" — "), B(levelName(in.Score.Level)))
				if in.Score.Level != report.LevelOK && in.Action.Why != "" {
					l = append(l, T(" · "+in.Action.Why))
				}
				d.Intro = append(d.Intro, l)
				break
			}
		}
	}
	d.Intro = append(d.Intro, Ln(T("Components: "+levelCounts(comps))))

	mine := sortedCriticals(crit, func(k pair) bool { return k.Cluster == c.Name })
	cb := Block{Title: "Critical", Empty: "Nothing Critical."}
	shown, more := capLines(len(mine), 10)
	for _, x := range mine[:shown] {
		cb.Items = append(cb.Items, n.criticalLine(x, false))
	}
	cb.More = more

	ab := Block{Title: "Next actions"}
	for _, ci := range topActions(comps, 3, func(ci compIn) bool { return ci.Level == report.LevelCritical }) {
		ab.Items = append(ab.Items, n.actionLine(ci, nil, false))
	}

	var repos []string
	for repo, cls := range unrec {
		if slices.Contains(cls, c.Name) {
			repos = append(repos, repo)
		}
	}
	sort.Strings(repos)
	ub := Block{Title: "Unrecognized images"}
	shown, more = capLines(len(repos), 10)
	for _, repo := range repos[:shown] {
		var others []string
		for _, o := range unrec[repo] {
			if o != c.Name {
				others = append(others, o)
			}
		}
		l := Ln(C(repo), T(" — "))
		switch {
		case len(others) > 0:
			l = append(l, D("also on "+strings.Join(others, ", ")+", not decided yet"))
		case knownRepos[repo]:
			l = append(l, D("seen before, not decided yet"))
		default:
			l = append(l, B("new to Owl"))
		}
		ub.Items = append(ub.Items, l)
	}
	ub.More, ub.MoreLink = more, n.link("/clusters/"+url.PathEscape(c.Name), "show", "unrecognized")
	if len(repos) > 0 {
		if link := n.link("/clusters/"+url.PathEscape(c.Name), "show", "unrecognized"); link != "" {
			ub.Items = append(ub.Items, Ln(L("Map, mark in-house or ignore them", link)))
		}
	}
	d.Sections = []Section{{Blocks: []Block{cb, ab, ub}}}
	d.Footer = n.footer("Open "+c.Name, "/clusters/"+url.PathEscape(c.Name))
	return d
}

// actionLine: "**CoreDNS** — High · Update to 1.11.3 — fixes 2 high advisories · runs in 3 clusters".
func (n *Notifier) actionLine(ci compIn, headline *string, clusters bool) Line {
	l := Ln(B(ci.C.Name), T(" — "+levelName(ci.Level)+" · "), B(ci.Action.Text))
	if ci.Action.Why != "" {
		l = append(l, T(" — "+ci.Action.Why))
	}
	if clusters {
		where := "runs in " + report.Plural(len(ci.Clusters), "cluster")
		if ci.InLast {
			where += ", incl. the last stage"
		}
		l = append(l, D(" · "+where))
	}
	if headline != nil && *headline != "" {
		l = append(l, D(" · Summary: "), T(*headline))
	}
	if link := n.link("/components/" + url.PathEscape(ci.C.ID)); link != "" {
		l = append(l, T(" · "), L("details", link))
	}
	return l
}

// SendTest posts a short message so an admin can see the webhook works.
func (n *Notifier) SendTest(ctx context.Context) error {
	d := Doc{Title: "Owl test message", Intro: []Line{Ln(T("Notifications from Owl reach this channel."))},
		Footer: n.footer("Open Owl", "/")}
	return n.post(ctx, "test", d.Markdown())
}

// Status is what Settings → Notifications shows (never the webhook URL).
type Status struct {
	WebhookConfigured bool       `json:"webhookConfigured"`
	MailConfigured    bool       `json:"mailConfigured"`
	LinksConfigured   bool       `json:"linksConfigured"` // OWL_PUBLIC_URL set
	LastSentAt        *time.Time `json:"lastSentAt,omitempty"`
	LastSentKind      string     `json:"lastSentKind,omitempty"`
	LastErrorAt       *time.Time `json:"lastErrorAt,omitempty"`
	LastError         string     `json:"lastError,omitempty"`
	LastDigest        *time.Time `json:"lastDigest,omitempty"` // scheduled slot of the last digest sent
	NextDigest        *time.Time `json:"nextDigest,omitempty"`
	EmailRecipients   int        `json:"emailRecipients"`
	Tracking          bool       `json:"tracking"` // the first check has run
}

func (n *Notifier) Status(ctx context.Context) (Status, error) {
	st, err := n.Store.NotifyState(ctx)
	if err != nil {
		return Status{}, err
	}
	set, err := n.settings(ctx)
	if err != nil {
		return Status{}, err
	}
	out := Status{WebhookConfigured: n.Webhook != nil, MailConfigured: n.Mail != nil, LinksConfigured: n.PublicURL != nil,
		LastSentKind: st.Meta[metaLastSentKind], LastError: st.Meta[metaLastError], Tracking: st.Meta[metaInitialized] != ""}
	out.LastSentAt, out.LastErrorAt, out.LastDigest = metaTime(st.Meta[metaLastSentAt]), metaTime(st.Meta[metaLastErrorAt]), metaTime(st.Meta[metaDigestSlot])
	if out.LastErrorAt != nil && out.LastSentAt != nil && out.LastSentAt.After(*out.LastErrorAt) {
		out.LastError, out.LastErrorAt = "", nil // delivered since: the error is history (it's in the log)
	}
	if set.Digest {
		next := set.NextSlot(n.now())
		if last := set.LastSlot(n.now()); n.digestDue(st.Meta, last) {
			next = last // due now (will go out within a minute) or being retried
		}
		out.NextDigest = &next
	}
	if users, err := n.Store.ListUsers(ctx); err == nil {
		for _, u := range users {
			if u.DigestEmail && u.Active() {
				out.EmailRecipients++
			}
		}
	}
	return out, nil
}

// DigestEnabled records when the digest was switched on: a slot before that is never sent (switching it
// on on Monday at 10:00 doesn't send the 09:00 digest at once).
func (n *Notifier) DigestEnabled(ctx context.Context) error {
	return n.Store.SetNotifyMeta(ctx, metaDigestEnabled, n.now().Format(time.RFC3339))
}

func metaTime(v string) *time.Time {
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return nil
	}
	return &t
}
