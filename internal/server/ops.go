package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"owl/internal/auth"
	"owl/internal/backup"
	"owl/internal/store"
)

// Settings keys for logs and the audit log (backup keys live in package backup).
const (
	settingLogLevel     = "logLevel"
	settingLogDays      = "logRetentionDays"
	settingAuditDays    = "auditRetentionDays"
	defaultLogDays      = 5
	defaultAuditDays    = 365
	maxImportBytes      = 64 << 20
	maxRestoreBodyBytes = backup.MaxRestoreBytes // compressed upload; decompression is limited too
)

func (s *Server) opsRoutes(mux *http.ServeMux) {
	// Admin: the backup status names the bucket, endpoint, paths and raw error text.
	mux.HandleFunc("GET /api/v1/ops", s.requireAdmin(s.getOps))
	mux.HandleFunc("PUT /api/v1/ops", s.requireAdmin(s.putOps))
	mux.HandleFunc("GET /api/v1/backups", s.requireAdmin(s.listBackups))
	mux.HandleFunc("POST /api/v1/backups", s.requireAdmin(s.backupNow))
	mux.HandleFunc("GET /api/v1/backups/{name}", s.requireAdmin(s.downloadBackup))
	mux.HandleFunc("POST /api/v1/backups/{name}/restore", s.requireAdmin(s.restoreStored))
	mux.HandleFunc("POST /api/v1/restore", s.requireAdmin(s.restoreUpload))
	mux.HandleFunc("POST /api/v1/clusters/import", s.requireAdmin(s.importCluster))
	mux.HandleFunc("GET /api/v1/audit", s.requireAdmin(s.listAudit))
}

type opsSettings struct {
	BackupTime         string `json:"backupTime"`
	BackupLocalDays    int    `json:"backupLocalDays"`
	BackupLocalCopies  int    `json:"backupLocalCopies"`
	LogLevel           string `json:"logLevel"`
	LogRetentionDays   int    `json:"logRetentionDays"`
	AuditRetentionDays int    `json:"auditRetentionDays"`
}

func (s *Server) loadOps(ctx context.Context) (opsSettings, error) {
	m, err := s.Store.GetSettings(ctx)
	if err != nil {
		return opsSettings{}, err
	}
	b := backup.ParseSettings(m)
	o := opsSettings{BackupTime: b.TimeString(), BackupLocalDays: b.LocalDays, BackupLocalCopies: b.LocalCopies,
		LogRetentionDays: atoiOr(m[settingLogDays], defaultLogDays), AuditRetentionDays: atoiOr(m[settingAuditDays], defaultAuditDays)}
	if _, ok := parseLevel(m[settingLogLevel]); ok {
		o.LogLevel = strings.ToLower(m[settingLogLevel]) // "" = never set in the UI: OWL_LOG_LEVEL applies
	}
	return o, nil
}

// parseLevel accepts slog's level names (debug, info, warn, error).
func parseLevel(v string) (slog.Level, bool) {
	var l slog.Level
	if v == "" || l.UnmarshalText([]byte(v)) != nil {
		return 0, false
	}
	return l, true
}

func validLevelName(v string) bool {
	_, ok := parseLevel(v)
	return ok && v == strings.ToLower(v) && !strings.ContainsAny(v, "+-0123456789")
}

func atoiOr(v string, def int) int {
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return n
	}
	return def
}

// ApplyOpsSettings pushes the stored log level and log retention into the running logger/log file.
// Called at startup, after a settings change and after a restore.
func (s *Server) ApplyOpsSettings(ctx context.Context) {
	o, err := s.loadOps(ctx)
	if err != nil {
		return
	}
	if l, ok := parseLevel(o.LogLevel); ok && s.LogLevel != nil {
		s.LogLevel.Set(l) // only a level saved in the UI overrides OWL_LOG_LEVEL
	}
	if s.LogFile != nil {
		s.LogFile.SetRetentionDays(o.LogRetentionDays)
	}
}

