package store

import (
	"context"
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

func openDB(t *testing.T, path string) *DB {
	t.Helper()
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
