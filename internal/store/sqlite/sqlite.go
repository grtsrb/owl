// Package sqlite implements store.Store on SQLite (pure Go driver, no CGO).
package sqlite

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"service-inventory/internal/snapshot"
	"service-inventory/internal/store"
)

//go:embed schema.sql
var schema string

const schemaVersion = 1

type DB struct{ db *sql.DB }

var _ store.Store = (*DB)(nil)

func Open(path string) (*DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection: SQLite allows a single writer anyway, and this avoids SQLITE_BUSY between our own
	// goroutines. Fine for the expected load (a few clusters pushing every 15 minutes).
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &DB{db: db}, nil
}

func migrate(db *sql.DB) error {
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	if v >= schemaVersion {
		return nil
	}
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	_, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion))
	return err
}

func (d *DB) Close() error { return d.db.Close() }

func now() time.Time { return time.Now().UTC().Truncate(time.Millisecond) }

const ts = time.RFC3339Nano

func parseTime(s sql.NullString) *time.Time {
	if !s.Valid || s.String == "" {
		return nil
	}
	t, err := time.Parse(ts, s.String)
	if err != nil {
		return nil
	}
	return &t
}

const clusterCols = "id, name, stage, tenant, created_at, last_seen_at, kubernetes_version, agent_version"

func scanCluster(row interface{ Scan(...any) error }) (*store.Cluster, error) {
	var c store.Cluster
	var created string
	var lastSeen sql.NullString
	if err := row.Scan(&c.ID, &c.Name, &c.Stage, &c.Tenant, &created, &lastSeen, &c.KubernetesVersion, &c.AgentVersion); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	if t := parseTime(sql.NullString{String: created, Valid: true}); t != nil {
		c.CreatedAt = *t
	}
	c.LastSeenAt = parseTime(lastSeen)
	return &c, nil
}

func (d *DB) CreateCluster(ctx context.Context, name string, stage store.Stage, tenant string) (*store.Cluster, error) {
	_, err := d.db.ExecContext(ctx,
		"INSERT INTO clusters(name, stage, tenant, created_at) VALUES (?,?,?,?)",
		name, string(stage), tenant, now().Format(ts))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, store.ErrExists
		}
		return nil, err
	}
	return d.GetCluster(ctx, name)
}

func (d *DB) GetCluster(ctx context.Context, name string) (*store.Cluster, error) {
	return scanCluster(d.db.QueryRowContext(ctx, "SELECT "+clusterCols+" FROM clusters WHERE name=?", name))
}