func (s *Server) getOps(w http.ResponseWriter, r *http.Request) {
	o, err := s.loadOps(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	if o.LogLevel == "" && s.LogLevel != nil {
		o.LogLevel = strings.ToLower(s.LogLevel.Level().String()) // show what's in effect (env/default)
	}
	out := map[string]any{"settings": o, "backupsEnabled": s.Backups != nil, "logFile": s.LogFile != nil}
	if s.Backups != nil {
		out["backup"] = s.Backups.Status(r.Context())
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) putOps(w http.ResponseWriter, r *http.Request) {
	var o opsSettings
	if !decodeJSON(w, r, &o) {
		return
	}
	o.LogLevel = strings.ToLower(o.LogLevel)
	switch _, _, okT := backup.ParseTime(o.BackupTime); {
	case !okT:
		httpError(w, http.StatusBadRequest, "backupTime: HH:MM, e.g. 03:00")
		return
	case o.BackupLocalDays < 1 || o.BackupLocalDays > 3650, o.BackupLocalCopies < 1 || o.BackupLocalCopies > 1000:
		httpError(w, http.StatusBadRequest, "backup retention: 1–3650 days, 1–1000 copies")
		return
	case !validLevelName(o.LogLevel):
		httpError(w, http.StatusBadRequest, "logLevel: debug, info, warn or error")
		return
	case o.LogRetentionDays < 1 || o.LogRetentionDays > 365, o.AuditRetentionDays < 1 || o.AuditRetentionDays > 3650:
		httpError(w, http.StatusBadRequest, "retention: logs 1–365 days, audit 1–3650 days")
		return
	}
	h, mi, _ := backup.ParseTime(o.BackupTime)
	o.BackupTime = fmt.Sprintf("%02d:%02d", h, mi)
	if err := s.saveSettings(r.Context(), map[string]string{
		backup.KeyTime: o.BackupTime, backup.KeyLocalDays: strconv.Itoa(o.BackupLocalDays), backup.KeyLocalCopies: strconv.Itoa(o.BackupLocalCopies),
		settingLogLevel: o.LogLevel, settingLogDays: strconv.Itoa(o.LogRetentionDays), settingAuditDays: strconv.Itoa(o.AuditRetentionDays),
	}); err != nil {
		s.fail(w, err)
		return
	}
	s.ApplyOpsSettings(r.Context())
	s.audit(r, "operations settings changed", "backupTime", o.BackupTime, "backupLocalDays", o.BackupLocalDays,
		"backupLocalCopies", o.BackupLocalCopies, "logLevel", o.LogLevel, "logRetentionDays", o.LogRetentionDays,
		"auditRetentionDays", o.AuditRetentionDays)
	s.getOps(w, r)
}

func (s *Server) backupsOff(w http.ResponseWriter) bool {
	if s.Backups == nil {
		httpError(w, http.StatusServiceUnavailable, "backups are disabled on this server")
		return true
	}
	return false
}

func (s *Server) listBackups(w http.ResponseWriter, r *http.Request) {
	if s.backupsOff(w) {
		return
	}
	list, err := s.Backups.List(r.Context())
	out := map[string]any{"backups": list}
	if list == nil {
		out["backups"] = []backup.Info{}
	}
	if err != nil {
		if list == nil {
			s.fail(w, err)
			return
		}
		out["remoteError"] = err.Error() // local list still useful
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) backupNow(w http.ResponseWriter, r *http.Request) {
	if s.backupsOff(w) {
		return
	}
	info, err := s.Backups.Run(context.WithoutCancel(r.Context()), "manual")
	if err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "backup taken", "name", info.Name)
	writeJSON(w, http.StatusCreated, info)
}

func (s *Server) downloadBackup(w http.ResponseWriter, r *http.Request) {
	if s.backupsOff(w) {
		return
	}
	name := r.PathValue("name")
	rc, size, err := s.Backups.Open(r.Context(), name)
	if err != nil {
		s.storeError(w, err)
		return
	}
	defer rc.Close()
	s.audit(r, "backup downloaded", "name", name)
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name)) // name matches the backup pattern
	if size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	_, _ = io.Copy(w, rc)
}

func (s *Server) restoreStored(w http.ResponseWriter, r *http.Request) {
	if s.backupsOff(w) {
		return
	}
	name := r.PathValue("name")
	rc, _, err := s.Backups.Open(r.Context(), name)
	if err != nil {
		s.storeError(w, err)
		return
	}
	defer rc.Close()
	s.restore(w, r, rc, "backup "+name)
}

// restoreUpload takes the backup file as the raw body. Only gzip/octet-stream content types are
// accepted, which an HTML form can't send — like the JSON-only rule for other writes.
func (s *Server) restoreUpload(w http.ResponseWriter, r *http.Request) {
	if s.backupsOff(w) {
		return
	}
	switch ct := r.Header.Get("Content-Type"); ct {
	case "application/gzip", "application/x-gzip", "application/octet-stream", "application/vnd.sqlite3", "application/x-sqlite3":
	default:
		httpError(w, http.StatusUnsupportedMediaType, "send the backup file as application/gzip or application/octet-stream")
		return
	}
	s.restore(w, r, http.MaxBytesReader(w, r.Body, maxRestoreBodyBytes), "uploaded file")
}

