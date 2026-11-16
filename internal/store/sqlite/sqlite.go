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
var schemaV1 string

// migrations[i] upgrades the database from user_version i to i+1. Append only — never edit a
// migration that has shipped.
var migrations = []string{
	schemaV1,
	`CREATE TABLE upstream_cache (
		key        TEXT PRIMARY KEY,
		fetched_at TEXT NOT NULL,
		etag       TEXT NOT NULL DEFAULT '',
		body       BLOB NOT NULL
	);`,
	// scope: "global" or a component id.
	`CREATE TABLE policies (
		scope      TEXT PRIMARY KEY,
		spec       TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);`,
	// User-defined stages and tenants. clusters is rebuilt (SQLite can't drop a CHECK constraint) so
	// stage/tenant reference those tables: renaming a tenant cascades, deleting one in use fails.
	// Runs with foreign_keys=OFF (see migrate), so DROP TABLE doesn't cascade into the child tables.
	`CREATE TABLE stages (
		name     TEXT PRIMARY KEY,
		position INTEGER NOT NULL
	);
	INSERT INTO stages(name, position) VALUES ('staging', 1), ('prelive', 2), ('live', 3);
	CREATE TABLE tenants (name TEXT PRIMARY KEY);
	INSERT OR IGNORE INTO tenants(name) SELECT DISTINCT tenant FROM clusters;
	CREATE TABLE clusters_new (
		id                 INTEGER PRIMARY KEY,
		name               TEXT NOT NULL UNIQUE,
		stage              TEXT NOT NULL REFERENCES stages(name) ON UPDATE CASCADE,
		tenant             TEXT NOT NULL REFERENCES tenants(name) ON UPDATE CASCADE,
		created_at         TEXT NOT NULL,
		last_seen_at       TEXT,
		kubernetes_version TEXT NOT NULL DEFAULT '',
		agent_version      TEXT NOT NULL DEFAULT ''
	);
	INSERT INTO clusters_new SELECT id, name, stage, tenant, created_at, last_seen_at, kubernetes_version, agent_version FROM clusters;
	DROP TABLE clusters;
	ALTER TABLE clusters_new RENAME TO clusters;
	CREATE TABLE catalog_overrides (
		repository TEXT PRIMARY KEY,
		kind       TEXT NOT NULL CHECK (kind IN ('map', 'inhouse', 'ignore')),
		component  TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL
	);
	CREATE TABLE settings (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);`,
}

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

// migrate runs pending migrations on one pinned connection with foreign keys off (required to
// rebuild a referenced table, see migration 4) and checks foreign keys before turning them back on.
func migrate(db *sql.DB) error {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var v int
	if err := conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	if v > len(migrations) {
		return fmt.Errorf("database schema v%d is newer than this binary supports (v%d)", v, len(migrations))
	}
	if v == len(migrations) {
		return nil
	}
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		return err
	}
	defer conn.ExecContext(ctx, "PRAGMA foreign_keys = ON")
	for ; v < len(migrations); v++ {
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[v]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration to v%d: %w", v+1, err)
		}
		if err := foreignKeyCheck(ctx, tx); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration to v%d: %w", v+1, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func foreignKeyCheck(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		var table string
		var rowid sql.NullInt64
		var parent string
		var fkid int
		_ = rows.Scan(&table, &rowid, &parent, &fkid)
		return fmt.Errorf("foreign key violation: %s row %v -> %s", table, rowid.Int64, parent)
	}
	return rows.Err()
}

func (d *DB) GetCache(ctx context.Context, key string) (*store.CacheEntry, error) {
	var e store.CacheEntry
	var fetched string
	err := d.db.QueryRowContext(ctx, "SELECT key, fetched_at, etag, body FROM upstream_cache WHERE key=?", key).
		Scan(&e.Key, &fetched, &e.ETag, &e.Body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	} else if err != nil {
		return nil, err
	}
	if t := parseTime(sql.NullString{String: fetched, Valid: true}); t != nil {
		e.FetchedAt = *t
	}
	return &e, nil
}

