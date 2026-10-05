package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	// StatusDry is a run that has not filed mail.
	StatusDry = "dry"
	// StatusPartial is a run that filed some rows and still has pending rows.
	StatusPartial = "partial"
	// StatusApplied is a run whose pending rows are finished.
	StatusApplied = "applied"
	// StatusUndone is a run whose filed rows were reversed or acknowledged.
	StatusUndone = "undone"
	// StatusSuperseded is a dry run replaced by a newer plan.
	StatusSuperseded = "superseded"

	// RowPending can still be filed or overridden.
	RowPending = "pending"
	// RowApplied was filed or recorded as a no-op.
	RowApplied = "applied"
	// RowError was not filed.
	RowError = "error"
	// RowUndone was reversed, or a copy was left in place on purpose.
	RowUndone = "undone"

	// FiledMove removed the source message.
	FiledMove = "move"
	// FiledCopy left the source message in place.
	FiledCopy = "copy"
	// FiledNone took no mailbox action.
	FiledNone = "none"
)

var (
	// ErrNotFound means the requested run does not exist.
	ErrNotFound = errors.New("store: not found")
	// ErrRowNotFound means that UID is not in the open run.
	ErrRowNotFound = errors.New("store: row not found")
	// ErrNotPending means the row can no longer be overridden.
	ErrNotPending = errors.New("store: that message is already filed")
	// ErrApplyInProgress means a partial apply must be finished or undone first.
	ErrApplyInProgress = errors.New("store: finish or undo the current apply before a new dry run")
)

// DB is the state file. The file mode is 0600.
type DB struct {
	db *sql.DB
}

// Run is one dry run, apply, or undo.
type Run struct {
	ID          int64
	Profile     string
	Mailbox     string
	UIDValidity uint32
	Status      string
	CreatedAt   time.Time
	AppliedAt   time.Time
	UndoneAt    time.Time
	Moves       int
	Copies      int
	Errors      int
	APICalls    int
	TokensIn    int
	TokensOut   int
	CostUSD     float64
	HasCost     bool
}

// Row is one message in a run. Subject and From are stored so a dry run can
// be shown again. Logs do not read them.
type Row struct {
	ID            int64
	RunID         int64
	UID           uint32
	MessageID     string
	Subject       string
	From          string
	Category      string
	Provider      string
	Model         string
	Confidence    float64
	Action        string
	Folder        string
	Source        string
	Override      bool
	Status        string
	DestUID       uint32
	Detail        string
	Filed         string
	FiledUID      uint32
	FiledValidity uint32
	TokensIn      int
	TokensOut     int
	CostUSD       float64
	HasCost       bool
}

// RowUpdate records the result of filing or skipping one row.
type RowUpdate struct {
	ID            int64
	Status        string
	DestUID       uint32
	Detail        string
	Filed         string
	FiledUID      uint32
	FiledValidity uint32
	Remember      bool
	Profile       string
	Mailbox       string
	UID           uint32
	MessageID     string
	RunID         int64
}

