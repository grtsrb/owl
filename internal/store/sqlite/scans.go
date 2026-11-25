package sqlite

import (
	"context"
	"database/sql"
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

func (d *DB) ListRegistryCredentials(ctx context.Context) ([]store.RegistryCredential, error) {
	rows, err := d.db.QueryContext(ctx, "SELECT prefix, username, secret, expires_at, updated_by, created_at, updated_at FROM registry_credentials ORDER BY prefix")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.RegistryCredential{}
	for rows.Next() {
		var c store.RegistryCredential
		var exp sql.NullString
		var created, updated string
		if err := rows.Scan(&c.Prefix, &c.Username, &c.Secret, &exp, &c.UpdatedBy, &created, &updated); err != nil {
			return nil, err
		}
		c.ExpiresAt = parseTime(exp)
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

func (d *DB) AddRegistryCredential(ctx context.Context, c store.RegistryCredential) error {
	t := now().Format(ts)
	_, err := d.db.ExecContext(ctx, `INSERT INTO registry_credentials(prefix, username, secret, expires_at, updated_by, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?)`, c.Prefix, c.Username, c.Secret, fmtTime(c.ExpiresAt), c.UpdatedBy, t, t)
	if isUnique(err) {
		return store.ErrExists
	}
	return err
}

func (d *DB) UpdateRegistryCredential(ctx context.Context, c store.RegistryCredential) error {
	res, err := d.db.ExecContext(ctx, `UPDATE registry_credentials SET username=?, secret=?, expires_at=?, updated_by=?, updated_at=?
		WHERE prefix=?`, c.Username, c.Secret, fmtTime(c.ExpiresAt), c.UpdatedBy, now().Format(ts), c.Prefix)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (d *DB) DeleteRegistryCredential(ctx context.Context, prefix string) error {
	res, err := d.db.ExecContext(ctx, "DELETE FROM registry_credentials WHERE prefix=?", prefix)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}
