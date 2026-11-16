package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"owl/internal/store"
)

func (d *DB) AddAudit(ctx context.Context, e store.AuditEntry) error {
	_, err := d.db.ExecContext(ctx, "INSERT INTO audit_log(at, action, details, remote, actor) VALUES (?,?,?,?,?)",
		now().Format(ts), e.Action, e.Details, e.Remote, e.Actor)
	return err
}

func (d *DB) ListAudit(ctx context.Context, limit int) ([]store.AuditEntry, error) {
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	rows, err := d.db.QueryContext(ctx, "SELECT id, at, action, details, remote, actor FROM audit_log ORDER BY at DESC, id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.AuditEntry{}
	for rows.Next() {
		var e store.AuditEntry
		var at string
		if err := rows.Scan(&e.ID, &at, &e.Action, &e.Details, &e.Remote, &e.Actor); err != nil {
			return nil, err
		}
		if t := parseTime(sql.NullString{String: at, Valid: true}); t != nil {
			e.At = *t
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (d *DB) PruneAudit(ctx context.Context, before time.Time) (int64, error) {
	res, err := d.db.ExecContext(ctx, "DELETE FROM audit_log WHERE at < ?", before.UTC().Format(ts))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Backup uses VACUUM INTO: a consistent snapshot even while agents are writing. The upstream cache
// (most of the file, and refetchable) is then dropped from the copy.
func (d *DB) Backup(ctx context.Context, path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("backup target %s already exists", path)
	}
	if _, err := d.db.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
		return fmt.Errorf("vacuum into: %w", err)
	}
	cp, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		return err
	}
	defer cp.Close()
	cp.SetMaxOpenConns(1)
	for _, q := range []string{"DELETE FROM upstream_cache", "VACUUM"} {
		if _, err := cp.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("trim backup: %w", err)
		}
	}
	return nil
}

// tables restored from a backup; audit_log, revoked_tokens and upstream_cache stay as they are.
var restoreTables = []string{"stages", "tenants", "clusters", "agent_tokens", "current_images", "changes",
	"policies", "catalog_overrides", "settings"}

// Restore migrates a copy of the backup to the current schema (Open does that, and refuses a newer
// schema), then replaces the restorable tables in one transaction.
func (d *DB) Restore(ctx context.Context, path string) error {
	bak, err := Open(path) // migrates in place: callers pass a private temp copy
	if err != nil {
		return fmt.Errorf("%w: %v", store.ErrInvalid, err)
	}
	var n int
	err = bak.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='clusters'").Scan(&n)
	bak.Close()
	if err != nil || n == 0 {
		return fmt.Errorf("%w: not an Owl backup (no clusters table)", store.ErrInvalid)
	}

	conn, err := d.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), "PRAGMA foreign_keys = ON")
	if _, err := conn.ExecContext(ctx, "ATTACH DATABASE ? AS bak", path); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), "DETACH DATABASE bak")

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Children first when deleting, parents first when inserting.
	for i := len(restoreTables) - 1; i >= 0; i-- {
		if _, err := tx.ExecContext(ctx, "DELETE FROM main."+restoreTables[i]); err != nil {
			return err
		}
	}
	for _, t := range restoreTables {
		if _, err := tx.ExecContext(ctx, "INSERT INTO main."+t+" SELECT * FROM bak."+t); err != nil {
			return fmt.Errorf("restore %s: %w", t, err)
		}
	}
	// Revocations made after the backup stay in force (revoked_tokens isn't restored).
	if _, err := tx.ExecContext(ctx, `UPDATE main.agent_tokens SET revoked_at =
		(SELECT r.revoked_at FROM main.revoked_tokens r WHERE r.token_hash = agent_tokens.token_hash)
		WHERE revoked_at IS NULL AND token_hash IN (SELECT token_hash FROM main.revoked_tokens)`); err != nil {
		return err
	}
	if err := foreignKeyCheck(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) ImportCluster(ctx context.Context, imp store.ClusterImport, hash, prefix string) (*store.Cluster, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO stages(name, position) VALUES (?, (SELECT coalesce(max(position), 0) + 1 FROM stages))", imp.Stage); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO tenants(name) VALUES (?)", imp.Tenant); err != nil {
		return nil, err
	}
	created := imp.CreatedAt
	if created.IsZero() {
		created = now()
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO clusters(name, stage, tenant, created_at, kubernetes_version, agent_version)
		VALUES (?,?,?,?,?,?)`, imp.Name, imp.Stage, imp.Tenant, created.UTC().Format(ts), imp.KubernetesVersion, imp.AgentVersion)
	if isUnique(err) {
		return nil, store.ErrExists
	} else if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if _, err := tx.ExecContext(ctx, "INSERT INTO agent_tokens(cluster_id, token_hash, prefix, created_at) VALUES (?,?,?,?)",
		id, hash, prefix, now().Format(ts)); err != nil {
		return nil, err
	}
	for _, r := range imp.Images {
		running, _ := json.Marshal(r.Running)
		if _, err := tx.ExecContext(ctx, `INSERT INTO current_images
			(cluster_id, namespace, kind, workload, container, image, is_init, desired_replicas, running) VALUES (?,?,?,?,?,?,?,?,?)`,
			id, r.Namespace, r.Kind, r.Workload, r.Container, r.Image, r.Init, r.DesiredReplicas, string(running)); err != nil {
			return nil, fmt.Errorf("import images: %w", err)
		}
	}
	for _, c := range imp.Changes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO changes
			(cluster_id, at, type, namespace, kind, workload, container, old_value, new_value) VALUES (?,?,?,?,?,?,?,?,?)`,
			id, c.At.UTC().Format(ts), string(c.Type), c.Namespace, c.Kind, c.Workload, c.Container, c.OldValue, c.NewValue); err != nil {
			return nil, fmt.Errorf("import history: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return d.GetCluster(ctx, imp.Name)
}
