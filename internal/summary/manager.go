// Package summary writes Claude upgrade summaries for version jumps (milestone 8). See DESIGN.md
// "Claude". Cost safety is the point of most of this file: every request is priced exactly before it's
// sent and refused if its worst case would exceed the remaining daily or monthly budget.
package summary

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"owl/internal/catalog"
	"owl/internal/policy"
	"owl/internal/report"
	"owl/internal/store"
	"owl/internal/upstream"
)

type Mode string

const (
	ModeOff      Mode = "off"      // nothing fetched, no requests (default)
	ModeEstimate Mode = "estimate" // notes fetched, local estimate shown, no requests to Claude
	ModeManual   Mode = "manual"   // an admin clicks Generate
	ModeAuto     Mode = "auto"     // missing summaries are written in the background
)

// Settings keys (store.SetSetting; edited in Settings → Claude summaries).
const (
	KeyMode       = "summaryMode"
	KeyDailyCap   = "summaryDailyCapMicros"
	KeyMonthlyCap = "summaryMonthlyCapMicros"
	KeyMaxInput   = "summaryMaxInputTokens"
	KeyMaxOutput  = "summaryMaxOutputTokens"
	KeyPaused     = "summaryPaused" // non-empty = paused, with the reason
)

type Settings struct {
	Mode             Mode  `json:"mode"`
	DailyCapMicros   int64 `json:"dailyCapMicros"`
	MonthlyCapMicros int64 `json:"monthlyCapMicros"`
	MaxInputTokens   int64 `json:"maxInputTokens"`
	MaxOutputTokens  int64 `json:"maxOutputTokens"`
}

var Defaults = Settings{Mode: ModeOff, DailyCapMicros: 5_000_000, MonthlyCapMicros: 20_000_000, MaxInputTokens: 30_000, MaxOutputTokens: 4_000}

// Limits for the editable settings. HardMaxInput also bounds "Generate anyway".
const (
	MaxCapMicros  = 1_000_000_000 // $1000
	MinMaxInput   = 2_000
	HardMaxInput  = 200_000
	MinMaxOutput  = 1_000
	HardMaxOutput = 16_000
)

func (s Settings) Validate() error {
	switch {
	case s.Mode != ModeOff && s.Mode != ModeEstimate && s.Mode != ModeManual && s.Mode != ModeAuto:
		return fmt.Errorf("mode: off, estimate, manual or auto")
	case s.DailyCapMicros < 10_000 || s.DailyCapMicros > MaxCapMicros, s.MonthlyCapMicros < 10_000 || s.MonthlyCapMicros > MaxCapMicros:
		return fmt.Errorf("caps: $0.01–$1000")
	case s.DailyCapMicros > s.MonthlyCapMicros:
		return fmt.Errorf("the daily cap can't be above the monthly cap")
	case s.MaxInputTokens < MinMaxInput || s.MaxInputTokens > HardMaxInput:
		return fmt.Errorf("max input tokens: %d–%d", MinMaxInput, HardMaxInput)
	case s.MaxOutputTokens < MinMaxOutput || s.MaxOutputTokens > HardMaxOutput:
		return fmt.Errorf("max output tokens: %d–%d", MinMaxOutput, HardMaxOutput)
	}
	return nil
}

func (s Settings) Values() map[string]string {
	return map[string]string{KeyMode: string(s.Mode), KeyDailyCap: strconv.FormatInt(s.DailyCapMicros, 10),
		KeyMonthlyCap: strconv.FormatInt(s.MonthlyCapMicros, 10), KeyMaxInput: strconv.FormatInt(s.MaxInputTokens, 10),
		KeyMaxOutput: strconv.FormatInt(s.MaxOutputTokens, 10)}
}