// Open creates or opens the state database.
func Open(ctx context.Context, path string) (*DB, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if path == "" {
		return nil, errors.New("store: path is empty")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: open: %w", err)
	}
	if err := prepare(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := chmodDB(path); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &DB{db: db}, nil
}

func prepare(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `PRAGMA busy_timeout = 5000`); err != nil {
		return fmt.Errorf("store: open: %w", err)
	}
	var mode string
	if err := db.QueryRowContext(ctx, `PRAGMA journal_mode = WAL`).Scan(&mode); err != nil {
		return fmt.Errorf("store: open: %w", err)
	}
	if mode != "wal" {
		return errors.New("store: open: journal mode")
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS profile_confirmation (
	profile TEXT PRIMARY KEY,
	confirmed_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS runs (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	profile TEXT NOT NULL,
	mailbox TEXT NOT NULL,
	uid_validity INTEGER NOT NULL,
	status TEXT NOT NULL,
	created_at TEXT NOT NULL,
	applied_at TEXT NOT NULL DEFAULT '',
	undone_at TEXT NOT NULL DEFAULT '',
	moves INTEGER NOT NULL DEFAULT 0,
	copies INTEGER NOT NULL DEFAULT 0,
	errors INTEGER NOT NULL DEFAULT 0,
	api_calls INTEGER NOT NULL DEFAULT 0,
	tokens_in INTEGER NOT NULL DEFAULT 0,
	tokens_out INTEGER NOT NULL DEFAULT 0,
	cost_usd REAL NOT NULL DEFAULT 0,
	has_cost INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS run_rows (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	run_id INTEGER NOT NULL,
	uid INTEGER NOT NULL,
	message_id TEXT NOT NULL DEFAULT '',
	subject TEXT NOT NULL DEFAULT '',
	from_addr TEXT NOT NULL DEFAULT '',
	category TEXT NOT NULL DEFAULT '',
	provider TEXT NOT NULL DEFAULT '',
	model TEXT NOT NULL DEFAULT '',
	confidence REAL NOT NULL DEFAULT 0,
	action TEXT NOT NULL DEFAULT '',
	folder TEXT NOT NULL DEFAULT '',
	source TEXT NOT NULL DEFAULT '',
	overridden INTEGER NOT NULL DEFAULT 0,
	status TEXT NOT NULL,
	dest_uid INTEGER NOT NULL DEFAULT 0,
	detail TEXT NOT NULL DEFAULT '',
	filed TEXT NOT NULL DEFAULT '',
	filed_uid INTEGER NOT NULL DEFAULT 0,
	filed_validity INTEGER NOT NULL DEFAULT 0,
	tokens_in INTEGER NOT NULL DEFAULT 0,
	tokens_out INTEGER NOT NULL DEFAULT 0,
	cost_usd REAL NOT NULL DEFAULT 0,
	has_cost INTEGER NOT NULL DEFAULT 0,
	UNIQUE (run_id, uid)
);
CREATE TABLE IF NOT EXISTS applied_messages (
	profile TEXT NOT NULL,
	mailbox TEXT NOT NULL,
	uid_validity INTEGER NOT NULL,
	uid INTEGER NOT NULL,
	message_id TEXT NOT NULL DEFAULT '',
	run_id INTEGER NOT NULL,
	PRIMARY KEY (profile, mailbox, uid_validity, uid)
);
CREATE INDEX IF NOT EXISTS runs_profile ON runs (profile, status, id);
CREATE INDEX IF NOT EXISTS run_rows_run ON run_rows (run_id, id);
CREATE INDEX IF NOT EXISTS applied_msgid ON applied_messages (profile, mailbox, message_id);
CREATE TABLE IF NOT EXISTS classification_consent (
	id TEXT PRIMARY KEY,
	consented_at TEXT NOT NULL
)`,
	}
	for _, statement := range statements {
		for _, part := range strings.Split(statement, ";") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if _, err := db.ExecContext(ctx, part); err != nil {
				return fmt.Errorf("store: open: %w", err)
			}
		}
	}
	return nil
}

// Close closes the database.
func (db *DB) Close() error {
	if db == nil || db.db == nil {
		return nil
	}
	if err := db.db.Close(); err != nil {
		return fmt.Errorf("store: close: %w", err)
	}
	return nil
}

// Confirmed reports whether this profile may file mail.
func (db *DB) Confirmed(ctx context.Context, profile string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	var n int
	err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM profile_confirmation WHERE profile = ?`, profile).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("store: read confirmation: %w", err)
	}
	return n > 0, nil
}

// Confirm records that the user accepted a dry run for this profile.
func (db *DB) Confirm(ctx context.Context, profile string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if profile == "" {
		return errors.New("store: profile is empty")
	}
	var n int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE profile = ?`, profile).Scan(&n); err != nil {
		return fmt.Errorf("store: read run: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	_, err := db.db.ExecContext(ctx, `INSERT INTO profile_confirmation (profile, confirmed_at) VALUES (?, ?)
		ON CONFLICT(profile) DO UPDATE SET confirmed_at = excluded.confirmed_at`, profile, now())
	if err != nil {
		return fmt.Errorf("store: write confirmation: %w", err)
	}
	return nil
}

// HasConsent reports whether this remote-classifier fingerprint was accepted.
func (db *DB) HasConsent(ctx context.Context, id string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if id == "" {
		return false, nil
	}
	var n int
	err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM classification_consent WHERE id = ?`, id).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("store: read consent: %w", err)
	}
	return n > 0, nil
}

// GrantConsent records that the user accepted this remote-classifier fingerprint.
func (db *DB) GrantConsent(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if id == "" {
		return errors.New("store: consent id is empty")
	}
	_, err := db.db.ExecContext(ctx, `INSERT INTO classification_consent (id, consented_at) VALUES (?, ?)
		ON CONFLICT(id) DO UPDATE SET consented_at = excluded.consented_at`, id, now())
	if err != nil {
		return fmt.Errorf("store: write consent: %w", err)
	}
	return nil
}

