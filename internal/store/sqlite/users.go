package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"owl/internal/store"
)

const userCols = "id, email, role, password_hash, coalesce(google_sub, ''), created_at, activated_at, last_login_at, disabled_at, digest_email"

// userColsU is userCols for a query that joins users as "u".
const userColsU = "u.id, u.email, u.role, u.password_hash, coalesce(u.google_sub, ''), u.created_at, u.activated_at, u.last_login_at, u.disabled_at, u.digest_email"

func scanUser(row interface{ Scan(...any) error }) (*store.User, error) {
	var u store.User
	var created string
	var activated, lastLogin, disabled sql.NullString
	if err := row.Scan(&u.ID, &u.Email, &u.Role, &u.PasswordHash, &u.GoogleSub, &created, &activated, &lastLogin, &disabled, &u.DigestEmail); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	if t := parseTime(sql.NullString{String: created, Valid: true}); t != nil {
		u.CreatedAt = *t
	}
	u.ActivatedAt, u.LastLoginAt, u.DisabledAt = parseTime(activated), parseTime(lastLogin), parseTime(disabled)
	u.HasPassword, u.HasGoogle = u.PasswordHash != "", u.GoogleSub != ""
	return &u, nil
}

func normEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

func (d *DB) CreateUser(ctx context.Context, email string, role store.Role) (*store.User, error) {
	if !role.Valid() {
		return nil, store.ErrInvalid
	}
	res, err := d.db.ExecContext(ctx, "INSERT INTO users(email, role, created_at) VALUES (?,?,?)",
		normEmail(email), string(role), now().Format(ts))
	if isUnique(err) {
		return nil, store.ErrExists
	} else if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return d.GetUser(ctx, id)
}

func (d *DB) GetUser(ctx context.Context, id int64) (*store.User, error) {
	return scanUser(d.db.QueryRowContext(ctx, "SELECT "+userCols+" FROM users WHERE id = ?", id))
}

func (d *DB) UserByEmail(ctx context.Context, email string) (*store.User, error) {
	return scanUser(d.db.QueryRowContext(ctx, "SELECT "+userCols+" FROM users WHERE email = ?", normEmail(email)))
}

func (d *DB) UserByGoogleSub(ctx context.Context, sub string) (*store.User, error) {
	if sub == "" {
		return nil, store.ErrNotFound
	}
	return scanUser(d.db.QueryRowContext(ctx, "SELECT "+userCols+" FROM users WHERE google_sub = ?", sub))
}

func (d *DB) ListUsers(ctx context.Context) ([]store.User, error) {
	rows, err := d.db.QueryContext(ctx, "SELECT "+userCols+" FROM users ORDER BY email")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

const activeAdmin = "role = 'admin' AND activated_at IS NOT NULL AND disabled_at IS NULL"

// keepsAnAdmin fails with ErrLastAdmin when u is an active admin and no other active admin exists.
func keepsAnAdmin(ctx context.Context, tx *sql.Tx, u *store.User) error {
	if u.Role != store.RoleAdmin || !u.Active() {
		return nil // not an active admin: changing it can't reduce their number
	}
	var others int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM users WHERE "+activeAdmin+" AND id != ?", u.ID).Scan(&others); err != nil {
		return err
	}
	if others == 0 {
		return store.ErrLastAdmin
	}
	return nil
}