// ParseSettings falls back to the defaults for anything missing or invalid (a bad value never widens
// a limit: each invalid field takes its default).
func ParseSettings(m map[string]string) Settings {
	s := Defaults
	if v := Mode(m[KeyMode]); v == ModeEstimate || v == ModeManual || v == ModeAuto {
		s.Mode = v
	}
	num := func(key string, lo, hi int64, dst *int64) {
		if n, err := strconv.ParseInt(m[key], 10, 64); err == nil && n >= lo && n <= hi {
			*dst = n
		}
	}
	num(KeyDailyCap, 10_000, MaxCapMicros, &s.DailyCapMicros)
	num(KeyMonthlyCap, 10_000, MaxCapMicros, &s.MonthlyCapMicros)
	num(KeyMaxInput, MinMaxInput, HardMaxInput, &s.MaxInputTokens)
	num(KeyMaxOutput, MinMaxOutput, HardMaxOutput, &s.MaxOutputTokens)
	if s.DailyCapMicros > s.MonthlyCapMicros {
		s.DailyCapMicros = s.MonthlyCapMicros
	}
	return s
}

// Jump is one upgrade: component + exact running version → exact target version. It carries no
// cluster information by construction.
type Jump struct {
	Component string `json:"component"`
	From      string `json:"from"`
	To        string `json:"to"`
}

func (j Jump) key() string { return j.Component + "\x00" + j.From + "\x00" + j.To }

// Notes is what Manager needs from internal/upstream (a fake in tests).
type Notes interface {
	ReleaseNotes(ctx context.Context, comp *catalog.Component, to catalog.Version, lines []string,
		include func(catalog.Version) bool) ([]upstream.Note, error)
}

type Manager struct {
	Store   store.Store
	Catalog *catalog.Catalog
	// CatalogFn, when set, gives the current catalog (built-in + components added in the UI).
	CatalogFn func() *catalog.Catalog
	Notes     Notes                          // nil = upstream disabled: nothing to summarize
	Infos     func(id string) *upstream.Info // current upstream info per component
	Report    func(ctx context.Context) (*report.Report, error)
	Claude    Claude // nil = no ANTHROPIC_API_KEY
	Model     string
	Log       *slog.Logger
	Now       func() time.Time

	mu     sync.Mutex
	jumps  map[string]*jumpState
	order  []string // jump keys, most urgent component first
	gen    sync.Mutex
	kickCh chan struct{}
	once   sync.Once
}

type jumpState struct {
	Jump
	name      string
	collected bool
	notes     []upstream.Note
	notesErr  string
	noNotes   bool
	tokens    int64 // exact (counted) or estimated input tokens
	exact     bool
	failedAt  time.Time
	lastErr   string
	// collectedAt: when the notes were last read (re-read after notesMaxAge).
	collectedAt time.Time
}

func (m *Manager) cat() *catalog.Catalog {
	if m.CatalogFn != nil {
		return m.CatalogFn()
	}
	return m.Catalog
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now().UTC()
	}
	return time.Now().UTC()
}

func (m *Manager) settings(ctx context.Context) (Settings, string) {
	vals, err := m.Store.GetSettings(ctx)
	if err != nil {
		return Settings{Mode: ModeOff}, "" // can't read the settings: behave as off
	}
	return ParseSettings(vals), vals[KeyPaused]
}

// Kick asks the background loop to refresh soon (after an upstream refresh or a settings change).
func (m *Manager) Kick() {
	m.once.Do(func() { m.kickCh = make(chan struct{}, 1) })
	select {
	case m.kickCh <- struct{}{}:
	default:
	}
}

// RunScheduler refreshes the jump list (and, in auto mode, writes missing summaries) on every kick and
// at least hourly.
func (m *Manager) RunScheduler(ctx context.Context) {
	m.once.Do(func() { m.kickCh = make(chan struct{}, 1) })
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		m.cycle(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-m.kickCh:
		}
	}
}

func (m *Manager) cycle(ctx context.Context) {
	if err := m.Refresh(ctx); err != nil {
		m.Log.Warn("summaries: refresh failed", "err", err)
		return
	}
	if set, _ := m.settings(ctx); set.Mode == ModeAuto {
		m.runAuto(ctx)
	}
}