// ForgetProfile drops confirmation and an unapplied dry run.
// A partial apply must be finished or undone first.
func (db *DB) ForgetProfile(ctx context.Context, profile string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if profile == "" {
		return errors.New("store: profile is empty")
	}
	tx, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: forget profile: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var partial int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE profile = ? AND status = ?`, profile, StatusPartial).Scan(&partial); err != nil {
		return fmt.Errorf("store: forget profile: %w", err)
	}
	if partial > 0 {
		return ErrApplyInProgress
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM profile_confirmation WHERE profile = ?`, profile); err != nil {
		return fmt.Errorf("store: forget profile: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET status = ? WHERE profile = ? AND status = ?`, StatusSuperseded, profile, StatusDry); err != nil {
		return fmt.Errorf("store: forget profile: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: forget profile: %w", err)
	}
	return nil
}

// RecentRuns returns the newest runs, without message rows.
func (db *DB) RecentRuns(ctx context.Context, limit int) ([]Run, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 50 {
		limit = 50
	}
	rows, err := db.db.QueryContext(ctx, `SELECT `+runColumns+` FROM runs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: read run: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read run: %w", err)
	}
	if out == nil {
		out = []Run{}
	}
	return out, nil
}

// Applied reports whether this message was already filed for the profile.
// A non-empty Message-ID matches even when the UID validity changed.
func (db *DB) Applied(ctx context.Context, profile, mailbox string, validity, uid uint32, messageID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	var n int
	err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM applied_messages
		WHERE profile = ? AND mailbox = ? AND (
			(uid_validity = ? AND uid = ?) OR (message_id <> '' AND message_id = ?)
		)`, profile, mailbox, validity, uid, messageID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("store: read applied: %w", err)
	}
	return n > 0, nil
}

// CreateRun stores a dry run and replaces an older unapplied dry run.
// A partial apply blocks the new plan.
func (db *DB) CreateRun(ctx context.Context, run Run, rows []Row) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if run.Profile == "" || run.Mailbox == "" {
		return 0, errors.New("store: profile or mailbox is empty")
	}
	tx, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: write run: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var status string
	err = tx.QueryRowContext(ctx, `SELECT status FROM runs
		WHERE profile = ? AND status IN (?, ?)
		ORDER BY id DESC LIMIT 1`, run.Profile, StatusDry, StatusPartial).Scan(&status)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("store: read run: %w", err)
	}
	if status == StatusPartial {
		return 0, ErrApplyInProgress
	}
	if status == StatusDry {
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET status = ? WHERE profile = ? AND status = ?`,
			StatusSuperseded, run.Profile, StatusDry); err != nil {
			return 0, fmt.Errorf("store: write run: %w", err)
		}
	}
	if run.CreatedAt.IsZero() {
		run.CreatedAt = time.Now().UTC()
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO runs (
		profile, mailbox, uid_validity, status, created_at, api_calls, tokens_in, tokens_out, cost_usd, has_cost
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.Profile, run.Mailbox, run.UIDValidity, StatusDry, run.CreatedAt.UTC().Format(time.RFC3339Nano),
		run.APICalls, run.TokensIn, run.TokensOut, run.CostUSD, boolInt(run.HasCost))
	if err != nil {
		return 0, fmt.Errorf("store: write run: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: write run: %w", err)
	}
	for _, row := range rows {
		if _, err := tx.ExecContext(ctx, `INSERT INTO run_rows (
			run_id, uid, message_id, subject, from_addr, category, provider, model, confidence,
			action, folder, source, overridden, status, detail, tokens_in, tokens_out, cost_usd, has_cost
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, row.UID, clip(row.MessageID, 500), clip(row.Subject, 300), clip(row.From, 300),
			row.Category, row.Provider, row.Model, row.Confidence, row.Action, row.Folder, row.Source, 0,
			row.Status, clip(row.Detail, 500), row.TokensIn, row.TokensOut, row.CostUSD, boolInt(row.HasCost)); err != nil {
			return 0, fmt.Errorf("store: write row: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: write run: %w", err)
	}
	return id, nil
}

