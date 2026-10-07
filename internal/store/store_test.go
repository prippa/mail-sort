package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestConfirmationSupersedeAndMode(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "mailsorter.db")
	db := openDB(t, path)
	if err := db.Confirm(ctx, "Work"); err != ErrNotFound {
		t.Fatalf("confirm err = %v", err)
	}
	id, err := db.CreateRun(ctx, Run{Profile: "Work", Mailbox: "INBOX", UIDValidity: 3}, []Row{
		{UID: 7, Subject: "Hello", From: "Ada <ada@example.com>", Category: "work", Status: RowPending, Action: "move", Folder: "Work"},
	})
	if err != nil || id == 0 {
		t.Fatal(err)
	}
	if err := db.Confirm(ctx, "Work"); err != nil {
		t.Fatal(err)
	}
	ok, err := db.Confirmed(ctx, "Work")
	if err != nil || !ok {
		t.Fatalf("confirmed = %v %v", ok, err)
	}
	second, err := db.CreateRun(ctx, Run{Profile: "Work", Mailbox: "INBOX", UIDValidity: 3}, nil)
	if err != nil {
		t.Fatal(err)
	}
	old, _, err := db.LoadRun(ctx, id)
	if err != nil || old.Status != StatusSuperseded {
		t.Fatalf("old status = %s err=%v", old.Status, err)
	}
	row, err := db.UpdatePending(ctx, "Work", 7, "personal", "move", "Personal")
	if err != ErrRowNotFound {
		t.Fatalf("override old run = %+v err=%v", row, err)
	}
	if _, err := db.CreateRun(ctx, Run{Profile: "Work", Mailbox: "INBOX", UIDValidity: 3}, []Row{
		{UID: 8, Status: RowPending, Action: "move", Folder: "Work", Category: "work"},
	}); err != nil {
		t.Fatal(err)
	}
	open, rows, err := db.LatestOpen(ctx, "Work")
	if err != nil || open.ID == second || len(rows) != 1 {
		t.Fatalf("open = %+v rows=%d err=%v", open, len(rows), err)
	}
	if err := db.CommitRow(ctx, RowUpdate{
		ID: rows[0].ID, Status: RowApplied, Filed: FiledMove, DestUID: 20,
		FiledUID: 8, FiledValidity: 3, Remember: true, Profile: "Work", Mailbox: "INBOX",
		UID: 8, MessageID: "<a@example.com>", RunID: open.ID,
	}); err != nil {
		t.Fatal(err)
	}
	status, err := db.FinishRun(ctx, open.ID)
	if err != nil || status != StatusApplied {
		t.Fatalf("status = %s err=%v", status, err)
	}
	if _, err := db.CreateRun(ctx, Run{Profile: "Work", Mailbox: "INBOX", UIDValidity: 3}, []Row{
		{UID: 9, Status: RowPending, Action: "move", Folder: "Work"},
	}); err != nil {
		t.Fatal(err)
	}
	applied, err := db.Applied(ctx, "Work", "INBOX", 9, 99, "<a@example.com>")
	if err != nil || !applied {
		t.Fatalf("applied = %v err=%v", applied, err)
	}
	if err := db.ClearApplied(ctx, "Work", "INBOX", "<a@example.com>", 3, 8, 3, 8); err != nil {
		t.Fatal(err)
	}
	applied, err = db.Applied(ctx, "Work", "INBOX", 3, 8, "<a@example.com>")
	if err != nil || applied {
		t.Fatalf("applied after clear = %v err=%v", applied, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	fresh, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fresh.Close() }()
	ok, err = fresh.Confirmed(ctx, "Work")
	if err != nil || !ok {
		t.Fatalf("reopen confirmed = %v %v", ok, err)
	}
}

