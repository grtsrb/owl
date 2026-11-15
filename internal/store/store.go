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
	Namespace       string
	Kind            string
	Workload        string
	Container       string
	Image           string // from the workload template (intended)
	Init            bool
	DesiredReplicas int32
	// Running images observed in pods for this container; usually one, more during a rollout.
	Running []snapshot.RunningImage
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
	ID        int64
	ClusterID int64
	Cluster   string // filled on reads
	At        time.Time
	Type      ChangeType
	Namespace string
	Kind      string
	Workload  string
	Container string
	OldValue  string
	NewValue  string
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

	Close() error
}
