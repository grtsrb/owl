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
	// ErrChanged: a user changed between computing a users-file plan and applying it.
	ErrChanged = errors.New("changed since the plan was made")
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

// CustomComponent is a catalog component added in the UI (milestone 11). Data is the component as JSON,
// validated by the catalog package; the store treats it as opaque.
type CustomComponent struct {
	ID        string    `json:"id"`
	Data      []byte    `json:"-"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// CustomDeleted is what deleting a custom component removed along with it.
type CustomDeleted struct {
	Policy    bool     `json:"policy"`    // its version policy
	Overrides []string `json:"overrides"` // repositories mapped to it
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
	// NodeArch: the nodes' most common CPU architecture (amd64, arm64), "" before the first snapshot.
	NodeArch string
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
	// DigestEmail: the user opted in to the weekly digest by email (milestone 10).
	DigestEmail bool `json:"digestEmail"`
	// Managed: the user is listed in a users file (`owl users apply`, milestone 12). Changes in the UI
	// still work but show as drift until the file is updated.
	Managed bool `json:"managed"`
	// InvitePending: an invite link that still works exists (an invited user whose link expired needs
	// a new one).
	InvitePending bool `json:"invitePending"`
	// PasswordHash (argon2id, PHC string) and GoogleSub never leave the server.
	PasswordHash string `json:"-"`
	GoogleSub    string `json:"-"`
}

// Active: accepted the invite and not disabled.
func (u *User) Active() bool { return u.ActivatedAt != nil && u.DisabledAt == nil }

// UserChange is one step of applying a users file (milestone 12). Invite creates a managed user;
// Update sets role, disabled and managed of UserID — only if the user still has the Was* values (else
// ErrChanged), so an apply does exactly what its plan showed.
type UserChange struct {
	Invite      bool
	Email       string
	UserID      int64
	Role        Role
	Disabled    bool
	Managed     bool
	WasRole     Role
	WasDisabled bool
	WasManaged  bool
}

// Session is a signed-in browser. Only the SHA-256 of the cookie value is stored.
type Session struct {
	TokenHash  string
	User       User
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time // absolute limit; the idle limit is checked against LastSeenAt
	// APIToken is set when the request was authenticated with a personal API token instead of a
	// browser session (milestone 12); TokenHash/LastSeenAt/ExpiresAt are then unused.
	APIToken *APIToken
}

// CanWrite: browser sessions follow the user's role; API tokens also need the write scope, and a write
// token whose owner is no longer an admin is read-only.
func (s *Session) CanWrite() bool {
	if s.User.Role != RoleAdmin {
		return false
	}
	return s.APIToken == nil || s.APIToken.Scope == ScopeWrite
}

// TokenScope is what a personal API token may do.
type TokenScope string

const (
	ScopeRead  TokenScope = "read"
	ScopeWrite TokenScope = "write" // admins only; effective only while the owner is an admin
)

func (s TokenScope) Valid() bool { return s == ScopeRead || s == ScopeWrite }

// APIToken is a personal API token (milestone 12) for the CLI. Only the SHA-256 is stored; Prefix is
// a short non-secret part for display. ExpiresAt nil = never expires.
type APIToken struct {
	ID         int64      `json:"id"`
	UserID     int64      `json:"userId"`
	UserEmail  string     `json:"userEmail"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Scope      TokenScope `json:"scope"`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	WarnedAt   *time.Time `json:"-"`
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

// ImageScan is the stored state of one image digest's vulnerability scan (milestone 15): the last good
// result (JSON from the scan package; opaque here) and the last attempt's error, if it failed. Not in
// backups (regenerable).
type ImageScan struct {
	Digest    string
	Ref       string
	ScannedAt *time.Time // last good scan
	Result    []byte
	Error     string
	AttemptAt time.Time
}

// VulnAcceptance: the team agreed to leave a vulnerability in an image repository (all its tags and
// digests), until ExpiresAt if set. Restored with backups (a decision, like catalog overrides).
type VulnAcceptance struct {
	Repository string     `json:"repository"`
	VulnID     string     `json:"vulnId"`
	Reason     string     `json:"reason"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	CreatedBy  string     `json:"createdBy"`
	CreatedAt  time.Time  `json:"createdAt"`
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

// CriticalMark is a (component, cluster) pair that is currently Critical, as notifications last saw it.
type CriticalMark struct {
	Component string    `json:"component"`
	Cluster   string    `json:"cluster"`
	Since     time.Time `json:"since"`
}

// ResolvedMark is a pair that left Critical (for the digest's "no longer Critical").
type ResolvedMark struct {
	Component string    `json:"component"`
	Cluster   string    `json:"cluster"`
	At        time.Time `json:"at"`
}

// NotifyState is what notifications remember between checks (milestone 10). Clusters are keyed by
// name + creation time, so deleting a cluster and adding it again under the same name is a new cluster.
// It's kept across restores, like the audit log.
type NotifyState struct {
	Critical []CriticalMark
	Clusters map[string]bool // ClusterKey(name, createdAt) of clusters already announced
	Repos    map[string]bool // image repositories already known as unrecognized
	Meta     map[string]string
}

func ClusterKey(name string, created time.Time) string {
	return name + "@" + created.UTC().Format(time.RFC3339Nano)
}

// NotifyDelta is one check's changes to NotifyState, applied atomically. Resolved pairs are removed
// from the Critical set and recorded as resolved (unless Quiet); Dropped pairs are removed silently
// (their cluster is gone).
type NotifyDelta struct {
	AddCritical  []CriticalMark
	Resolved     []CriticalMark
	Dropped      []CriticalMark
	Clusters     []string // keys to add
	DropClusters []string
	Repos        []string
	Meta         map[string]string
	At           time.Time
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

	// Custom catalog components (restored with backups). CreateCustomComponent returns ErrExists for a
	// taken id, UpdateCustomComponent ErrNotFound for a missing one. DeleteCustomComponent also removes
	// the component's policy and the overrides mapping to it, atomically; DeleteImpact reports what that
	// would remove without changing anything.
	ListCustomComponents(ctx context.Context) ([]CustomComponent, error)
	CreateCustomComponent(ctx context.Context, id string, data []byte) error
	UpdateCustomComponent(ctx context.Context, id string, data []byte) error
	// cascade=false deletes only the row: when a built-in component has taken over the id, the policy
	// and overrides under that id are the built-in's now.
	DeleteCustomComponent(ctx context.Context, id string, cascade bool) (*CustomDeleted, error)
	CustomDeleteImpact(ctx context.Context, id string) (*CustomDeleted, error)

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
	// ChangesSince returns every cluster's changes at or after since, newest first (the weekly digest).
	ChangesSince(ctx context.Context, since time.Time) ([]Change, error)
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
	// DeleteSummaries removes a component's summaries (a custom component now points at another project).
	DeleteSummaries(ctx context.Context, component string) error
	AddSummaryRequest(ctx context.Context, r SummaryRequest) error
	// ListSummaryRequests returns the newest first.
	ListSummaryRequests(ctx context.Context, limit int) ([]SummaryRequest, error)
	// SummarySpend is the total cost (micro-USD) of requests at or after since.
	SummarySpend(ctx context.Context, since time.Time) (int64, error)

	// Users (milestone 9). Users, sessions and one-time tokens are kept across restores: a restore
	// never changes who can sign in. Emails are compared lowercase. UpdateUser and DeleteUser return
	// ErrLastAdmin when no active admin would be left; disabling or deleting a user ends their sessions
	// and deletes their API tokens.
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
	// ApplyUserChanges applies a users-file plan in one transaction: ErrChanged when a user no longer
	// matches the plan (or an invited email exists by now), ErrLastAdmin when no active admin would be
	// left. Disabling ends sessions and one-time links. Returns the invited users.
	ApplyUserChanges(ctx context.Context, changes []UserChange) ([]User, error)

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

	// Personal API tokens (milestone 12). Never in backups, never restored (like sessions).
	// CreateAPIToken returns ErrExists when the user already has a token with that name.
	// ListAPITokens: userID 0 = every user's. APITokenByHash returns the token with its user (expired
	// ones too: the caller decides), or ErrNotFound. DeleteAPIToken: userID 0 = any owner; ErrNotFound
	// when there's no such token (of that user). ExpiringAPITokens: not yet warned, expiring before t.
	CreateAPIToken(ctx context.Context, t APIToken, hash string) (*APIToken, error)
	ListAPITokens(ctx context.Context, userID int64) ([]APIToken, error)
	APITokenByHash(ctx context.Context, hash string) (*APIToken, *User, error)
	TouchAPIToken(ctx context.Context, id int64) error
	DeleteAPIToken(ctx context.Context, id, userID int64) (*APIToken, error)
	ExpiringAPITokens(ctx context.Context, before time.Time) ([]APIToken, error)
	MarkAPITokenWarned(ctx context.Context, id int64) error

	// Notifications (milestone 10). Kept across restores. ListResolved returns pairs that left Critical
	// at or after since, oldest first; PruneResolved deletes older ones. SetDigestEmail is the user's
	// own opt-in for the digest by email.
	NotifyState(ctx context.Context) (*NotifyState, error)
	ApplyNotifyDelta(ctx context.Context, d NotifyDelta) error
	SetNotifyMeta(ctx context.Context, key, value string) error
	ListResolved(ctx context.Context, since time.Time) ([]ResolvedMark, error)
	PruneResolved(ctx context.Context, before time.Time) error
	SetDigestEmail(ctx context.Context, userID int64, on bool) error

	// Image scans (milestone 15): not in backups, not restored. DeleteImageScansExcept removes every
	// digest not in keep (no longer running anywhere).
	ListImageScans(ctx context.Context) ([]ImageScan, error)
	PutImageScan(ctx context.Context, s ImageScan) error
	DeleteImageScansExcept(ctx context.Context, keep []string) (int64, error)
	// Accepted vulnerabilities (restored with backups). PutVulnAcceptance replaces an existing one;
	// DeleteVulnAcceptance returns ErrNotFound when there's none.
	ListVulnAcceptances(ctx context.Context) ([]VulnAcceptance, error)
	PutVulnAcceptance(ctx context.Context, a VulnAcceptance) error
	DeleteVulnAcceptance(ctx context.Context, repository, vulnID string) error

	// Upstream response cache (release lists, EOL data, advisories).
	GetCache(ctx context.Context, key string) (*CacheEntry, error)
	PutCache(ctx context.Context, e CacheEntry) error
	// TouchCache marks an entry fresh again (a 304: the body didn't change, so it isn't rewritten).
	TouchCache(ctx context.Context, key string, at time.Time) error

	Close() error
}