func TestPartialApplyBlocksAnotherPlan(t *testing.T) {
	ctx := context.Background()
	db := openDB(t, filepath.Join(t.TempDir(), "mailsorter.db"))
	id, err := db.CreateRun(ctx, Run{Profile: "Work", Mailbox: "INBOX", UIDValidity: 1}, []Row{
		{UID: 1, Status: RowPending, Action: "move", Folder: "Work", Category: "work"},
		{UID: 2, Status: RowPending, Action: "move", Folder: "Work", Category: "work"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, rows, err := db.LoadRun(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CommitRow(ctx, RowUpdate{
		ID: rows[0].ID, Status: RowApplied, Filed: FiledMove, DestUID: 10,
		FiledUID: 1, FiledValidity: 1, Remember: true, Profile: "Work", Mailbox: "INBOX", UID: 1, RunID: id,
	}); err != nil {
		t.Fatal(err)
	}
	status, err := db.FinishRun(ctx, id)
	if err != nil || status != StatusPartial {
		t.Fatalf("status = %s err=%v", status, err)
	}
	if _, err := db.CreateRun(ctx, Run{Profile: "Work", Mailbox: "INBOX", UIDValidity: 1}, nil); err != ErrApplyInProgress {
		t.Fatalf("err = %v", err)
	}
}

func TestConsentForgetAndRecent(t *testing.T) {
	ctx := context.Background()
	db := openDB(t, filepath.Join(t.TempDir(), "mailsorter.db"))
	ok, err := db.HasConsent(ctx, "abc")
	if err != nil || ok {
		t.Fatalf("consent = %v %v", ok, err)
	}
	if err := db.GrantConsent(ctx, "abc"); err != nil {
		t.Fatal(err)
	}
	ok, err = db.HasConsent(ctx, "abc")
	if err != nil || !ok {
		t.Fatalf("consent = %v %v", ok, err)
	}
	id, err := db.CreateRun(ctx, Run{Profile: "Work", Mailbox: "INBOX", UIDValidity: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Confirm(ctx, "Work"); err != nil {
		t.Fatal(err)
	}
	if err := db.ForgetProfile(ctx, "Work"); err != nil {
		t.Fatal(err)
	}
	ok, err = db.Confirmed(ctx, "Work")
	if err != nil || ok {
		t.Fatalf("still confirmed %v %v", ok, err)
	}
	run, _, err := db.LoadRun(ctx, id)
	if err != nil || run.Status != StatusSuperseded {
		t.Fatalf("status %s err %v", run.Status, err)
	}
	recent, err := db.RecentRuns(ctx, 10)
	if err != nil || len(recent) != 1 || recent[0].ID != id {
		t.Fatalf("%+v %v", recent, err)
	}
}

func TestPredictedCategorySurvivesOverride(t *testing.T) {
	ctx := context.Background()
	db := openDB(t, filepath.Join(t.TempDir(), "mailsorter.db"))
	_, err := db.CreateRun(ctx, Run{Profile: "Work", Mailbox: "INBOX", UIDValidity: 3}, []Row{
		{UID: 7, Subject: "Hello", From: "Ada <ada@example.com>", Category: "work", Status: RowPending, Action: "move", Folder: "Work", Confidence: 0.91},
	})
	if err != nil {
		t.Fatal(err)
	}
	row, err := db.UpdatePending(ctx, "Work", 7, "personal", "move", "Personal")
	if err != nil {
		t.Fatal(err)
	}
	if row.Predicted != "work" || row.Category != "personal" || !row.Override {
		t.Fatalf("%+v", row)
	}
	labeled, err := db.LabeledExamples(ctx, "Work", 10)
	if err != nil || len(labeled) != 1 || labeled[0].Predicted != "work" || labeled[0].Category != "personal" {
		t.Fatalf("%+v %v", labeled, err)
	}
}

func TestExamplesKeepTheNewestRowPerUID(t *testing.T) {
	ctx := context.Background()
	db := openDB(t, filepath.Join(t.TempDir(), "mailsorter.db"))
	if _, err := db.CreateRun(ctx, Run{Profile: "Work", Mailbox: "INBOX", UIDValidity: 3}, []Row{
		{UID: 7, Subject: "First", From: "Ada <ada@example.com>", Category: "work", Status: RowPending, Confidence: 0.4},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpdatePending(ctx, "Work", 7, "personal", "move", "Personal"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateRun(ctx, Run{Profile: "Work", Mailbox: "INBOX", UIDValidity: 3}, []Row{
		{UID: 7, Subject: "Second", From: "Ada <ada@example.com>", Category: "newsletters", Status: RowPending, Confidence: 0.99},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpdatePending(ctx, "Work", 7, "travel", "move", "Travel"); err != nil {
		t.Fatal(err)
	}
	labeled, err := db.LabeledExamples(ctx, "Work", 10)
	if err != nil || len(labeled) != 1 || labeled[0].Predicted != "newsletters" || labeled[0].Category != "travel" {
		t.Fatalf("%+v %v", labeled, err)
	}
	recent, err := db.RecentExamples(ctx, "Work", 10)
	if err != nil || len(recent) != 1 || recent[0].Subject != "Second" {
		t.Fatalf("%+v %v", recent, err)
	}
}

func TestOldDatabaseGainsModelCategory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE run_rows (
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
	)`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	db := openDB(t, path)
	rows, err := db.db.Query(`PRAGMA table_info(run_rows)`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	found := false
	for rows.Next() {
		var cid, notNull, pk int
		var name, kind string
		var dflt any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		if name == "model_category" {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("model_category was not added")
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateRun(context.Background(), Run{Profile: "Work", Mailbox: "INBOX", UIDValidity: 1}, []Row{
		{UID: 1, Category: "work", Status: RowPending},
	}); err != nil {
		t.Fatal(err)
	}
}

func openDB(t *testing.T, path string) *DB {
	t.Helper()
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