// LatestOpen returns the newest dry or partial run for the profile.
func (db *DB) LatestOpen(ctx context.Context, profile string) (Run, []Row, error) {
	run, err := db.oneRun(ctx, `SELECT `+runColumns+` FROM runs
		WHERE profile = ? AND status IN (?, ?) ORDER BY id DESC LIMIT 1`, profile, StatusDry, StatusPartial)
	if err != nil {
		return Run{}, nil, err
	}
	rows, err := db.rowsOf(ctx, run.ID)
	if err != nil {
		return Run{}, nil, err
	}
	return run, rows, nil
}

// LatestFiled returns the newest applied or partial run.
func (db *DB) LatestFiled(ctx context.Context, profile string) (Run, []Row, error) {
	run, err := db.oneRun(ctx, `SELECT `+runColumns+` FROM runs
		WHERE profile = ? AND status IN (?, ?) ORDER BY id DESC LIMIT 1`, profile, StatusApplied, StatusPartial)
	if err != nil {
		return Run{}, nil, err
	}
	rows, err := db.rowsOf(ctx, run.ID)
	if err != nil {
		return Run{}, nil, err
	}
	return run, rows, nil
}

// LoadRun reads one run and its rows.
func (db *DB) LoadRun(ctx context.Context, id int64) (Run, []Row, error) {
	run, err := db.oneRun(ctx, `SELECT `+runColumns+` FROM runs WHERE id = ?`, id)
	if err != nil {
		return Run{}, nil, err
	}
	rows, err := db.rowsOf(ctx, id)
	if err != nil {
		return Run{}, nil, err
	}
	return run, rows, nil
}

// UpdatePending changes the category on a pending or error row and marks it pending.
func (db *DB) UpdatePending(ctx context.Context, profile string, uid uint32, category, action, folder string) (Row, error) {
	if err := ctx.Err(); err != nil {
		return Row{}, err
	}
	run, err := db.oneRun(ctx, `SELECT `+runColumns+` FROM runs
		WHERE profile = ? AND status IN (?, ?) ORDER BY id DESC LIMIT 1`, profile, StatusDry, StatusPartial)
	if err != nil {
		return Row{}, err
	}
	row, err := db.oneRow(ctx, `SELECT `+rowColumns+` FROM run_rows WHERE run_id = ? AND uid = ?`, run.ID, uid)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Row{}, ErrRowNotFound
		}
		return Row{}, err
	}
	if row.Status != RowPending && row.Status != RowError {
		return Row{}, ErrNotPending
	}
	_, err = db.db.ExecContext(ctx, `UPDATE run_rows
		SET category = ?, action = ?, folder = ?, overridden = 1, status = ?, detail = ''
		WHERE id = ?`, category, action, folder, RowPending, row.ID)
	if err != nil {
		return Row{}, fmt.Errorf("store: write row: %w", err)
	}
	return db.oneRow(ctx, `SELECT `+rowColumns+` FROM run_rows WHERE id = ?`, row.ID)
}