// Refresh recomputes the jumps from the report and, unless summaries are off, collects their notes.
func (m *Manager) Refresh(ctx context.Context) error {
	rep, err := m.Report(ctx)
	if err != nil {
		return err
	}
	set, _ := m.settings(ctx)
	var order []string
	found := map[string]*jumpState{}
	for _, c := range rep.Components {
		if c.Target == nil || c.Target.Version == "" {
			continue
		}
		tv, ok := catalog.ParseVersion(c.Target.Version, "")
		if !ok {
			continue
		}
		var keys []string
		for _, in := range c.Instances {
			if in.Status != string(catalog.StatusIdentified) || in.Version == "" {
				continue // unpinned, in-house, …: no exact starting version
			}
			fv, ok := catalog.ParseVersion(in.Version, "")
			if !ok || fv.Compare(tv) >= 0 {
				continue // at or ahead of the target
			}
			j := Jump{Component: c.ID, From: in.Version, To: c.Target.Version}
			if _, dup := found[j.key()]; !dup {
				found[j.key()] = &jumpState{Jump: j, name: c.Name}
				keys = append(keys, j.key())
			}
		}
		sort.Slice(keys, func(a, b int) bool { // by version, not text (1.9.0 before 1.10.0)
			va, _ := catalog.ParseVersion(found[keys[a]].From, "")
			vb, _ := catalog.ParseVersion(found[keys[b]].From, "")
			return va.Compare(vb) < 0
		})
		order = append(order, keys...)
	}
	m.mu.Lock()
	for k := range found {
		// Keep the same state object for jumps that still exist: a Generate running right now holds
		// it, and what it records (failure time, exact token count) must not land on a discarded copy.
		if old := m.jumps[k]; old != nil {
			found[k] = old
		}
	}
	m.jumps, m.order = found, order
	m.mu.Unlock()
	if set.Mode == ModeOff || m.Notes == nil {
		return nil
	}
	for _, k := range order {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		m.mu.Lock()
		st := m.jumps[k]
		fresh := st != nil && st.collected && m.now().Sub(st.collectedAt) < notesMaxAge
		m.mu.Unlock()
		if st != nil && !fresh {
			m.collect(ctx, st)
		}
	}
	return nil
}

// notesMaxAge: collected notes are re-read after this (a release body may be filled in after
// publishing, a branch's notes file updated later). The fetcher's cache makes re-reading cheap.
const notesMaxAge = 6 * time.Hour

// collect fetches a jump's release notes (cached by the upstream fetcher) and estimates its tokens.
func (m *Manager) collect(ctx context.Context, st *jumpState) {
	notes, err := m.notesFor(ctx, st.Jump)
	m.mu.Lock()
	defer m.mu.Unlock()
	st.collected, st.collectedAt = true, m.now()
	st.notes, st.notesErr, st.noNotes = nil, "", false
	switch {
	case errors.Is(err, upstream.ErrNoNotes):
		st.noNotes = true
	case err != nil:
		st.notesErr = err.Error()
		st.collected = false // try again next cycle
	default:
		st.notes = notes
		chars := 0
		for _, n := range notes {
			chars += len(n.Text) + len(n.Version) + 40
		}
		st.tokens, st.exact = estimateTokens(chars), false
	}
}

// notesFor selects which releases belong to a jump (internal/policy decides what a line is) and asks
// upstream for their notes. Same line: every release after From up to To. Across lines: the first
// release of each line after From's, and every release of To's line up to To.
func (m *Manager) notesFor(ctx context.Context, j Jump) ([]upstream.Note, error) {
	comp := m.cat().ByID(j.Component)
	info := m.Infos(j.Component)
	if comp == nil || info == nil {
		return nil, fmt.Errorf("upstream data not loaded yet")
	}
	from, ok1 := catalog.ParseVersion(j.From, "")
	to, ok2 := catalog.ParseVersion(j.To, "")
	if !ok1 || !ok2 {
		return nil, fmt.Errorf("unparseable versions")
	}
	ls := policy.UsableLines(info.Lines)
	fi, _ := policy.Position(from, ls)
	ti, _ := policy.Position(to, ls)
	var lines []string
	for i := ti; i <= fi && i < len(ls); i++ {
		lines = append(lines, ls[i].Name)
	}
	include := func(v catalog.Version) bool {
		if v.Compare(from) <= 0 || v.Compare(to) > 0 {
			return false
		}
		if fi == ti {
			return true
		}
		vi, exact := policy.Position(v, ls)
		switch {
		case vi == ti:
			return true
		case vi == fi && exact:
			return false // patches of the old line: backports of what the newer lines have
		case !exact:
			return true // line unknown to us: rather include than miss something
		}
		// An intermediate line: only its first release (x.y.0, or x.0 for major-only lines).
		lv, _ := catalog.ParseVersion(ls[vi].Name, "")
		if lv.Parts == 1 {
			return v.Minor == 0 && v.Patch == 0
		}
		return v.Patch == 0
	}
	return m.Notes.ReleaseNotes(ctx, comp, to, lines, include)
}

