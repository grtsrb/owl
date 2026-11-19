package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"owl/internal/report"
	"owl/internal/summary"
	"owl/internal/upstream"
)

func (s *Server) summaryRoutes(mux *http.ServeMux) {
	// Public like the rest of the read API: summaries contain only public data (component, versions,
	// what upstream's notes say). Spending and settings are admin-only.
	mux.HandleFunc("GET /api/v1/summaries", s.listSummaries)
	mux.HandleFunc("GET /api/v1/summaries/status", s.requireAdmin(s.summaryStatus))
	mux.HandleFunc("PUT /api/v1/summaries/settings", s.requireAdmin(s.putSummarySettings))
	mux.HandleFunc("POST /api/v1/summaries/generate", s.requireAdmin(s.generateSummary))
	mux.HandleFunc("POST /api/v1/summaries/resume", s.requireAdmin(s.resumeSummaries))
}

// BuildReport is the scored report (also what summaries derive their jumps from).
func (s *Server) BuildReport(ctx context.Context) (*report.Report, error) {
	return report.Build(ctx, report.Inputs{
		Catalog: s.cat(), Store: s.Store, Infos: s.upstreamInfo, Now: time.Now().UTC(),
	})
}

// UpstreamInfo exposes the current upstream info per component (for the summary manager).
func (s *Server) UpstreamInfo(id string) *upstream.Info { return s.upstreamInfo(id) }

func (s *Server) summariesOff(w http.ResponseWriter) bool {
	if s.Summaries == nil {
		httpError(w, http.StatusServiceUnavailable, "summaries are not available on this server (upstream checks disabled)")
		return true
	}
	return false
}

func (s *Server) listSummaries(w http.ResponseWriter, r *http.Request) {
	if s.Summaries == nil {
		writeJSON(w, http.StatusOK, summary.Overview{Mode: summary.ModeOff, Jumps: []summary.JumpView{}})
		return
	}
	ov, err := s.Summaries.List(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ov)
}

func (s *Server) summaryStatus(w http.ResponseWriter, r *http.Request) {
	if s.summariesOff(w) {
		return
	}
	st, err := s.Summaries.Status(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) putSummarySettings(w http.ResponseWriter, r *http.Request) {
	if s.summariesOff(w) {
		return
	}
	var set summary.Settings
	if !decodeJSON(w, r, &set) {
		return
	}
	if err := set.Validate(); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.saveSettings(r.Context(), set.Values()); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "summary settings changed", "mode", set.Mode, "dailyCap", summary.USD(set.DailyCapMicros),
		"monthlyCap", summary.USD(set.MonthlyCapMicros), "maxInputTokens", set.MaxInputTokens, "maxOutputTokens", set.MaxOutputTokens)
	s.Summaries.Kick()
	s.summaryStatus(w, r)
}

type generateBody struct {
	Component string `json:"component"`
	From      string `json:"from"`
	To        string `json:"to"`
	Force     bool   `json:"force"` // "Generate anyway" for jumps above the input limit
}

// generateSummary runs one request synchronously (the UI shows a spinner); it can take a minute, so
// this response gets a longer write deadline than the server default.
func (s *Server) generateSummary(w http.ResponseWriter, r *http.Request) {
	if s.summariesOff(w) {
		return
	}
	var b generateBody
	if !decodeJSON(w, r, &b) {
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(5 * time.Minute))
	j := summary.Jump{Component: b.Component, From: b.From, To: b.To}
	err := s.Summaries.Generate(context.WithoutCancel(r.Context()), j, b.Force)
	if err != nil {
		var code int
		switch {
		case errors.Is(err, summary.ErrRequestFailed):
			code = http.StatusBadGateway // a request was made and failed (logged, cost booked)
			s.audit(r, "summary failed", "component", j.Component, "from", j.From, "to", j.To, "force", b.Force)
		case errors.Is(err, summary.ErrUnknownJump):
			code = http.StatusNotFound
		case errors.Is(err, summary.ErrNoKey), errors.Is(err, summary.ErrUnknownPrice):
			code = http.StatusServiceUnavailable
		case errors.Is(err, summary.ErrNotAllowed), errors.Is(err, summary.ErrPaused), errors.Is(err, summary.ErrTooLarge),
			errors.Is(err, summary.ErrBudget), errors.Is(err, summary.ErrNoNotes), errors.Is(err, summary.ErrNotReady),
			errors.Is(err, summary.ErrNotesUnavailable):
			code = http.StatusConflict // nothing was sent
		default:
			s.fail(w, err) // internal (store) error: logged, generic message to the client
			return
		}
		httpError(w, code, err.Error())
		return
	}
	s.audit(r, "summary generated", "component", j.Component, "from", j.From, "to", j.To, "force", b.Force)
	s.listSummaries(w, r)
}

func (s *Server) resumeSummaries(w http.ResponseWriter, r *http.Request) {
	if s.summariesOff(w) {
		return
	}
	if err := s.Summaries.Resume(r.Context()); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "summaries resumed")
	s.summaryStatus(w, r)
}
