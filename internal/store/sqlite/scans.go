package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"owl/internal/store"
)

func fmtTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(ts)
}

func (d *DB) ListImageScans(ctx context.Context) ([]store.ImageScan, error) {
	rows, err := d.db.QueryContext(ctx, "SELECT digest, ref, scanned_at, result, error, attempt_at FROM image_scans ORDER BY digest")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.ImageScan{}
	for rows.Next() {
		var s store.ImageScan
		var scanned sql.NullString
		var result, attempt string
		if err := rows.Scan(&s.Digest, &s.Ref, &scanned, &result, &s.Error, &attempt); err != nil {
			return nil, err
		}
		s.ScannedAt, s.Result = parseTime(scanned), []byte(result)
		if t := parseTime(sql.NullString{String: attempt, Valid: true}); t != nil {
			s.AttemptAt = *t
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (d *DB) PutImageScan(ctx context.Context, s store.ImageScan) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO image_scans(digest, ref, scanned_at, result, error, attempt_at) VALUES (?,?,?,?,?,?)
		ON CONFLICT(digest) DO UPDATE SET ref=excluded.ref, scanned_at=excluded.scanned_at, result=excluded.result,
		error=excluded.error, attempt_at=excluded.attempt_at`,
		s.Digest, s.Ref, fmtTime(s.ScannedAt), string(s.Result), s.Error, s.AttemptAt.UTC().Format(ts))
	return err
}

func (d *DB) DeleteImageScansExcept(ctx context.Context, keep []string) (int64, error) {
	q, args := "DELETE FROM image_scans", []any{}
	if len(keep) > 0 {
		q += " WHERE digest NOT IN (" + strings.TrimSuffix(strings.Repeat("?,", len(keep)), ",") + ")"
		for _, k := range keep {
			args = append(args, k)
		}
	}
	res, err := d.db.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (d *DB) ListVulnAcceptances(ctx context.Context) ([]store.VulnAcceptance, error) {
	rows, err := d.db.QueryContext(ctx, "SELECT repository, vuln_id, reason, expires_at, created_by, created_at FROM vuln_acceptances ORDER BY repository, vuln_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.VulnAcceptance{}
	for rows.Next() {
		var a store.VulnAcceptance
		var exp sql.NullString
		var created string
		if err := rows.Scan(&a.Repository, &a.VulnID, &a.Reason, &exp, &a.CreatedBy, &created); err != nil {
			return nil, err
		}
		a.ExpiresAt = parseTime(exp)
		if t := parseTime(sql.NullString{String: created, Valid: true}); t != nil {
			a.CreatedAt = *t
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (d *DB) PutVulnAcceptance(ctx context.Context, a store.VulnAcceptance) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO vuln_acceptances(repository, vuln_id, reason, expires_at, created_by, created_at) VALUES (?,?,?,?,?,?)
		ON CONFLICT(repository, vuln_id) DO UPDATE SET reason=excluded.reason, expires_at=excluded.expires_at,
		created_by=excluded.created_by, created_at=excluded.created_at`,
		a.Repository, a.VulnID, a.Reason, fmtTime(a.ExpiresAt), a.CreatedBy, now().Format(ts))
	return err
}

func (d *DB) DeleteVulnAcceptance(ctx context.Context, repository, vulnID string) error {
	res, err := d.db.ExecContext(ctx, "DELETE FROM vuln_acceptances WHERE repository=? AND vuln_id=?", repository, vulnID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (d *DB) ListRegistryLogins(ctx context.Context) ([]store.RegistryLogin, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT id, name, username, secret, seal_id, expires_at, updated_by, created_at, updated_at
		FROM registry_logins ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.RegistryLogin{}
	idx := map[int64]int{}
	for rows.Next() {
		var l store.RegistryLogin
		var exp sql.NullString
		var created, updated string
		if err := rows.Scan(&l.ID, &l.Name, &l.Username, &l.Secret, &l.SealID, &exp, &l.UpdatedBy, &created, &updated); err != nil {
			return nil, err
		}
		l.ExpiresAt = parseTime(exp)
		if t := parseTime(sql.NullString{String: created, Valid: true}); t != nil {
			l.CreatedAt = *t
		}
		if t := parseTime(sql.NullString{String: updated, Valid: true}); t != nil {
			l.UpdatedAt = *t
		}
		l.Scopes = []string{}
		idx[l.ID] = len(out)
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	srows, err := d.db.QueryContext(ctx, "SELECT prefix, login_id FROM registry_scopes ORDER BY prefix")
	if err != nil {
		return nil, err
	}
	defer srows.Close()
	for srows.Next() {
		var p string
		var id int64
		if err := srows.Scan(&p, &id); err != nil {
			return nil, err
		}
		if i, ok := idx[id]; ok {
			out[i].Scopes = append(out[i].Scopes, p)
		}
	}
	return out, srows.Err()
}

func (d *DB) AddRegistryLogin(ctx context.Context, l store.RegistryLogin) (int64, error) {
	if len(l.Scopes) == 0 {
		return 0, fmt.Errorf("%w: a login needs at least one prefix", store.ErrInvalid)
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	t := now().Format(ts)
	res, err := tx.ExecContext(ctx, `INSERT INTO registry_logins(name, username, secret, seal_id, expires_at, updated_by, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?)`, l.Name, l.Username, l.Secret, l.SealID, fmtTime(l.ExpiresAt), l.UpdatedBy, t, t)
	if isUnique(err) {
		return 0, store.ErrExists
	} else if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	for _, p := range l.Scopes {
		if _, err := tx.ExecContext(ctx, "INSERT INTO registry_scopes(prefix, login_id) VALUES (?,?)", p, id); isUnique(err) {
			return 0, store.ErrExists
		} else if err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}

func (d *DB) UpdateRegistryLogin(ctx context.Context, l store.RegistryLogin) error {
	res, err := d.db.ExecContext(ctx, `UPDATE registry_logins SET name=?, username=?, secret=?, seal_id=?, expires_at=?, updated_by=?, updated_at=?
		WHERE id=?`, l.Name, l.Username, l.Secret, l.SealID, fmtTime(l.ExpiresAt), l.UpdatedBy, now().Format(ts), l.ID)
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

func (d *DB) DeleteRegistryLogin(ctx context.Context, id int64) error {
	res, err := d.db.ExecContext(ctx, "DELETE FROM registry_logins WHERE id=?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (d *DB) AddRegistryScope(ctx context.Context, loginID int64, prefix string) error {
	_, err := d.db.ExecContext(ctx, "INSERT INTO registry_scopes(prefix, login_id) VALUES (?,?)", prefix, loginID)
	switch {
	case isUnique(err):
		return store.ErrExists
	case isForeignKey(err):
		return store.ErrNotFound
	}
	return err
}

func (d *DB) DeleteRegistryScope(ctx context.Context, prefix string) error {
	res, err := d.db.ExecContext(ctx, "DELETE FROM registry_scopes WHERE prefix=?", prefix)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}
