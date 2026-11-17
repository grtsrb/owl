package sqlite

import (
	"context"
	"database/sql"
	"time"

	"owl/internal/store"
)

func (d *DB) ListSummaries(ctx context.Context) ([]store.Summary, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT component, from_version, to_version, model, prompt_version, created_at, content
		FROM summaries ORDER BY component, from_version, to_version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Summary
	for rows.Next() {
		var s store.Summary
		var at, content string
		if err := rows.Scan(&s.Component, &s.FromVersion, &s.ToVersion, &s.Model, &s.PromptVersion, &at, &content); err != nil {
			return nil, err
		}
		if t := parseTime(sql.NullString{String: at, Valid: true}); t != nil {
			s.CreatedAt = *t
		}
		s.Content = []byte(content)
		out = append(out, s)
	}
	return out, rows.Err()
}

// PutSummary stores or replaces (Regenerate) the summary of one jump.
func (d *DB) PutSummary(ctx context.Context, s store.Summary) error {
	at := s.CreatedAt
	if at.IsZero() {
		at = now()
	}
	_, err := d.db.ExecContext(ctx, `INSERT INTO summaries(component, from_version, to_version, model, prompt_version, created_at, content)
		VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(component, from_version, to_version) DO UPDATE SET model=excluded.model,
			prompt_version=excluded.prompt_version, created_at=excluded.created_at, content=excluded.content`,
		s.Component, s.FromVersion, s.ToVersion, s.Model, s.PromptVersion, at.UTC().Format(ts), string(s.Content))
	return err
}

func (d *DB) AddSummaryRequest(ctx context.Context, r store.SummaryRequest) error {
	at := r.At
	if at.IsZero() {
		at = now()
	}
	_, err := d.db.ExecContext(ctx, `INSERT INTO summary_requests(at, component, from_version, to_version, model,
		input_tokens, output_tokens, cost_micros, result, error) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		at.UTC().Format(ts), r.Component, r.FromVersion, r.ToVersion, r.Model, r.InputTokens, r.OutputTokens,
		r.CostMicros, r.Result, r.Error)
	return err
}

func (d *DB) ListSummaryRequests(ctx context.Context, limit int) ([]store.SummaryRequest, error) {
	if limit <= 0 || limit > 1000 {
		limit = 20
	}
	rows, err := d.db.QueryContext(ctx, `SELECT id, at, component, from_version, to_version, model, input_tokens,
		output_tokens, cost_micros, result, error FROM summary_requests ORDER BY at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.SummaryRequest{}
	for rows.Next() {
		var r store.SummaryRequest
		var at string
		if err := rows.Scan(&r.ID, &at, &r.Component, &r.FromVersion, &r.ToVersion, &r.Model, &r.InputTokens,
			&r.OutputTokens, &r.CostMicros, &r.Result, &r.Error); err != nil {
			return nil, err
		}
		if t := parseTime(sql.NullString{String: at, Valid: true}); t != nil {
			r.At = *t
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (d *DB) SummarySpend(ctx context.Context, since time.Time) (int64, error) {
	var n int64
	err := d.db.QueryRowContext(ctx, "SELECT coalesce(sum(cost_micros), 0) FROM summary_requests WHERE at >= ?",
		since.UTC().Format(ts)).Scan(&n)
	return n, err
}

func (d *DB) DeleteSummaries(ctx context.Context, component string) error {
	_, err := d.db.ExecContext(ctx, "DELETE FROM summaries WHERE component = ?", component)
	return err
}