func (d *DB) PutCache(ctx context.Context, e store.CacheEntry) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO upstream_cache(key, fetched_at, etag, body) VALUES (?,?,?,?)
		ON CONFLICT(key) DO UPDATE SET fetched_at=excluded.fetched_at, etag=excluded.etag, body=excluded.body`,
		e.Key, e.FetchedAt.UTC().Format(ts), e.ETag, e.Body)
	return err
}

func (d *DB) Close() error { return d.db.Close() }

func now() time.Time { return time.Now().UTC().Truncate(time.Millisecond) }

// ts is fixed-width (always milliseconds, always UTC "Z") so text order == time order in SQL.
const ts = "2006-01-02T15:04:05.000Z07:00"

func parseTime(s sql.NullString) *time.Time {
	if !s.Valid || s.String == "" {
		return nil
	}
	t, err := time.Parse(ts, s.String)
	if err != nil {
		if t, err = time.Parse(time.RFC3339Nano, s.String); err != nil {
			return nil
		}
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
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := stageExists(ctx, tx, string(stage)); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO tenants(name) VALUES (?)", tenant); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO clusters(name, stage, tenant, created_at) VALUES (?,?,?,?)",
		name, string(stage), tenant, now().Format(ts)); err != nil {
		if isUnique(err) {
			return nil, store.ErrExists
		}
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return d.GetCluster(ctx, name)
}

func (d *DB) UpdateCluster(ctx context.Context, name string, stage store.Stage, tenant string) (*store.Cluster, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := stageExists(ctx, tx, string(stage)); err != nil {
		return nil, err
	}
	var n int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM tenants WHERE name=?", tenant).Scan(&n); err != nil {
		return nil, err
	} else if n == 0 {
		return nil, fmt.Errorf("tenant %q: %w", tenant, store.ErrNotFound)
	}
	res, err := tx.ExecContext(ctx, "UPDATE clusters SET stage=?, tenant=? WHERE name=?", string(stage), tenant, name)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, store.ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return d.GetCluster(ctx, name)
}

func stageExists(ctx context.Context, q interface {
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}, name string) error {
	var n int
	if err := q.QueryRowContext(ctx, "SELECT count(*) FROM stages WHERE name=?", name).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w %q", store.ErrUnknownStage, name)
	}
	return nil
}

func isUnique(err error) bool { return err != nil && strings.Contains(err.Error(), "UNIQUE") }

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

func (d *DB) ListPolicies(ctx context.Context) (map[string]string, error) {
	rows, err := d.db.QueryContext(ctx, "SELECT scope, spec FROM policies")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var scope, spec string
		if err := rows.Scan(&scope, &spec); err != nil {
			return nil, err
		}
		out[scope] = spec
	}
	return out, rows.Err()
}

func (d *DB) SetPolicy(ctx context.Context, scope, spec string) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO policies(scope, spec, updated_at) VALUES (?,?,?)
		ON CONFLICT(scope) DO UPDATE SET spec=excluded.spec, updated_at=excluded.updated_at`, scope, spec, now().Format(ts))
	return err
}

func (d *DB) DeletePolicy(ctx context.Context, scope string) error {
	_, err := d.db.ExecContext(ctx, "DELETE FROM policies WHERE scope=?", scope)
	return err
}

