package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"owl/internal/store"
)

const apiTokenCols = "t.id, t.user_id, u.email, t.name, t.prefix, t.scope, t.created_at, t.expires_at, t.last_used_at, t.warned_at"

func scanAPIToken(row interface{ Scan(...any) error }) (*store.APIToken, error) {
	var t store.APIToken
	var created string
	var expires, used, warned sql.NullString
	if err := row.Scan(&t.ID, &t.UserID, &t.UserEmail, &t.Name, &t.Prefix, &t.Scope, &created, &expires, &used, &warned); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	if c := parseTime(sql.NullString{String: created, Valid: true}); c != nil {
		t.CreatedAt = *c
	}
	t.ExpiresAt, t.LastUsedAt, t.WarnedAt = parseTime(expires), parseTime(used), parseTime(warned)
	return &t, nil
}

func (d *DB) CreateAPIToken(ctx context.Context, t store.APIToken, hash string) (*store.APIToken, error) {
	if !t.Scope.Valid() || t.Name == "" || hash == "" {
		return nil, store.ErrInvalid
	}
	var expires any
	if t.ExpiresAt != nil {
		expires = t.ExpiresAt.UTC().Format(ts)
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// An expired token (still listed for 30 days) doesn't block its name: the replacement the expiry
	// email asks for usually has the same one.
	if _, err := tx.ExecContext(ctx, "DELETE FROM api_tokens WHERE user_id = ? AND name = ? AND expires_at <= ?", t.UserID, t.Name, now().Format(ts)); err != nil {
		return nil, err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO api_tokens(user_id, name, token_hash, prefix, scope, created_at, expires_at)
		VALUES (?,?,?,?,?,?,?)`, t.UserID, t.Name, hash, t.Prefix, string(t.Scope), now().Format(ts), expires)
	if isUnique(err) {
		return nil, store.ErrExists
	} else if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	created, err := scanAPIToken(tx.QueryRowContext(ctx, "SELECT "+apiTokenCols+" FROM api_tokens t JOIN users u ON u.id = t.user_id WHERE t.id = ?", id))
	if err != nil {
		return nil, err
	}
	return created, tx.Commit()
}

func (d *DB) listAPITokens(ctx context.Context, where string, args ...any) ([]store.APIToken, error) {
	rows, err := d.db.QueryContext(ctx, "SELECT "+apiTokenCols+" FROM api_tokens t JOIN users u ON u.id = t.user_id "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.APIToken{}
	for rows.Next() {
		t, err := scanAPIToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func (d *DB) ListAPITokens(ctx context.Context, userID int64) ([]store.APIToken, error) {
	if userID == 0 {
		return d.listAPITokens(ctx, "ORDER BY u.email, t.name")
	}
	return d.listAPITokens(ctx, "WHERE t.user_id = ? ORDER BY t.name", userID)
}

func (d *DB) APITokenByHash(ctx context.Context, hash string) (*store.APIToken, *store.User, error) {
	t, err := scanAPIToken(d.db.QueryRowContext(ctx, "SELECT "+apiTokenCols+" FROM api_tokens t JOIN users u ON u.id = t.user_id WHERE t.token_hash = ?", hash))
	if err != nil {
		return nil, nil, err
	}
	u, err := d.GetUser(ctx, t.UserID)
	if err != nil {
		return nil, nil, err
	}
	return t, u, nil
}

func (d *DB) TouchAPIToken(ctx context.Context, id int64) error {
	_, err := d.db.ExecContext(ctx, "UPDATE api_tokens SET last_used_at = ? WHERE id = ?", now().Format(ts), id)
	return err
}

func (d *DB) DeleteAPIToken(ctx context.Context, id, userID int64) (*store.APIToken, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	t, err := scanAPIToken(tx.QueryRowContext(ctx, "SELECT "+apiTokenCols+" FROM api_tokens t JOIN users u ON u.id = t.user_id WHERE t.id = ?", id))
	if err != nil {
		return nil, err
	}
	if userID != 0 && t.UserID != userID {
		return nil, store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM api_tokens WHERE id = ?", id); err != nil {
		return nil, err
	}
	return t, tx.Commit()
}

// ExpiringAPITokens: tokens of active users that expire between now and before and weren't warned.
func (d *DB) ExpiringAPITokens(ctx context.Context, before time.Time) ([]store.APIToken, error) {
	return d.listAPITokens(ctx, `WHERE t.warned_at IS NULL AND t.expires_at IS NOT NULL AND t.expires_at > ? AND t.expires_at <= ?
		AND u.activated_at IS NOT NULL AND u.disabled_at IS NULL ORDER BY t.expires_at`, now().Format(ts), before.UTC().Format(ts))
}

func (d *DB) MarkAPITokenWarned(ctx context.Context, id int64) error {
	_, err := d.db.ExecContext(ctx, "UPDATE api_tokens SET warned_at = ? WHERE id = ?", now().Format(ts), id)
	return err
}