func (s *Server) restore(w http.ResponseWriter, r *http.Request, body io.Reader, from string) {
	ctx := context.WithoutCancel(r.Context()) // don't abort half-way if the browser goes away
	var safety string
	run := func() (err error) {
		if safety, err = s.Backups.Restore(ctx, body); err != nil {
			return err
		}
		// Custom components come back with the backup: rebuild the catalog before anything reads it.
		// The restore itself succeeded either way, so a failure here is logged, not reported as one.
		if err := s.ReloadCatalog(ctx); err != nil {
			s.Log.Error("catalog reload after restore failed", "err", err)
		}
		// Registry credentials (sealed) come back too.
		if err := s.ReloadCredentials(ctx); err != nil {
			s.Log.Error("registry credentials reload after restore failed", "err", err)
		}
		return nil
	}
	var err error
	if s.Notify != nil {
		// No check may see the restored data before notifications have recorded it quietly (an old
		// backup must not cause an alert storm): checks wait while the restore runs.
		err = s.Notify.WhileQuiet(ctx, run)
	} else {
		err = run()
	}
	if err != nil {
		s.Log.Error("restore failed", "from", from, "err", err)
		code := http.StatusInternalServerError // disk, safety backup, transaction: the server's problem
		if errors.Is(err, backup.ErrInvalidBackup) || errors.Is(err, store.ErrInvalid) {
			code = http.StatusBadRequest // the file
		}
		httpError(w, code, err.Error())
		return
	}
	s.ApplyOpsSettings(ctx)
	s.kickSummaries() // settings (mode, caps) and clusters came back from the backup
	s.kickScans()     // the restored clusters may run other images
	s.audit(r, "database restored", "from", from, "safetyBackup", safety)
	go s.RefreshUpstream(context.Background()) // components the backup added or removed (cached: cheap)
	writeJSON(w, http.StatusOK, map[string]string{"restored": from, "safetyBackup": safety})
}

func (s *Server) importCluster(w http.ResponseWriter, r *http.Request) {
	if !requireJSON(w, r) {
		return
	}
	var ex ClusterExport
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxImportBytes)).Decode(&ex); err != nil {
		httpError(w, http.StatusBadRequest, "invalid export: "+err.Error())
		return
	}
	c := ex.Cluster
	c.Tenant = strings.TrimSpace(c.Tenant)
	switch {
	case ex.Format != exportFormat || ex.Version != 1:
		httpError(w, http.StatusBadRequest, fmt.Sprintf("not an Owl cluster export (format %q version %d)", ex.Format, ex.Version))
		return
	case !store.ValidClusterName(c.Name) || !store.ValidStageName(c.Stage) || !store.ValidTenantName(c.Tenant):
		httpError(w, http.StatusBadRequest, "export has an invalid cluster name, stage or tenant")
		return
	}
	token, hash, prefix := auth.NewToken()
	cl, err := s.Store.ImportCluster(r.Context(), store.ClusterImport{Name: c.Name, Stage: c.Stage, Tenant: c.Tenant,
		CreatedAt: ex.CreatedAt, KubernetesVersion: c.KubernetesVersion, AgentVersion: c.AgentVersion,
		Images: ex.Images, Changes: ex.Changes}, hash, prefix)
	if err != nil {
		s.storeError(w, err)
		return
	}
	s.audit(r, "cluster imported", "cluster", cl.Name, "images", len(ex.Images), "changes", len(ex.Changes))
	writeJSON(w, http.StatusCreated, map[string]any{"cluster": toClusterJSON(cl), "token": token})
}

func (s *Server) listAudit(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	es, err := s.Store.ListAudit(r.Context(), limit)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, es)
}

// RunMaintenance prunes the audit log to its retention once a day (and at startup).
func (s *Server) RunMaintenance(ctx context.Context) {
	for {
		if o, err := s.loadOps(ctx); err == nil {
			cut := time.Now().Add(-time.Duration(o.AuditRetentionDays) * 24 * time.Hour)
			if n, err := s.Store.PruneAudit(ctx, cut); err != nil {
				s.Log.Error("audit prune failed", "err", err)
			} else if n > 0 {
				s.Log.Info("audit log pruned", "deleted", n, "olderThanDays", o.AuditRetentionDays)
			}
		}
		// Expired sessions and links are already refused when used; this only clears them out.
		if err := s.Store.PruneSessions(ctx, time.Now().Add(-sessionIdle)); err != nil && ctx.Err() == nil {
			s.Log.Error("session prune failed", "err", err)
		}
		s.warnExpiringTokens(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(24 * time.Hour):
		}
	}
}