// Errors from Generate (the server maps them to statuses).
var (
	ErrUnknownJump  = errors.New("no such upgrade in the current report")
	ErrNotAllowed   = errors.New("summaries are off or estimate-only — switch to manual or automatic in Settings → Claude summaries")
	ErrNoKey        = errors.New("ANTHROPIC_API_KEY is not set on the server")
	ErrPaused       = errors.New("summaries are paused after repeated failures — check the last error and click Resume")
	ErrTooLarge     = errors.New("the release notes are larger than the input limit")
	ErrBudget       = errors.New("this request could exceed the remaining daily or monthly budget")
	ErrNoNotes      = errors.New("no release notes available for this upgrade")
	ErrUnknownPrice = errors.New("unknown price for the configured model: set OWL_CLAUDE_MODEL to a listed model")
	ErrNotReady     = errors.New("release notes are still being collected — try again in a moment")
	// ErrNotesUnavailable: fetching the release notes failed (nothing was sent to Claude).
	ErrNotesUnavailable = errors.New("release notes couldn't be fetched")
	// ErrRequestFailed: a request was made (and logged, and its cost booked) but produced no summary.
	ErrRequestFailed = errors.New("summary request failed")
)

// Generate writes (or, with an existing summary, rewrites) the summary of one jump now. force allows a
// jump above the input limit (still bounded by HardMaxInput and the caps).
func (m *Manager) Generate(ctx context.Context, j Jump, force bool) error {
	lookup := func() *jumpState {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.jumps[j.key()]
	}
	st := lookup()
	if st == nil {
		// The list may be behind the clusters (it's recomputed on kicks): check the current report.
		if err := m.Refresh(ctx); err != nil {
			return err
		}
		if st = lookup(); st == nil {
			return ErrUnknownJump
		}
	}
	m.mu.Lock()
	fresh := st.collected && m.now().Sub(st.collectedAt) < notesMaxAge
	m.mu.Unlock()
	if set, _ := m.settings(ctx); !fresh && set.Mode != ModeOff {
		m.collect(ctx, st)
	}
	return m.generate(ctx, st, force)
}

