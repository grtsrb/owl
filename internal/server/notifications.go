package server

import (
	"context"
	"net/http"

	"owl/internal/notify"
)

func (s *Server) notifyRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/notifications", s.requireAdmin(s.getNotifications))
	mux.HandleFunc("PUT /api/v1/notifications", s.requireAdmin(s.putNotifications))
	mux.HandleFunc("POST /api/v1/notifications/test", s.requireAdmin(s.testNotification))
	mux.HandleFunc("POST /api/v1/notifications/digest", s.requireAdmin(s.digestNow))
	// Any signed-in user: their own digest-by-email opt-in.
	mux.HandleFunc("PUT /api/v1/auth/digest-email", s.setDigestEmail)
}

// kickNotify asks notifications to look for changes (after a push, an upstream refresh or an admin change
// that can change levels: policies, overrides, clusters).
func (s *Server) kickSummaries() {
	if s.Summaries != nil {
		s.Summaries.Kick()
	}
}

func (s *Server) kickNotify() {
	if s.Notify != nil {
		s.Notify.Kick()
	}
}

// UpstreamLoaded: the first upstream refresh finished (or upstream checks are off).
func (s *Server) UpstreamLoaded() bool {
	if s.Upstream == nil {
		return true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.upstream != nil
}

// UpstreamIncomplete: the component's last upstream refresh had errors (its levels may be too low).
func (s *Server) UpstreamIncomplete(id string) bool {
	info := s.upstreamInfo(id)
	return info != nil && len(info.Errors) > 0
}

func (s *Server) notifyOff(w http.ResponseWriter) bool {
	if s.Notify == nil {
		httpError(w, http.StatusServiceUnavailable, "notifications are not available on this server")
		return true
	}
	return false
}

func (s *Server) getNotifications(w http.ResponseWriter, r *http.Request) {
	if s.notifyOff(w) {
		return
	}
	m, err := s.Store.GetSettings(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	st, err := s.Notify.Status(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": notify.ParseSettings(m), "status": st})
}

func (s *Server) putNotifications(w http.ResponseWriter, r *http.Request) {
	if s.notifyOff(w) {
		return
	}
	var in notify.Settings
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := in.Normalize(); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	m, err := s.Store.GetSettings(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	before := notify.ParseSettings(m)
	if err := s.saveSettings(r.Context(), in.Values()); err != nil {
		s.fail(w, err)
		return
	}
	if in.Digest && !before.Digest {
		if err := s.Notify.DigestEnabled(r.Context()); err != nil {
			s.fail(w, err)
			return
		}
	}
	s.audit(r, "notification settings changed", "digest", in.Digest, "critical", in.Critical, "unrecognized", in.Unrecognized,
		"newCluster", in.NewCluster, "digestDay", in.DigestDay, "digestTime", in.DigestTime, "digestTimeZone", in.DigestTZ)
	s.getNotifications(w, r)
}

func (s *Server) testNotification(w http.ResponseWriter, r *http.Request) {
	s.sendNow(w, r, "notification test sent", func(ctx context.Context) error { return s.Notify.SendTest(ctx) })
}

func (s *Server) digestNow(w http.ResponseWriter, r *http.Request) {
	s.sendNow(w, r, "digest sent manually", func(ctx context.Context) error { return s.Notify.SendDigestNow(ctx) })
}

// sendNow runs a manual send (not cancelled when the browser goes away) and audits it.
func (s *Server) sendNow(w http.ResponseWriter, r *http.Request, action string, send func(context.Context) error) {
	if s.notifyOff(w) {
		return
	}
	err := send(context.WithoutCancel(r.Context()))
	s.audit(r, action, "ok", err == nil)
	if err != nil {
		httpError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"sent": true})
}

func (s *Server) setDigestEmail(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	var b struct {
		On bool `json:"on"`
	}
	if !decodeJSON(w, r, &b) {
		return
	}
	if b.On && s.Auth.Mail == nil {
		httpError(w, http.StatusConflict, "email isn't set up on this server (OWL_SMTP_*)")
		return
	}
	if err := s.Store.SetDigestEmail(r.Context(), sess.User.ID, b.On); err != nil {
		s.storeError(w, err)
		return
	}
	u, err := s.Store.GetUser(r.Context(), sess.User.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "digest email opt-in changed", "on", b.On)
	me := toMe(u)
	me.EmailAvailable = s.Auth.Mail != nil
	writeJSON(w, http.StatusOK, me)
}