// CommitRow writes one filing result. Remember records the message so a later
// plan does not file it again.
func (db *DB) CommitRow(ctx context.Context, update RowUpdate) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	tx, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: write row: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `UPDATE run_rows
		SET status = ?, dest_uid = ?, detail = ?, filed = ?, filed_uid = ?, filed_validity = ?
		WHERE id = ?`,
		update.Status, update.DestUID, clip(update.Detail, 500), update.Filed,
		update.FiledUID, update.FiledValidity, update.ID)
	if err != nil {
		return fmt.Errorf("store: write row: %w", err)
	}
	if update.Remember {
		uid := update.FiledUID
		validity := update.FiledValidity
		if uid == 0 {
			uid = update.UID
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO applied_messages (
			profile, mailbox, uid_validity, uid, message_id, run_id
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(profile, mailbox, uid_validity, uid) DO UPDATE SET
			message_id = excluded.message_id, run_id = excluded.run_id`,
			update.Profile, update.Mailbox, validity, uid, clip(update.MessageID, 500), update.RunID)
		if err != nil {
			return fmt.Errorf("store: write applied: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: write row: %w", err)
	}
	return nil
}

// FinishRun recomputes counters and sets dry, partial, or applied.
func (db *DB) FinishRun(ctx context.Context, runID int64) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var moves, copies, failures, pending int
	err := db.db.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(CASE WHEN status = ? AND filed = ? THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status = ? AND filed = ? THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0)
		FROM run_rows WHERE run_id = ?`,
		RowApplied, FiledMove, RowApplied, FiledCopy, RowError, RowPending, runID).Scan(&moves, &copies, &failures, &pending)
	if err != nil {
		return "", fmt.Errorf("store: read row: %w", err)
	}
	status := StatusApplied
	appliedAt := now()
	switch {
	case pending > 0 && moves+copies == 0 && failures == 0:
		status = StatusDry
		appliedAt = ""
	case pending > 0:
		status = StatusPartial
	}
	_, err = db.db.ExecContext(ctx, `UPDATE runs SET status = ?, applied_at = ?, moves = ?, copies = ?, errors = ? WHERE id = ?`,
		status, appliedAt, moves, copies, failures, runID)
	if err != nil {
		return "", fmt.Errorf("store: write run: %w", err)
	}
	return status, nil
}

// ClearApplied forgets a filed message so a later plan can see it again.
func (db *DB) ClearApplied(ctx context.Context, profile, mailbox, messageID string, validity, uid, filedValidity, filedUID uint32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := db.db.ExecContext(ctx, `DELETE FROM applied_messages
		WHERE profile = ? AND mailbox = ? AND (
			(uid_validity = ? AND uid = ?)
			OR (uid_validity = ? AND uid = ?)
			OR (message_id <> '' AND message_id = ?)
		)`, profile, mailbox, validity, uid, filedValidity, filedUID, messageID)
	if err != nil {
		return fmt.Errorf("store: write applied: %w", err)
	}
	return nil
}

// SetRowDetail records an undo failure and leaves the row applied.
func (db *DB) SetRowDetail(ctx context.Context, rowID int64, detail string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := db.db.ExecContext(ctx, `UPDATE run_rows SET detail = ? WHERE id = ?`, clip(detail, 500), rowID)
	if err != nil {
		return fmt.Errorf("store: write row: %w", err)
	}
	return nil
}

// MarkUndone sets one row as undone. A non-zero backUID replaces the stored destination UID.
func (db *DB) MarkUndone(ctx context.Context, rowID int64, detail string, backUID uint32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var err error
	if backUID == 0 {
		_, err = db.db.ExecContext(ctx, `UPDATE run_rows SET status = ?, detail = ? WHERE id = ?`,
			RowUndone, clip(detail, 500), rowID)
	} else {
		_, err = db.db.ExecContext(ctx, `UPDATE run_rows SET status = ?, detail = ?, dest_uid = ? WHERE id = ?`,
			RowUndone, clip(detail, 500), backUID, rowID)
	}
	if err != nil {
		return fmt.Errorf("store: write row: %w", err)
	}
	return nil
}

// FinishUndo sets the run status from the rows that are still applied.
func (db *DB) FinishUndo(ctx context.Context, runID int64) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var applied, pending int
	err := db.db.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0)
		FROM run_rows WHERE run_id = ?`, RowApplied, RowPending, runID).Scan(&applied, &pending)
	if err != nil {
		return "", fmt.Errorf("store: read row: %w", err)
	}
	status := StatusApplied
	undoneAt := ""
	switch {
	case applied == 0 && pending == 0:
		status = StatusUndone
		undoneAt = now()
	case pending > 0 && applied == 0:
		status = StatusPartial
	case pending > 0:
		status = StatusPartial
	}
	_, err = db.db.ExecContext(ctx, `UPDATE runs SET status = ?, undone_at = CASE WHEN ? = '' THEN undone_at ELSE ? END WHERE id = ?`,
		status, undoneAt, undoneAt, runID)
	if err != nil {
		return "", fmt.Errorf("store: write run: %w", err)
	}
	return status, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanRun(row rowScanner) (Run, error) {
	var run Run
	var validity, hasCost int64
	var created, applied, undone string
	err := row.Scan(
		&run.ID, &run.Profile, &run.Mailbox, &validity, &run.Status, &created, &applied, &undone,
		&run.Moves, &run.Copies, &run.Errors, &run.APICalls, &run.TokensIn, &run.TokensOut, &run.CostUSD, &hasCost,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, ErrNotFound
	}
	if err != nil {
		return Run{}, fmt.Errorf("store: read run: %w", err)
	}
	value, err := u32(validity)
	if err != nil {
		return Run{}, err
	}
	run.UIDValidity = value
	run.HasCost = hasCost != 0
	run.CreatedAt, err = parseTime(created)
	if err != nil {
		return Run{}, err
	}
	run.AppliedAt, err = parseTime(applied)
	if err != nil {
		return Run{}, err
	}
	run.UndoneAt, err = parseTime(undone)
	if err != nil {
		return Run{}, err
	}
	return run, nil
}

func (db *DB) oneRun(ctx context.Context, query string, args ...any) (Run, error) {
	if err := ctx.Err(); err != nil {
		return Run{}, err
	}
	var run Run
	var validity, hasCost int64
	var created, applied, undone string
	err := db.db.QueryRowContext(ctx, query, args...).Scan(
		&run.ID, &run.Profile, &run.Mailbox, &validity, &run.Status, &created, &applied, &undone,
		&run.Moves, &run.Copies, &run.Errors, &run.APICalls, &run.TokensIn, &run.TokensOut, &run.CostUSD, &hasCost,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, ErrNotFound
	}
	if err != nil {
		return Run{}, fmt.Errorf("store: read run: %w", err)
	}
	value, err := u32(validity)
	if err != nil {
		return Run{}, err
	}
	run.UIDValidity = value
	run.HasCost = hasCost != 0
	run.CreatedAt, err = parseTime(created)
	if err != nil {
		return Run{}, err
	}
	run.AppliedAt, err = parseTime(applied)
	if err != nil {
		return Run{}, err
	}
	run.UndoneAt, err = parseTime(undone)
	if err != nil {
		return Run{}, err
	}
	return run, nil
}

func (db *DB) rowsOf(ctx context.Context, runID int64) ([]Row, error) {
	rows, err := db.db.QueryContext(ctx, `SELECT `+rowColumns+` FROM run_rows WHERE run_id = ? ORDER BY id`, runID)
	if err != nil {
		return nil, fmt.Errorf("store: read row: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Row
	for rows.Next() {
		row, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read row: %w", err)
	}
	return out, nil
}

func (db *DB) oneRow(ctx context.Context, query string, args ...any) (Row, error) {
	rows, err := db.db.QueryContext(ctx, query, args...)
	if err != nil {
		return Row{}, fmt.Errorf("store: read row: %w", err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return Row{}, fmt.Errorf("store: read row: %w", err)
		}
		return Row{}, ErrNotFound
	}
	return scanRow(rows)
}

type scanner interface {
	Scan(dest ...any) error
}

func scanRow(row scanner) (Row, error) {
	var out Row
	var uid, dest, filedUID, filedValidity, overridden, hasCost int64
	err := row.Scan(
		&out.ID, &out.RunID, &uid, &out.MessageID, &out.Subject, &out.From, &out.Category,
		&out.Provider, &out.Model, &out.Confidence, &out.Action, &out.Folder, &out.Source,
		&overridden, &out.Status, &dest, &out.Detail, &out.Filed, &filedUID, &filedValidity,
		&out.TokensIn, &out.TokensOut, &out.CostUSD, &hasCost,
	)
	if err != nil {
		return Row{}, fmt.Errorf("store: read row: %w", err)
	}
	out.UID, err = u32(uid)
	if err != nil {
		return Row{}, err
	}
	out.DestUID, err = u32(dest)
	if err != nil {
		return Row{}, err
	}
	out.FiledUID, err = u32(filedUID)
	if err != nil {
		return Row{}, err
	}
	out.FiledValidity, err = u32(filedValidity)
	if err != nil {
		return Row{}, err
	}
	out.Override = overridden != 0
	out.HasCost = hasCost != 0
	return out, nil
}

const runColumns = `id, profile, mailbox, uid_validity, status, created_at, applied_at, undone_at, moves, copies, errors, api_calls, tokens_in, tokens_out, cost_usd, has_cost`

const rowColumns = `id, run_id, uid, message_id, subject, from_addr, category, provider, model, confidence, action, folder, source, overridden, status, dest_uid, detail, filed, filed_uid, filed_validity, tokens_in, tokens_out, cost_usd, has_cost`

func chmodDB(path string) error {
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("store: open: %w", err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		extra := path + suffix
		if _, err := os.Stat(extra); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("store: open: %w", err)
		}
		if err := os.Chmod(extra, 0o600); err != nil {
			return fmt.Errorf("store: open: %w", err)
		}
	}
	return nil
}

func now() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func parseTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, errors.New("store: timestamp")
	}
	return parsed, nil
}

func u32(value int64) (uint32, error) {
	if value < 0 || value > math.MaxUint32 {
		return 0, errors.New("store: uid is out of range")
	}
	return uint32(value), nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func clip(value string, n int) string {
	runes := []rune(value)
	if len(runes) <= n {
		return value
	}
	return string(runes[:n])
}