// generate runs one request. Everything that decides whether money may be spent (mode, pause, caps,
// spend so far) is read after taking the lock, so a request waiting behind another sees its outcome.
func (m *Manager) generate(ctx context.Context, st *jumpState, force bool) error {
	m.gen.Lock() // one request at a time: the budget check below is only sound when serialized
	defer m.gen.Unlock()
	set, paused := m.settings(ctx)
	switch {
	case set.Mode != ModeManual && set.Mode != ModeAuto:
		return ErrNotAllowed
	case m.Claude == nil:
		return ErrNoKey
	case paused != "":
		return ErrPaused
	}
	if _, ok := priceFor(m.Model, 0); !ok {
		return ErrUnknownPrice
	}
	m.mu.Lock()
	notes, noNotes, notesErr, collected := st.notes, st.noNotes, st.notesErr, st.collected
	m.mu.Unlock()
	switch {
	case noNotes:
		return ErrNoNotes
	case notesErr != "":
		return fmt.Errorf("%w: %s", ErrNotesUnavailable, notesErr)
	case !collected:
		return ErrNotReady
	}
	comp := m.cat().ByID(st.Component)
	if comp == nil {
		return ErrNotReady // a custom component deleted since the jumps were computed
	}
	// A name typed in the UI could be internal: for custom components Claude only gets the public
	// GitHub repository as the name.
	name := comp.Name
	if comp.Custom {
		name = comp.GitHub
	}
	req := Request{Model: m.Model, Fallback: fallbacks[m.Model], System: systemPrompt,
		User: userMessage(name, comp.GitHub, st.Jump, notes), MaxTokens: set.MaxOutputTokens, Schema: Schema()}

	in, err := m.Claude.CountTokens(ctx, req) // free
	if err != nil {
		return m.fail(ctx, st, req, 0, 0, 0, "token count failed: "+err.Error())
	}
	m.mu.Lock()
	st.tokens, st.exact = in, true
	m.mu.Unlock()
	if in > HardMaxInput || (in > set.MaxInputTokens && !force) {
		return fmt.Errorf("%w (%d tokens; limit %d)", ErrTooLarge, in, set.MaxInputTokens)
	}
	worst, _ := worstCase(m.Model, in, set.MaxOutputTokens)
	now := m.now()
	day, err1 := m.Store.SummarySpend(ctx, time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC))
	month, err2 := m.Store.SummarySpend(ctx, time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC))
	if err := errors.Join(err1, err2); err != nil {
		return err // can't tell what was spent: don't spend more
	}
	if day+worst > set.DailyCapMicros || month+worst > set.MonthlyCapMicros {
		return fmt.Errorf("%w (up to %s; spent today %s of %s, this month %s of %s)", ErrBudget,
			USD(worst), USD(day), USD(set.DailyCapMicros), USD(month), USD(set.MonthlyCapMicros))
	}

	m.Log.Info("summary request", "component", st.Component, "from", st.From, "to", st.To, "model", m.Model,
		"inputTokens", in, "worstCase", USD(worst))
	resp, err := m.Claude.Send(ctx, req)
	if err != nil {
		cost := int64(0)
		msg := err.Error()
		if errors.Is(err, ErrTransport) {
			cost, msg = worst, msg+" (cost unknown: booked at the worst case)"
		}
		return m.fail(ctx, st, req, in, 0, cost, msg)
	}
	cost := actualCost(resp.Attempts)
	var inTok, outTok int64
	for _, a := range resp.Attempts {
		inTok, outTok = inTok+a.InputTokens, outTok+a.OutputTokens
	}
	switch resp.StopReason {
	case "refusal":
		return m.fail(ctx, st, req, inTok, outTok, cost, "Claude declined to summarize these release notes")
	case "max_tokens":
		return m.fail(ctx, st, req, inTok, outTok, cost, "the output limit was reached before the summary was complete — raise max output tokens")
	}
	content, err := parseContent(resp.Text, notes)
	if err != nil {
		return m.fail(ctx, st, req, inTok, outTok, cost, err.Error())
	}
	sources := make([]upstream.Note, len(notes))
	for i, n := range notes {
		sources[i] = upstream.Note{Version: n.Version, URL: n.URL}
	}
	body, _ := json.Marshal(Stored{Content: content, Sources: sources})
	if err := m.Store.PutSummary(ctx, store.Summary{Component: st.Component, FromVersion: st.From, ToVersion: st.To,
		Model: m.Model, PromptVersion: PromptVersion, CreatedAt: now, Content: body}); err != nil {
		_ = m.record(ctx, st.Jump, req.Model, inTok, outTok, cost, "failed", "saving the summary failed: "+err.Error())
		return err
	}
	m.mu.Lock()
	st.failedAt, st.lastErr = time.Time{}, ""
	m.mu.Unlock()
	m.Log.Info("summary written", "component", st.Component, "from", st.From, "to", st.To, "cost", USD(cost))
	return m.record(ctx, st.Jump, req.Model, inTok, outTok, cost, "ok", "")
}

