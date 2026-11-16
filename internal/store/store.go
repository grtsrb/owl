// Package store defines persistence for the server. The SQLite implementation lives in
// internal/store/sqlite; a Postgres one can implement the same interface later.
package store

import (
	"context"
	"errors"
	"time"

	"service-inventory/internal/snapshot"
)

var (
	ErrNotFound     = errors.New("not found")
	ErrExists       = errors.New("already exists")
	ErrInUse        = errors.New("in use")
	ErrUnknownStage = errors.New("unknown stage")
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

type IngestResult struct {
	Changes []Change
	First   bool
}

type Store interface {
	// CreateCluster needs an existing stage (ErrUnknownStage); a new tenant is created on the fly.
	CreateCluster(ctx context.Context, name string, stage Stage, tenant string) (*Cluster, error)
	ListClusters(ctx context.Context) ([]Cluster, error)
	GetCluster(ctx context.Context, name string) (*Cluster, error)
	// UpdateCluster changes stage and tenant; both must exist (ErrUnknownStage / ErrNotFound).
	UpdateCluster(ctx context.Context, name string, stage Stage, tenant string) (*Cluster, error)
	DeleteCluster(ctx context.Context, name string) error

	// Stages, ordered by position. DeleteStage returns ErrInUse while clusters use it;
	// ReorderStages takes every stage name exactly once, earliest first.
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

	// Upstream response cache (release lists, EOL data, advisories).
	GetCache(ctx context.Context, key string) (*CacheEntry, error)
	PutCache(ctx context.Context, e CacheEntry) error

	Close() error
}
