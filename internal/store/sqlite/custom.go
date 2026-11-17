package sqlite

import (
	"context"
	"database/sql"

	"owl/internal/store"
)

func (d *DB) ListCustomComponents(ctx context.Context) ([]store.CustomComponent, error) {
	rows, err := d.db.QueryContext(ctx, "SELECT id, data, created_at, updated_at FROM custom_components ORDER BY created_at, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.CustomComponent{}
	for rows.Next() {
		var c store.CustomComponent
		var data, created, updated string
		if err := rows.Scan(&c.ID, &data, &created, &updated); err != nil {
			return nil, err
		}
		c.Data = []byte(data)
		if t := parseTime(sql.NullString{String: created, Valid: true}); t != nil {
			c.CreatedAt = *t
		}
		if t := parseTime(sql.NullString{String: updated, Valid: true}); t != nil {
			c.UpdatedAt = *t
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (d *DB) CreateCustomComponent(ctx context.Context, id string, data []byte) error {
	t := now().Format(ts)
	_, err := d.db.ExecContext(ctx, "INSERT INTO custom_components(id, data, created_at, updated_at) VALUES (?,?,?,?)", id, string(data), t, t)
	if isUnique(err) {
		return store.ErrExists
	}
	return err
}

func (d *DB) UpdateCustomComponent(ctx context.Context, id string, data []byte) error {
	res, err := d.db.ExecContext(ctx, "UPDATE custom_components SET data=?, updated_at=? WHERE id=?", string(data), now().Format(ts), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}

type queryer interface {
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
}

func customImpact(ctx context.Context, q queryer, id string) (*store.CustomDeleted, error) {
	out := &store.CustomDeleted{Overrides: []string{}}
	var n int
	if err := q.QueryRowContext(ctx, "SELECT count(*) FROM custom_components WHERE id=?", id).Scan(&n); err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, store.ErrNotFound
	}
	if err := q.QueryRowContext(ctx, "SELECT count(*) FROM policies WHERE scope=?", id).Scan(&n); err != nil {
		return nil, err
	}
	out.Policy = n > 0
	rows, err := q.QueryContext(ctx, "SELECT repository FROM catalog_overrides WHERE kind='map' AND component=? ORDER BY repository", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		out.Overrides = append(out.Overrides, r)
	}
	return out, rows.Err()
}

func (d *DB) CustomDeleteImpact(ctx context.Context, id string) (*store.CustomDeleted, error) {
	return customImpact(ctx, d.db, id)
}

func (d *DB) DeleteCustomComponent(ctx context.Context, id string, cascade bool) (*store.CustomDeleted, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	out, err := customImpact(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	qs := []string{"DELETE FROM custom_components WHERE id=?"}
	if cascade {
		qs = append(qs, "DELETE FROM policies WHERE scope=?", "DELETE FROM catalog_overrides WHERE kind='map' AND component=?")
	} else {
		out = &store.CustomDeleted{Overrides: []string{}}
	}
	for _, q := range qs {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}