func (m *Manager) record(ctx context.Context, j Jump, model string, in, out, cost int64, result, msg string) error {
	return m.Store.AddSummaryRequest(context.WithoutCancel(ctx), store.SummaryRequest{At: m.now(), Component: j.Component,
		FromVersion: j.From, ToVersion: j.To, Model: model, InputTokens: in, OutputTokens: out, CostMicros: cost,
		Result: result, Error: msg})
}

// failuresToPause in a row pause summaries until an admin resumes.
const failuresToPause = 3

func (m *Manager) fail(ctx context.Context, st *jumpState, req Request, in, out, cost int64, msg string) error {
	m.Log.Warn("summary failed", "component", st.Component, "from", st.From, "to", st.To, "err", msg, "cost", USD(cost))
	ctx = context.WithoutCancel(ctx)
	if err := m.record(ctx, st.Jump, req.Model, in, out, cost, "failed", msg); err != nil {
		m.Log.Error("summary request log write failed", "err", err)
	}
	m.mu.Lock()
	st.failedAt, st.lastErr = m.now(), msg
	m.mu.Unlock()
	if recent, err := m.Store.ListSummaryRequests(ctx, failuresToPause); err == nil && len(recent) == failuresToPause {
		all := true
		for _, r := range recent {
			all = all && r.Result == "failed"
		}
		if all {
			reason := fmt.Sprintf("%d failed requests in a row; last: %s", failuresToPause, msg)
			if err := m.Store.SetSetting(ctx, KeyPaused, reason); err != nil {
				m.Log.Error("pausing summaries failed", "err", err)
			}
			m.Log.Warn("summaries paused", "reason", reason)
		}
	}
	return fmt.Errorf("%w: %s", ErrRequestFailed, msg)
}

// Resume clears a pause; the logged "resumed" entry restarts the failure count.
func (m *Manager) Resume(ctx context.Context) error {
	if err := m.Store.SetSetting(ctx, KeyPaused, ""); err != nil {
		return err
	}
	m.mu.Lock()
	for _, st := range m.jumps {
		st.failedAt = time.Time{} // an admin looked at it: allow a retry today
	}
	m.mu.Unlock()
	m.Kick()
	return m.Store.AddSummaryRequest(ctx, store.SummaryRequest{At: m.now(), Model: m.Model, Result: "resumed"})
}

// runAuto writes missing summaries, most urgent component first, until one can't be afforded or
// summaries pause. A failed jump waits until the next UTC day.
func (m *Manager) runAuto(ctx context.Context) {
	have, err := m.existing(ctx)
	if err != nil {
		return
	}
	m.mu.Lock()
	order := append([]string(nil), m.order...)
	m.mu.Unlock()
	for _, k := range order {
		if ctx.Err() != nil {
			return
		}
		set, paused := m.settings(ctx) // re-read: an admin may switch modes or pause meanwhile
		if set.Mode != ModeAuto || paused != "" {
			return
		}
		m.mu.Lock()
		st := m.jumps[k]
		skip := st == nil || have[k] || !st.collected || st.noNotes || st.notesErr != "" ||
			(!st.failedAt.IsZero() && sameDay(st.failedAt, m.now())) ||
			(st.tokens > set.MaxInputTokens && st.exact)
		m.mu.Unlock()
		if skip {
			continue
		}
		err := m.generate(ctx, st, false)
		switch {
		case err == nil, errors.Is(err, ErrTooLarge), errors.Is(err, ErrNoNotes):
		case errors.Is(err, ErrBudget):
			m.Log.Info("summaries: waiting for budget", "err", err)
			return
		case errors.Is(err, ErrPaused), errors.Is(err, ErrNoKey), errors.Is(err, ErrUnknownPrice):
			return
		}
	}
}

func sameDay(a, b time.Time) bool {
	a, b = a.UTC(), b.UTC()
	return a.Year() == b.Year() && a.YearDay() == b.YearDay()
}

func (m *Manager) existing(ctx context.Context) (map[string]bool, error) {
	all, err := m.Store.ListSummaries(ctx)
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, s := range all {
		have[Jump{s.Component, s.FromVersion, s.ToVersion}.key()] = true
	}
	return have, nil
}