func (d *DB) AllChanges(ctx context.Context, clusterID int64) ([]store.Change, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT ch.id, ch.cluster_id, c.name, ch.at, ch.type, ch.namespace, ch.kind,
		ch.workload, ch.container, ch.old_value, ch.new_value FROM changes ch JOIN clusters c ON c.id=ch.cluster_id
		WHERE ch.cluster_id=? ORDER BY ch.at, ch.id`, clusterID)
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

func (d *DB) ListStages(ctx context.Context) ([]store.StageInfo, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT s.name, s.position, (SELECT count(*) FROM clusters c WHERE c.stage=s.name)
		FROM stages s ORDER BY s.position, s.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.StageInfo{}
	for rows.Next() {
		var st store.StageInfo
		if err := rows.Scan(&st.Name, &st.Position, &st.Clusters); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func (d *DB) CreateStage(ctx context.Context, name string) error {
	_, err := d.db.ExecContext(ctx,
		"INSERT INTO stages(name, position) VALUES (?, (SELECT coalesce(max(position), 0) + 1 FROM stages))", name)
	if isUnique(err) {
		return store.ErrExists
	}
	return err
}

func (d *DB) DeleteStage(ctx context.Context, name string) error {
	return d.deleteUnused(ctx, "stages", "stage", name)
}

// deleteUnused deletes a stage/tenant row unless a cluster still uses it.
func (d *DB) deleteUnused(ctx context.Context, table, col, name string) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var used int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM clusters WHERE "+col+"=?", name).Scan(&used); err != nil {
		return err
	}
	if used > 0 {
		return fmt.Errorf("%w by %d cluster(s)", store.ErrInUse, used)
	}
	res, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE name=?", name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return tx.Commit()
}

func (d *DB) ReorderStages(ctx context.Context, names []string) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var total int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM stages").Scan(&total); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, n := range names {
		seen[n] = true
	}
	if len(names) != total || len(seen) != total {
		return fmt.Errorf("reorder needs every stage exactly once (%d stages, got %d names)", total, len(names))
	}
	for i, n := range names {
		res, err := tx.ExecContext(ctx, "UPDATE stages SET position=? WHERE name=?", i+1, n)
		if err != nil {
			return err
		}
		if c, _ := res.RowsAffected(); c == 0 {
			return fmt.Errorf("%w %q", store.ErrUnknownStage, n)
		}
	}
	return tx.Commit()
}

func (d *DB) ListTenants(ctx context.Context) ([]store.TenantInfo, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT t.name, (SELECT count(*) FROM clusters c WHERE c.tenant=t.name)
		FROM tenants t ORDER BY t.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.TenantInfo{}
	for rows.Next() {
		var t store.TenantInfo
		if err := rows.Scan(&t.Name, &t.Clusters); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (d *DB) CreateTenant(ctx context.Context, name string) error {
	_, err := d.db.ExecContext(ctx, "INSERT INTO tenants(name) VALUES (?)", name)
	if isUnique(err) {
		return store.ErrExists
	}
	return err
}

// RenameTenant relies on ON UPDATE CASCADE to move the clusters along.
func (d *DB) RenameTenant(ctx context.Context, from, to string) error {
	res, err := d.db.ExecContext(ctx, "UPDATE tenants SET name=? WHERE name=?", to, from)
	if isUnique(err) {
		return store.ErrExists
	} else if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (d *DB) DeleteTenant(ctx context.Context, name string) error {
	return d.deleteUnused(ctx, "tenants", "tenant", name)
}

func (d *DB) ListOverrides(ctx context.Context) ([]store.CatalogOverride, error) {
	rows, err := d.db.QueryContext(ctx, "SELECT repository, kind, component, updated_at FROM catalog_overrides ORDER BY repository")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.CatalogOverride{}
	for rows.Next() {
		var o store.CatalogOverride
		var at string
		if err := rows.Scan(&o.Repository, &o.Kind, &o.Component, &at); err != nil {
			return nil, err
		}
		if t := parseTime(sql.NullString{String: at, Valid: true}); t != nil {
			o.UpdatedAt = *t
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (d *DB) SetOverride(ctx context.Context, o store.CatalogOverride) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO catalog_overrides(repository, kind, component, updated_at) VALUES (?,?,?,?)
		ON CONFLICT(repository) DO UPDATE SET kind=excluded.kind, component=excluded.component, updated_at=excluded.updated_at`,
		o.Repository, string(o.Kind), o.Component, now().Format(ts))
	return err
}

func (d *DB) DeleteOverride(ctx context.Context, repository string) error {
	res, err := d.db.ExecContext(ctx, "DELETE FROM catalog_overrides WHERE repository=?", repository)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (d *DB) GetSettings(ctx context.Context) (map[string]string, error) {
	rows, err := d.db.QueryContext(ctx, "SELECT key, value FROM settings")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

func (d *DB) SetSetting(ctx context.Context, key, value string) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES (?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}
