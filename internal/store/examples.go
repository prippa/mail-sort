package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Example is one classified message kept for the Evaluate screen.
// Predicted is the model's category. Category is the user's label when
// Override is set, and the model's category otherwise.
type Example struct {
	ID         int64
	UID        uint32
	Subject    string
	From       string
	Predicted  string
	Category   string
	Confidence float64
	Status     string
	Override   bool
}

// RecentExamples returns the newest classified rows for a profile.
func (db *DB) RecentExamples(ctx context.Context, profile string, limit int) ([]Example, error) {
	return db.examples(ctx, profile, limit, false)
}

// LabeledExamples returns rows the user corrected or accepted by applying.
func (db *DB) LabeledExamples(ctx context.Context, profile string, limit int) ([]Example, error) {
	return db.examples(ctx, profile, limit, true)
}

func (db *DB) examples(ctx context.Context, profile string, limit int, labeled bool) ([]Example, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 40
	}
	where := `runs.profile = ? AND run_rows.model_category != ''`
	args := []any{profile}
	if labeled {
		where += ` AND (run_rows.overridden = 1 OR run_rows.status = ?)`
		args = append(args, RowApplied)
	}
	// Newest row per UID. A later dry run must not count the same message twice.
	query := `SELECT id, uid, subject, from_addr, model_category, category, confidence, status, overridden
		FROM (
			SELECT run_rows.id, run_rows.uid, run_rows.subject, run_rows.from_addr,
				run_rows.model_category, run_rows.category, run_rows.confidence, run_rows.status, run_rows.overridden,
				ROW_NUMBER() OVER (PARTITION BY run_rows.uid ORDER BY run_rows.id DESC) AS rn
			FROM run_rows JOIN runs ON runs.id = run_rows.run_id
			WHERE ` + where + `
		) WHERE rn = 1
		ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := db.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: read examples: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Example
	for rows.Next() {
		var item Example
		var uid, overridden int64
		if err := rows.Scan(&item.ID, &uid, &item.Subject, &item.From, &item.Predicted, &item.Category, &item.Confidence, &item.Status, &overridden); err != nil {
			return nil, fmt.Errorf("store: read examples: %w", err)
		}
		parsed, err := u32(uid)
		if err != nil {
			return nil, err
		}
		item.UID = parsed
		item.Override = overridden != 0
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read examples: %w", err)
	}
	return out, nil
}

// SetLabel records the user's category on the newest classified row for this
// UID. It does not change a filed message.
func (db *DB) SetLabel(ctx context.Context, profile string, uid uint32, category string) (Row, error) {
	if err := ctx.Err(); err != nil {
		return Row{}, err
	}
	var id int64
	err := db.db.QueryRowContext(ctx, `SELECT run_rows.id FROM run_rows
		JOIN runs ON runs.id = run_rows.run_id
		WHERE runs.profile = ? AND run_rows.uid = ? AND run_rows.model_category != ''
		ORDER BY run_rows.id DESC LIMIT 1`, profile, uid).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Row{}, ErrNotFound
	}
	if err != nil {
		return Row{}, fmt.Errorf("store: read examples: %w", err)
	}
	if _, err := db.db.ExecContext(ctx, `UPDATE run_rows SET category = ?, overridden = 1 WHERE id = ?`, category, id); err != nil {
		return Row{}, fmt.Errorf("store: write row: %w", err)
	}
	return db.oneRow(ctx, `SELECT `+rowColumns+` FROM run_rows WHERE id = ?`, id)
}