func (d *DB) UpdateUser(ctx context.Context, id int64, role store.Role, disabled bool) (*store.User, error) {
	if !role.Valid() {
		return nil, store.ErrInvalid
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	u, err := scanUser(tx.QueryRowContext(ctx, "SELECT "+userCols+" FROM users WHERE id = ?", id))
	if err != nil {
		return nil, err
	}
	if role != store.RoleAdmin || disabled {
		if err := keepsAnAdmin(ctx, tx, u); err != nil {
			return nil, err
		}
	}
	at := now().Format(ts)
	if _, err := tx.ExecContext(ctx, `UPDATE users SET role = ?,
		disabled_at = CASE WHEN ? THEN coalesce(disabled_at, ?) ELSE NULL END WHERE id = ?`, string(role), disabled, at, id); err != nil {
		return nil, err
	}
	if disabled {
		if err := endSessions(ctx, tx, id, ""); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM auth_tokens WHERE user_id = ?", id); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return d.GetUser(ctx, id)
}

func (d *DB) DeleteUser(ctx context.Context, id int64) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	u, err := scanUser(tx.QueryRowContext(ctx, "SELECT "+userCols+" FROM users WHERE id = ?", id))
	if err != nil {
		return err
	}
	if err := keepsAnAdmin(ctx, tx, u); err != nil {
		return err
	}
	// Sessions and tokens go with the user (ON DELETE CASCADE).
	if _, err := tx.ExecContext(ctx, "DELETE FROM users WHERE id = ?", id); err != nil {
		return err
	}
	return tx.Commit()
}

func endSessions(ctx context.Context, tx *sql.Tx, userID int64, keep string) error {
	_, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ? AND token_hash != ?", userID, keep)
	return err
}

func (d *DB) SetPassword(ctx context.Context, id int64, hash, keepSession string) error {
	if hash == "" {
		return store.ErrInvalid
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, "UPDATE users SET password_hash = ?, activated_at = coalesce(activated_at, ?) WHERE id = ?",
		hash, now().Format(ts), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	// A new password ends every other session and voids outstanding links (a leaked reset link or a
	// stolen session doesn't outlive the password change).
	if err := endSessions(ctx, tx, id, keepSession); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM auth_tokens WHERE user_id = ?", id); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) LinkGoogle(ctx context.Context, id int64, sub string) error {
	if sub == "" {
		return store.ErrInvalid
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, "UPDATE users SET google_sub = ?, activated_at = coalesce(activated_at, ?) WHERE id = ? AND google_sub IS NULL",
		sub, now().Format(ts), id)
	if isUnique(err) {
		return store.ErrExists // that Google account already belongs to another user
	} else if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM auth_tokens WHERE user_id = ? AND kind = 'invite'", id); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) RecordLogin(ctx context.Context, id int64) error {
	_, err := d.db.ExecContext(ctx, "UPDATE users SET last_login_at = ? WHERE id = ?", now().Format(ts), id)
	return err
}

// --- sessions ---

func (d *DB) CreateSession(ctx context.Context, hash string, userID int64, expires time.Time) error {
	at := now().Format(ts)
	_, err := d.db.ExecContext(ctx, "INSERT INTO sessions(token_hash, user_id, created_at, last_seen_at, expires_at) VALUES (?,?,?,?,?)",
		hash, userID, at, at, expires.UTC().Format(ts))
	return err
}

func (d *DB) SessionByHash(ctx context.Context, hash string) (*store.Session, error) {
	var s store.Session
	var created, seen, expires string
	row := d.db.QueryRowContext(ctx, `SELECT s.token_hash, s.created_at, s.last_seen_at, s.expires_at, `+
		userColsU+` FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.token_hash = ?`, hash)
	u, err := scanUser(scanPrefix{row, []any{&s.TokenHash, &created, &seen, &expires}})
	if err != nil {
		return nil, err
	}
	s.User = *u
	for _, p := range []struct {
		dst *time.Time
		src string
	}{{&s.CreatedAt, created}, {&s.LastSeenAt, seen}, {&s.ExpiresAt, expires}} {
		if t := parseTime(sql.NullString{String: p.src, Valid: true}); t != nil {
			*p.dst = *t
		}
	}
	return &s, nil
}

// scanPrefix scans extra leading columns before handing the rest to a scanner like scanUser.
type scanPrefix struct {
	row   interface{ Scan(...any) error }
	first []any
}

func (s scanPrefix) Scan(dest ...any) error { return s.row.Scan(append(s.first, dest...)...) }

func (d *DB) TouchSession(ctx context.Context, hash string) error {
	_, err := d.db.ExecContext(ctx, "UPDATE sessions SET last_seen_at = ? WHERE token_hash = ?", now().Format(ts), hash)
	return err
}

func (d *DB) DeleteSession(ctx context.Context, hash string) error {
	_, err := d.db.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash = ?", hash)
	return err
}

func (d *DB) PruneSessions(ctx context.Context, idleBefore time.Time) error {
	n := now().Format(ts)
	if _, err := d.db.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at <= ? OR last_seen_at < ?", n, idleBefore.UTC().Format(ts)); err != nil {
		return err
	}
	_, err := d.db.ExecContext(ctx, "DELETE FROM auth_tokens WHERE expires_at <= ?", n)
	return err
}

// --- one-time tokens ---

func (d *DB) CreateAuthToken(ctx context.Context, hash string, userID int64, kind store.AuthTokenKind, expires time.Time) error {
	if kind != store.TokenInvite && kind != store.TokenReset {
		return store.ErrInvalid
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// A new invite replaces the old one (Send invite again). Reset links accumulate (each expires in an
	// hour, all are voided when the password is set): otherwise anyone could cancel the link an admin
	// just sent, through the public "Forgot password?" form.
	if kind == store.TokenInvite {
		if _, err := tx.ExecContext(ctx, "DELETE FROM auth_tokens WHERE user_id = ? AND kind = 'invite'", userID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO auth_tokens(token_hash, user_id, kind, created_at, expires_at) VALUES (?,?,?,?,?)",
		hash, userID, string(kind), now().Format(ts), expires.UTC().Format(ts)); err != nil {
		return err
	}
	return tx.Commit()
}

func authToken(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, hash string) (*store.User, store.AuthTokenKind, error) {
	var kind string
	row := q.QueryRowContext(ctx, `SELECT t.kind, `+userColsU+` FROM auth_tokens t JOIN users u ON u.id = t.user_id
		WHERE t.token_hash = ? AND t.expires_at > ? AND u.disabled_at IS NULL`, hash, now().Format(ts))
	u, err := scanUser(scanPrefix{row, []any{&kind}})
	if err != nil {
		return nil, "", err
	}
	return u, store.AuthTokenKind(kind), nil
}

func (d *DB) PeekAuthToken(ctx context.Context, hash string) (*store.User, store.AuthTokenKind, error) {
	return authToken(ctx, d.db, hash)
}

func (d *DB) ConsumeAuthToken(ctx context.Context, hash string) (*store.User, store.AuthTokenKind, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	u, kind, err := authToken(ctx, tx, hash)
	if err != nil {
		return nil, "", err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM auth_tokens WHERE token_hash = ?", hash); err != nil {
		return nil, "", err
	}
	if err := tx.Commit(); err != nil {
		return nil, "", err
	}
	return u, kind, nil
}