func (d *DB) ListClusters(ctx context.Context) ([]store.Cluster, error) {
	rows, err := d.db.QueryContext(ctx, "SELECT "+clusterCols+" FROM clusters ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Cluster
	for rows.Next() {
		c, err := scanCluster(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (d *DB) DeleteCluster(ctx context.Context, name string) error {
	res, err := d.db.ExecContext(ctx, "DELETE FROM clusters WHERE name=?", name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (d *DB) AddToken(ctx context.Context, clusterID int64, hash, prefix string) error {
	_, err := d.db.ExecContext(ctx,
		"INSERT INTO agent_tokens(cluster_id, token_hash, prefix, created_at) VALUES (?,?,?,?)",
		clusterID, hash, prefix, now().Format(ts))
	return err
}

func (d *DB) RevokeTokens(ctx context.Context, clusterID int64) error {
	_, err := d.db.ExecContext(ctx,
		"UPDATE agent_tokens SET revoked_at=? WHERE cluster_id=? AND revoked_at IS NULL",
		now().Format(ts), clusterID)
	return err
}

func (d *DB) ClusterByTokenHash(ctx context.Context, hash string) (*store.Cluster, error) {
	c, err := scanCluster(d.db.QueryRowContext(ctx, "SELECT c."+strings.ReplaceAll(clusterCols, ", ", ", c.")+
		" FROM agent_tokens t JOIN clusters c ON c.id=t.cluster_id WHERE t.token_hash=? AND t.revoked_at IS NULL", hash))
	if err != nil {
		return nil, err
	}
	_, err = d.db.ExecContext(ctx, "UPDATE agent_tokens SET last_used_at=? WHERE token_hash=?", now().Format(ts), hash)
	return c, err
}

func (d *DB) Ingest(ctx context.Context, clusterID int64, snap *snapshot.Snapshot) (*store.IngestResult, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var lastSeen sql.NullString
	var oldK8s string
	if err := tx.QueryRowContext(ctx, "SELECT last_seen_at, kubernetes_version FROM clusters WHERE id=?", clusterID).
		Scan(&lastSeen, &oldK8s); err != nil {
		return nil, err
	}
	first := !lastSeen.Valid

	old, err := currentImages(ctx, tx, clusterID)
	if err != nil {
		return nil, err
	}
	rows := store.RowsFromSnapshot(snap)
	at := now()

	var changes []store.Change
	if first {
		// Don't record hundreds of "added" events for the initial inventory.
		changes = []store.Change{{Type: store.ChangeClusterFirstIn, NewValue: fmt.Sprintf("%d containers", len(rows))}}
	} else {
		changes = store.Diff(old, rows)
		if oldK8s != snap.Cluster.KubernetesVersion {
			changes = append(changes, store.Change{Type: store.ChangeK8sVersion, OldValue: oldK8s, NewValue: snap.Cluster.KubernetesVersion})
		}
	}

	if _, err := tx.ExecContext(ctx, "DELETE FROM current_images WHERE cluster_id=?", clusterID); err != nil {
		return nil, err
	}
	ins, err := tx.PrepareContext(ctx, `INSERT INTO current_images
		(cluster_id, namespace, kind, workload, container, image, is_init, desired_replicas, running)
		VALUES (?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return nil, err
	}
	defer ins.Close()
	for _, r := range rows {
		running, _ := json.Marshal(r.Running)
		if _, err := ins.ExecContext(ctx, clusterID, r.Namespace, r.Kind, r.Workload, r.Container, r.Image,
			r.Init, r.DesiredReplicas, string(running)); err != nil {
			return nil, err
		}
	}

	for i := range changes {
		c := &changes[i]
		c.ClusterID, c.At = clusterID, at
		res, err := tx.ExecContext(ctx, `INSERT INTO changes
			(cluster_id, at, type, namespace, kind, workload, container, old_value, new_value)
			VALUES (?,?,?,?,?,?,?,?,?)`,
			clusterID, at.Format(ts), string(c.Type), c.Namespace, c.Kind, c.Workload, c.Container, c.OldValue, c.NewValue)
		if err != nil {
			return nil, err
		}
		c.ID, _ = res.LastInsertId()
	}

	if _, err := tx.ExecContext(ctx,
		"UPDATE clusters SET last_seen_at=?, kubernetes_version=?, agent_version=? WHERE id=?",
		at.Format(ts), snap.Cluster.KubernetesVersion, snap.AgentVersion, clusterID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &store.IngestResult{Changes: changes, First: first}, nil
}

type querier interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
}

func currentImages(ctx context.Context, q querier, clusterID int64) ([]store.ImageRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT namespace, kind, workload, container, image, is_init, desired_replicas, running
		FROM current_images WHERE cluster_id=? ORDER BY namespace, kind, workload, container`, clusterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.ImageRow
	for rows.Next() {
		var r store.ImageRow
		var running string
		if err := rows.Scan(&r.Namespace, &r.Kind, &r.Workload, &r.Container, &r.Image, &r.Init, &r.DesiredReplicas, &running); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(running), &r.Running)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (d *DB) CurrentImages(ctx context.Context, clusterID int64) ([]store.ImageRow, error) {
	return currentImages(ctx, d.db, clusterID)
}

func (d *DB) ListChanges(ctx context.Context, clusterName string, limit int) ([]store.Change, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	q := `SELECT ch.id, ch.cluster_id, c.name, ch.at, ch.type, ch.namespace, ch.kind, ch.workload, ch.container,
		ch.old_value, ch.new_value FROM changes ch JOIN clusters c ON c.id=ch.cluster_id`
	args := []any{}
	if clusterName != "" {
		q += " WHERE c.name=?"
		args = append(args, clusterName)
	}
	q += " ORDER BY ch.at DESC, ch.id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := d.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Change
	for rows.Next() {
		var c store.Change
		var at string
		if err := rows.Scan(&c.ID, &c.ClusterID, &c.Cluster, &at, &c.Type, &c.Namespace, &c.Kind, &c.Workload,
			&c.Container, &c.OldValue, &c.NewValue); err != nil {
			return nil, err
		}
		if t := parseTime(sql.NullString{String: at, Valid: true}); t != nil {
			c.At = *t
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
