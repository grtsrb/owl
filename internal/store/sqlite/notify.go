package sqlite

import (
	"context"
	"database/sql"
	"time"

	"owl/internal/store"
)

func (d *DB) NotifyState(ctx context.Context) (*store.NotifyState, error) {
	st := &store.NotifyState{Critical: []store.CriticalMark{}, Clusters: map[string]bool{}, Repos: map[string]bool{}, Meta: map[string]string{}}
	rows, err := d.db.QueryContext(ctx, "SELECT component, cluster, since FROM notify_critical ORDER BY component, cluster")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var m store.CriticalMark
		var since string
		if err := rows.Scan(&m.Component, &m.Cluster, &since); err != nil {
			rows.Close()
			return nil, err
		}
		if t := parseTime(sql.NullString{String: since, Valid: true}); t != nil {
			m.Since = *t
		}
		st.Critical = append(st.Critical, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for q, into := range map[string]map[string]bool{
		"SELECT cluster_key FROM notify_clusters": st.Clusters,
		"SELECT repository FROM notify_repos":     st.Repos,
	} {
		rows, err := d.db.QueryContext(ctx, q)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				rows.Close()
				return nil, err
			}
			into[k] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	rows, err = d.db.QueryContext(ctx, "SELECT key, value FROM notify_meta")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		st.Meta[k] = v
	}
	return st, rows.Err()
}

func (d *DB) ApplyNotifyDelta(ctx context.Context, delta store.NotifyDelta) error {
	at := delta.At
	if at.IsZero() {
		at = now()
	}
	atS := at.UTC().Format(ts)
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	exec := func(q string, args ...any) error {
		_, err := tx.ExecContext(ctx, q, args...)
		return err
	}
	for _, m := range delta.AddCritical {
		since := m.Since
		if since.IsZero() {
			since = at
		}
		if err := exec("INSERT OR IGNORE INTO notify_critical(component, cluster, since) VALUES (?,?,?)",
			m.Component, m.Cluster, since.UTC().Format(ts)); err != nil {
			return err
		}
	}
	for _, m := range delta.Resolved {
		if err := exec("DELETE FROM notify_critical WHERE component=? AND cluster=?", m.Component, m.Cluster); err != nil {
			return err
		}
		if err := exec("INSERT INTO notify_resolved(component, cluster, at) VALUES (?,?,?)", m.Component, m.Cluster, atS); err != nil {
			return err
		}
	}
	for _, m := range delta.Dropped {
		if err := exec("DELETE FROM notify_critical WHERE component=? AND cluster=?", m.Component, m.Cluster); err != nil {
			return err
		}
	}
	for _, k := range delta.Clusters {
		if err := exec("INSERT OR IGNORE INTO notify_clusters(cluster_key, announced_at) VALUES (?,?)", k, atS); err != nil {
			return err
		}
	}
	for _, k := range delta.DropClusters {
		if err := exec("DELETE FROM notify_clusters WHERE cluster_key=?", k); err != nil {
			return err
		}
	}
	for _, r := range delta.Repos {
		if err := exec("INSERT OR IGNORE INTO notify_repos(repository, first_at) VALUES (?,?)", r, atS); err != nil {
			return err
		}
	}
	for k, v := range delta.Meta {
		if err := exec(`INSERT INTO notify_meta(key, value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, k, v); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (d *DB) SetNotifyMeta(ctx context.Context, key, value string) error {
	return d.ApplyNotifyDelta(ctx, store.NotifyDelta{Meta: map[string]string{key: value}})
}

func (d *DB) ListResolved(ctx context.Context, since time.Time) ([]store.ResolvedMark, error) {
	rows, err := d.db.QueryContext(ctx, "SELECT component, cluster, at FROM notify_resolved WHERE at >= ? ORDER BY at, id",
		since.UTC().Format(ts))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.ResolvedMark{}
	for rows.Next() {
		var m store.ResolvedMark
		var at string
		if err := rows.Scan(&m.Component, &m.Cluster, &at); err != nil {
			return nil, err
		}
		if t := parseTime(sql.NullString{String: at, Valid: true}); t != nil {
			m.At = *t
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (d *DB) PruneResolved(ctx context.Context, before time.Time) error {
	_, err := d.db.ExecContext(ctx, "DELETE FROM notify_resolved WHERE at < ?", before.UTC().Format(ts))
	return err
}

func (d *DB) SetDigestEmail(ctx context.Context, userID int64, on bool) error {
	res, err := d.db.ExecContext(ctx, "UPDATE users SET digest_email = ? WHERE id = ?", on, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}
