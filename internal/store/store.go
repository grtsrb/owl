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
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("already exists")
)

// Stage is a cluster's position in the promotion path, used for drift detection.
type Stage string

const (
	StageStaging Stage = "staging"
	StagePrelive Stage = "prelive"
	StageLive    Stage = "live"
)

// Rank orders stages along the promotion path (staging < prelive < live).
func (s Stage) Rank() int {
	switch s {
	case StageStaging:
		return 1
	case StagePrelive:
		return 2
	case StageLive:
		return 3
	}
	return 0
}

func (s Stage) Valid() bool { return s.Rank() > 0 }

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
	CreateCluster(ctx context.Context, name string, stage Stage, tenant string) (*Cluster, error)
	ListClusters(ctx context.Context) ([]Cluster, error)
	GetCluster(ctx context.Context, name string) (*Cluster, error)
	DeleteCluster(ctx context.Context, name string) error

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

	// Upstream response cache (release lists, EOL data, advisories).
	GetCache(ctx context.Context, key string) (*CacheEntry, error)
	PutCache(ctx context.Context, e CacheEntry) error

	Close() error
}