// USD formats micro-dollars for messages: $0.0123 below a cent, else cents.
func USD(micros int64) string {
	if micros > 0 && micros < 10_000 {
		return fmt.Sprintf("$%.4f", float64(micros)/1e6)
	}
	return fmt.Sprintf("$%.2f", float64(micros)/1e6)
}

// --- views for the API ---

// States of a jump in the UI.
const (
	StateReady      = "ready"      // summary exists
	StateOff        = "off"        // summaries are off
	StateCollecting = "collecting" // notes not fetched yet
	StateNoNotes    = "no_notes"
	StateError      = "error"    // notes couldn't be fetched
	StateEstimate   = "estimate" // estimate-only mode
	StateNoKey      = "no_key"
	StatePaused     = "paused"
	StateTooLarge   = "too_large"
	StateFailed     = "failed"    // failed today; retried tomorrow (auto) or on click
	StateBudget     = "budget"    // its worst case doesn't fit the remaining budget today
	StateAvailable  = "available" // manual mode: an admin can generate it
	StateQueued     = "queued"    // auto mode: will be written
)

type JumpView struct {
	Jump
	Name    string `json:"name"`
	State   string `json:"state"`
	Tokens  int64  `json:"tokens,omitempty"`
	Exact   bool   `json:"exact,omitempty"` // tokens counted by the API (else a local estimate)
	MaxCost int64  `json:"maxCostMicros,omitempty"`
	// Releases whose notes would be (or were) used.
	Sources []upstream.Note `json:"sources,omitempty"`
	Error   string          `json:"error,omitempty"`
	Summary *SummaryView    `json:"summary,omitempty"`
}

type SummaryView struct {
	Stored
	Model         string    `json:"model"`
	PromptVersion int       `json:"promptVersion"`
	CreatedAt     time.Time `json:"createdAt"`
}

// Overview is the public part: no spending, no settings beyond the mode.
type Overview struct {
	Mode    Mode       `json:"mode"`
	Enabled bool       `json:"enabled"` // an API key is configured
	Paused  bool       `json:"paused"`
	Jumps   []JumpView `json:"jumps"`
}

func (m *Manager) List(ctx context.Context) (Overview, error) {
	set, paused := m.settings(ctx)
	all, err := m.Store.ListSummaries(ctx)
	if err != nil {
		return Overview{}, err
	}
	saved := map[string]store.Summary{}
	for _, s := range all {
		saved[Jump{s.Component, s.FromVersion, s.ToVersion}.key()] = s
	}
	var remaining int64 = -1
	if set.Mode == ModeManual || set.Mode == ModeAuto {
		now := m.now()
		day, err1 := m.Store.SummarySpend(ctx, time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC))
		month, err2 := m.Store.SummarySpend(ctx, time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC))
		if err1 == nil && err2 == nil {
			remaining = min(set.DailyCapMicros-day, set.MonthlyCapMicros-month)
		}
	}
	out := Overview{Mode: set.Mode, Enabled: m.Claude != nil, Paused: paused != "", Jumps: []JumpView{}}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range m.order {
		st := m.jumps[k]
		// The public view never carries raw API/store error text (admins see it in the request log).
		v := JumpView{Jump: st.Jump, Name: st.name, Tokens: st.tokens, Exact: st.exact, Error: st.notesErr}
		if st.lastErr != "" {
			v.Error = "The last attempt failed — details in Settings → Claude summaries (admin)."
		}
		for _, n := range st.notes {
			v.Sources = append(v.Sources, upstream.Note{Version: n.Version, URL: n.URL})
		}
		if st.collected && !st.noNotes {
			v.MaxCost, _ = worstCase(m.Model, st.tokens, set.MaxOutputTokens)
		}
		if s, ok := saved[k]; ok {
			var stored Stored
			if json.Unmarshal(s.Content, &stored) == nil {
				v.Summary = &SummaryView{Stored: stored, Model: s.Model, PromptVersion: s.PromptVersion, CreatedAt: s.CreatedAt}
				v.Sources = stored.Sources
			}
		}
		v.State = m.state(st, v, set, paused, remaining)
		out.Jumps = append(out.Jumps, v)
	}
	return out, nil
}

