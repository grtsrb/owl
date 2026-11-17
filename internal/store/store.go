// Package store defines persistence for the server. The SQLite implementation lives in
// internal/store/sqlite; a Postgres one can implement the same interface later.
package store

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"owl/internal/snapshot"
)

var (
	ErrNotFound      = errors.New("not found")
	ErrExists        = errors.New("already exists")
	ErrInUse         = errors.New("in use")
	ErrUnknownStage  = errors.New("unknown stage")
	ErrUnknownTenant = errors.New("unknown tenant")
	ErrInvalid       = errors.New("invalid input")
	// ErrLastAdmin: the change would leave no active admin (nobody could manage Owl any more).
	ErrLastAdmin = errors.New("Owl needs at least one active admin")
)

// Names end up in kubectl contexts, file names, HTTP headers and YAML, so every entry point (API and
// CLI) checks them with these.
var (
	reClusterName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	reTenantName  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _.-]{0,39}$`)
)

func ValidClusterName(s string) bool { return reClusterName.MatchString(s) }
func ValidStageName(s string) bool   { return reClusterName.MatchString(s) }
func ValidTenantName(s string) bool  { return reTenantName.MatchString(s) && strings.TrimSpace(s) == s }

const (
	ClusterNameRule = "lowercase letters, digits and '-', up to 63 characters"
	StageNameRule   = ClusterNameRule
	TenantNameRule  = "letters, digits, space, '_', '.', '-', up to 40 characters"
)

// Stage is a cluster's position in the promotion path, used for drift detection. Stages are
// user-defined and ordered (see ListStages); these are the defaults a new database starts with.
type Stage string

const (
	StageStaging Stage = "staging"
	StagePrelive Stage = "prelive"
	StageLive    Stage = "live"
)

// StageInfo is one entry of the ordered stage list.
type StageInfo struct {
	Name     string `json:"name"`
	Position int    `json:"position"` // 1 = earliest in the promotion path
	Clusters int    `json:"clusters"` // how many clusters use it
}

type TenantInfo struct {
	Name     string `json:"name"`
	Clusters int    `json:"clusters"`
}

// OverrideKind is the decision made about an image the catalog doesn't recognize.
type OverrideKind string

const (
	OverrideMap     OverrideKind = "map"     // treat as Component
	OverrideInHouse OverrideKind = "inhouse" // our own image: listed, not checked upstream
	OverrideIgnore  OverrideKind = "ignore"  // hide from the unrecognized list
)

func (k OverrideKind) Valid() bool {
	return k == OverrideMap || k == OverrideInHouse || k == OverrideIgnore
}

// CatalogOverride applies to every image of Repository (registry/repository, as catalog.Ref.Name()).
type CatalogOverride struct {
	Repository string       `json:"repository"`
	Kind       OverrideKind `json:"kind"`
	Component  string       `json:"component,omitempty"` // for OverrideMap
	UpdatedAt  time.Time    `json:"updatedAt"`
}

type Cluster struct {
	ID                int64
	Name              string
	Stage             Stage
	Tenant            string
	CreatedAt         time.Time
	LastSeenAt        *time.Time
	KubernetesVersion string
	AgentVersion      string
}

// ImageRow is one container image of one workload in a cluster's current state.
type ImageRow struct {
	Namespace       string `json:"namespace"`
	Kind            string `json:"kind"`
	Workload        string `json:"workload"`
	Container       string `json:"container"`
	Image           string `json:"image"` // from the workload template (intended)
	Init            bool   `json:"init,omitempty"`
	DesiredReplicas int32  `json:"desiredReplicas"`
	// Running images observed in pods for this container; usually one, more during a rollout.
	Running []snapshot.RunningImage `json:"running"`
}

type ChangeType string

const (
	ChangeAdded          ChangeType = "added"
	ChangeRemoved        ChangeType = "removed"
	ChangeImageChanged   ChangeType = "image_changed"
	ChangeK8sVersion     ChangeType = "kubernetes_version_changed"
	ChangeClusterFirstIn ChangeType = "first_snapshot"
)

type Change struct {
	ID        int64      `json:"id"`
	ClusterID int64      `json:"-"`
	Cluster   string     `json:"cluster"` // filled on reads
	At        time.Time  `json:"at"`
	Type      ChangeType `json:"type"`
	Namespace string     `json:"namespace,omitempty"`
	Kind      string     `json:"kind,omitempty"`
	Workload  string     `json:"workload,omitempty"`
	Container string     `json:"container,omitempty"`
	OldValue  string     `json:"oldValue,omitempty"`
	NewValue  string     `json:"newValue,omitempty"`
}

type CacheEntry struct {
	Key       string
	FetchedAt time.Time
	ETag      string
	Body      []byte
}

// Role is what a signed-in user may do: viewers read everything, admins also change things.
type Role string

const (
	RoleAdmin  Role = "admin"
	RoleViewer Role = "viewer"
)

func (r Role) Valid() bool { return r == RoleAdmin || r == RoleViewer }

// User is someone invited to Owl. ActivatedAt is nil until they accept the invite (set a password or
// sign in with Google). Disabled users can't sign in; their sessions are gone.
type User struct {
	ID          int64      `json:"id"`
	Email       string     `json:"email"` // lowercase
	Role        Role       `json:"role"`
	CreatedAt   time.Time  `json:"createdAt"`
	ActivatedAt *time.Time `json:"activatedAt,omitempty"`
	LastLoginAt *time.Time `json:"lastLoginAt,omitempty"`
	DisabledAt  *time.Time `json:"disabledAt,omitempty"`
	HasPassword bool       `json:"hasPassword"`
	HasGoogle   bool       `json:"hasGoogle"`
	// PasswordHash (argon2id, PHC string) and GoogleSub never leave the server.
	PasswordHash string `json:"-"`
	GoogleSub    string `json:"-"`
}

// Active: accepted the invite and not disabled.
func (u *User) Active() bool { return u.ActivatedAt != nil && u.DisabledAt == nil }

// Session is a signed-in browser. Only the SHA-256 of the cookie value is stored.
type Session struct {
	TokenHash  string
	User       User
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time // absolute limit; the idle limit is checked against LastSeenAt
}

// AuthTokenKind is what a one-time link does.
type AuthTokenKind string

const (
	TokenInvite AuthTokenKind = "invite" // accept an invite: set a password (or sign in with Google)
	TokenReset  AuthTokenKind = "reset"  // set a new password
)

// AuditEntry is one admin action ("who changed what"). Actor is the signed-in user's email.
type AuditEntry struct {
	ID      int64     `json:"id"`
	At      time.Time `json:"at"`
	Action  string    `json:"action"`
	Details string    `json:"details"`
	Remote  string    `json:"remote"`
	Actor   string    `json:"actor,omitempty"`
}

// ClusterImport is a cluster export brought back after a delete (milestone 7).
type ClusterImport struct {
	Name              string
	Stage             string
	Tenant            string
	CreatedAt         time.Time
	KubernetesVersion string
	AgentVersion      string
	Images            []ImageRow
	Changes           []Change
}

// Summary is a Claude-written upgrade summary for one jump (milestone 8). Content is the validated
// JSON the summary package produced; the store treats it as opaque.
type Summary struct {
	Component     string    `json:"component"`
	FromVersion   string    `json:"from"`
	ToVersion     string    `json:"to"`
	Model         string    `json:"model"`
	PromptVersion int       `json:"promptVersion"`
	CreatedAt     time.Time `json:"createdAt"`
	Content       []byte    `json:"-"`
}

// SummaryRequest is one paid (or attempted) request to Claude, for spending caps and the request log.
// CostMicros is in millionths of a US dollar. Result is "ok", "failed" or "resumed" (an admin cleared a
// pause; resets the consecutive-failure count, costs nothing).
type SummaryRequest struct {
	ID           int64     `json:"id"`
	At           time.Time `json:"at"`
	Component    string    `json:"component"`
	FromVersion  string    `json:"from"`
	ToVersion    string    `json:"to"`
	Model        string    `json:"model"`
	InputTokens  int64     `json:"inputTokens"`
	OutputTokens int64     `json:"outputTokens"`
	CostMicros   int64     `json:"costMicros"`
	Result       string    `json:"result"`
	Error        string    `json:"error,omitempty"`
}

type IngestResult struct {
	Changes []Change
	First   bool
}

type Store interface {
	// CreateCluster needs an existing stage (ErrUnknownStage); a new tenant is created on the fly.
	CreateCluster(ctx context.Context, name string, stage Stage, tenant string) (*Cluster, error)
	// CreateClusterWithToken creates the cluster and its first agent token atomically.
	CreateClusterWithToken(ctx context.Context, name string, stage Stage, tenant, tokenHash, tokenPrefix string) (*Cluster, error)
	ListClusters(ctx context.Context) ([]Cluster, error)
	GetCluster(ctx context.Context, name string) (*Cluster, error)
	// UpdateCluster changes stage and tenant; both must exist (ErrUnknownStage / ErrUnknownTenant).
	UpdateCluster(ctx context.Context, name string, stage Stage, tenant string) (*Cluster, error)
	DeleteCluster(ctx context.Context, name string) error

	// Stages, ordered by position. DeleteStage returns ErrInUse while clusters use it;
	// ReorderStages takes every stage name exactly once, earliest first (else ErrInvalid).
	ListStages(ctx context.Context) ([]StageInfo, error)
	CreateStage(ctx context.Context, name string) error
	DeleteStage(ctx context.Context, name string) error
	ReorderStages(ctx context.Context, names []string) error

	// Tenants. RenameTenant updates every cluster of that tenant; DeleteTenant returns ErrInUse while
	// clusters use it.
	ListTenants(ctx context.Context) ([]TenantInfo, error)
	CreateTenant(ctx context.Context, name string) error
	RenameTenant(ctx context.Context, from, to string) error
	DeleteTenant(ctx context.Context, name string) error

	// Catalog overrides for unrecognized images, keyed by repository.
	ListOverrides(ctx context.Context) ([]CatalogOverride, error)
	SetOverride(ctx context.Context, o CatalogOverride) error
	DeleteOverride(ctx context.Context, repository string) error

	// Settings: small key/value pairs edited in the UI (agent image, server URL, ...).
	GetSettings(ctx context.Context) (map[string]string, error)
	SetSetting(ctx context.Context, key, value string) error

	// AddToken stores a token hash for the cluster; prefix is a short non-secret label for display.
	AddToken(ctx context.Context, clusterID int64, hash, prefix string) error
	// RevokeTokens revokes all active tokens of a cluster.
	RevokeTokens(ctx context.Context, clusterID int64) error
	// RotateToken revokes the cluster's active tokens and adds a new one in one transaction, so a
	// failure never leaves the agent locked out without a replacement.
	RotateToken(ctx context.Context, clusterID int64, hash, prefix string) error
	// ClusterByTokenHash returns the cluster for an active token and records its use.
	ClusterByTokenHash(ctx context.Context, hash string) (*Cluster, error)

	// Ingest replaces the cluster's current state with the snapshot and records changes, atomically.
	Ingest(ctx context.Context, clusterID int64, snap *snapshot.Snapshot) (*IngestResult, error)
	CurrentImages(ctx context.Context, clusterID int64) ([]ImageRow, error)
	ListChanges(ctx context.Context, clusterName string, limit int) ([]Change, error)
	// AllChanges returns a cluster's full history, oldest first (for exports).
	AllChanges(ctx context.Context, clusterID int64) ([]Change, error)

	// Version policies: scope "global" or a component id -> policy spec (see internal/policy).
	ListPolicies(ctx context.Context) (map[string]string, error)
	SetPolicy(ctx context.Context, scope, spec string) error
	DeletePolicy(ctx context.Context, scope string) error

	// Audit log of admin actions, newest first.
	AddAudit(ctx context.Context, e AuditEntry) error
	ListAudit(ctx context.Context, limit int) ([]AuditEntry, error)
	PruneAudit(ctx context.Context, before time.Time) (int64, error)

	// Backup writes a consistent copy of the database to path while it's in use (upstream cache left
	// out: it's refetched). Restore replaces clusters, tokens, state, history, stages, tenants,
	// policies, overrides and settings with the backup's — after migrating the backup to this binary's
	// schema — and keeps the audit log and upstream cache (who-did-what isn't rewritten by a restore).
	Backup(ctx context.Context, path string) error
	Restore(ctx context.Context, path string) error
	// ImportCluster recreates a deleted cluster from its export, with a new agent token. Missing
	// stages/tenants are created (a stage is appended last).
	ImportCluster(ctx context.Context, imp ClusterImport, tokenHash, tokenPrefix string) (*Cluster, error)

	// Claude upgrade summaries (kept across restores, like the audit log) and the log of requests made
	// for them (the spending caps are computed from it).
	ListSummaries(ctx context.Context) ([]Summary, error)
	PutSummary(ctx context.Context, s Summary) error
	AddSummaryRequest(ctx context.Context, r SummaryRequest) error
	// ListSummaryRequests returns the newest first.
	ListSummaryRequests(ctx context.Context, limit int) ([]SummaryRequest, error)
	// SummarySpend is the total cost (micro-USD) of requests at or after since.
	SummarySpend(ctx context.Context, since time.Time) (int64, error)

	// Users (milestone 9). Users, sessions and one-time tokens are kept across restores: a restore
	// never changes who can sign in. Emails are compared lowercase. UpdateUser and DeleteUser return
	// ErrLastAdmin when no active admin would be left; disabling or deleting a user ends their sessions.
	CreateUser(ctx context.Context, email string, role Role) (*User, error)
	GetUser(ctx context.Context, id int64) (*User, error)
	UserByEmail(ctx context.Context, email string) (*User, error)
	UserByGoogleSub(ctx context.Context, sub string) (*User, error)
	ListUsers(ctx context.Context) ([]User, error)
	UpdateUser(ctx context.Context, id int64, role Role, disabled bool) (*User, error)
	DeleteUser(ctx context.Context, id int64) error
	// SetPassword stores a new hash, activates an invited user and ends their other sessions
	// (keepSession, if not empty, survives — the user's own session after changing their password).
	SetPassword(ctx context.Context, id int64, hash, keepSession string) error
	// LinkGoogle stores the account's Google subject and activates an invited user.
	LinkGoogle(ctx context.Context, id int64, sub string) error
	RecordLogin(ctx context.Context, id int64) error

	// Sessions: CreateSession stores a session (hash of the cookie value); SessionByHash returns it
	// with its user, or ErrNotFound; TouchSession updates last-seen.
	CreateSession(ctx context.Context, hash string, userID int64, expires time.Time) error
	SessionByHash(ctx context.Context, hash string) (*Session, error)
	TouchSession(ctx context.Context, hash string) error
	DeleteSession(ctx context.Context, hash string) error
	// PruneSessions deletes sessions that expired (absolute) or were idle since before idleBefore,
	// and expired one-time tokens.
	PruneSessions(ctx context.Context, idleBefore time.Time) error

	// One-time tokens (invite, reset). CreateAuthToken replaces the user's earlier invite; reset tokens
	// accumulate until they expire or the password is set.
	// PeekAuthToken returns the user for a valid token without using it; ConsumeAuthToken does the same
	// and deletes the token, atomically (ErrNotFound when missing, expired, or the user is disabled).
	CreateAuthToken(ctx context.Context, hash string, userID int64, kind AuthTokenKind, expires time.Time) error
	PeekAuthToken(ctx context.Context, hash string) (*User, AuthTokenKind, error)
	ConsumeAuthToken(ctx context.Context, hash string) (*User, AuthTokenKind, error)

	// Upstream response cache (release lists, EOL data, advisories).
	GetCache(ctx context.Context, key string) (*CacheEntry, error)
	PutCache(ctx context.Context, e CacheEntry) error

	Close() error
}