func (m *Manager) state(st *jumpState, v JumpView, set Settings, paused string, remaining int64) string {
	switch {
	case v.Summary != nil:
		return StateReady
	case set.Mode == ModeOff:
		return StateOff
	case st.notesErr != "":
		return StateError
	case !st.collected:
		return StateCollecting
	case st.noNotes:
		return StateNoNotes
	case set.Mode == ModeEstimate:
		return StateEstimate
	case m.Claude == nil:
		return StateNoKey
	case paused != "":
		return StatePaused
	case st.tokens > set.MaxInputTokens && (st.exact || st.tokens > set.MaxInputTokens*2):
		return StateTooLarge
	case !st.failedAt.IsZero() && sameDay(st.failedAt, m.now()):
		return StateFailed
	case remaining >= 0 && v.MaxCost > remaining:
		return StateBudget
	case set.Mode == ModeManual:
		return StateAvailable
	}
	return StateQueued
}

// Status is the admin view: settings, spending and the request log.
type Status struct {
	Settings         Settings               `json:"settings"`
	Enabled          bool                   `json:"enabled"`
	Model            string                 `json:"model"`
	Fallback         string                 `json:"fallback,omitempty"`
	Price            *Price                 `json:"price,omitempty"` // nil = unknown model: refuses to run
	Paused           string                 `json:"paused,omitempty"`
	SpentTodayMicros int64                  `json:"spentTodayMicros"`
	SpentMonthMicros int64                  `json:"spentMonthMicros"`
	SpentTotalMicros int64                  `json:"spentTotalMicros"`
	Summaries        int                    `json:"summaries"`
	Requests         []store.SummaryRequest `json:"requests"`
	// MaxPerSummaryMicros is the most one summary can cost with the saved limits (incl. fallback).
	MaxPerSummaryMicros int64 `json:"maxPerSummaryMicros"`
}

func (m *Manager) Status(ctx context.Context) (Status, error) {
	set, paused := m.settings(ctx)
	now := m.now()
	st := Status{Settings: set, Enabled: m.Claude != nil, Model: m.Model, Fallback: fallbacks[m.Model], Paused: paused}
	if p, ok := priceFor(m.Model, 0); ok {
		st.Price = &p
	}
	st.MaxPerSummaryMicros, _ = worstCase(m.Model, set.MaxInputTokens, set.MaxOutputTokens)
	var errs []error
	var err error
	st.SpentTodayMicros, err = m.Store.SummarySpend(ctx, time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC))
	errs = append(errs, err)
	st.SpentMonthMicros, err = m.Store.SummarySpend(ctx, time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC))
	errs = append(errs, err)
	st.SpentTotalMicros, err = m.Store.SummarySpend(ctx, time.Time{})
	errs = append(errs, err)
	all, err := m.Store.ListSummaries(ctx)
	errs = append(errs, err)
	st.Summaries = len(all)
	st.Requests, err = m.Store.ListSummaryRequests(ctx, 20)
	errs = append(errs, err)
	return st, errors.Join(errs...)
}

// ModelLabel is how the UI names a model id ("claude-opus-5-5" → "Claude Opus 5.5").
func ModelLabel(id string) string {
	parts := strings.Split(strings.TrimPrefix(id, "claude-"), "-")
	if len(parts) == 0 || parts[0] == "" {
		return id
	}
	name := strings.ToUpper(parts[0][:1]) + parts[0][1:]
	return "Claude " + name + " " + strings.Join(parts[1:], ".")
}

// Forget drops what's known in memory about a component's jumps (notes, failures): a custom component
// was edited to point at another project, or deleted. Stored summaries are removed by the caller.
func (m *Manager) Forget(component string) {
	m.mu.Lock()
	for k, st := range m.jumps {
		if st.Component == component {
			delete(m.jumps, k)
		}
	}
	keep := m.order[:0]
	for _, k := range m.order {
		if _, ok := m.jumps[k]; ok {
			keep = append(keep, k)
		}
	}
	m.order = keep
	m.mu.Unlock()
	m.Kick()
}
