package main

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prippa/mail-sort/internal/classify"
	"github.com/prippa/mail-sort/internal/config"
	"github.com/prippa/mail-sort/internal/engine"
	"github.com/prippa/mail-sort/internal/store"
)

func TestRunRequiresProfile(t *testing.T) {
	isolate(t)
	_, stderr, code := runCmd(t, "run")
	if code != 2 || !strings.Contains(stderr, "--profile") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	_, stderr, code = runCmd(t, "undo")
	if code != 2 || !strings.Contains(stderr, "--profile") {
		t.Fatalf("undo code=%d stderr=%q", code, stderr)
	}
}

func TestApplyWithoutConfirmDoesNotDial(t *testing.T) {
	dir := isolate(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	connected := make(chan struct{}, 1)
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			_ = conn.Close()
			connected <- struct{}{}
		}
	}()
	host, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	body := "language: en\nprofiles:\n  - name: Work\n    provider: custom\n    host: " + host + "\n    port: " + port + "\n    security: implicit_tls\n    username: ada@example.com\n    email: ada@example.com\n    password_env: MAIL_SORTER_PASSWORD_WORK\n"
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAIL_SORTER_PASSWORD_WORK", "secret-do-not-print")
	state, err := config.StateDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(context.Background(), filepath.Join(state, "mailsorter.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateRun(context.Background(), store.Run{Profile: "Work", Mailbox: "INBOX", UIDValidity: 1}, []store.Row{
		{UID: 4, Subject: "secret-subject", Status: store.RowPending, Action: "move", Folder: "Work", Category: "work"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runCmd(t, "--config", path, "run", "--profile", "Work", "--apply")
	if code != 1 || strings.Contains(stderr, "secret-do-not-print") || strings.Contains(stdout, "secret-subject") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "not confirmed") && !strings.Contains(stderr, "не подтвержд") {
		t.Fatalf("stderr=%q", stderr)
	}
	select {
	case <-connected:
		t.Fatal("unconfirmed apply opened a connection")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestConfirmAndOverride(t *testing.T) {
	dir := isolate(t)
	body := []byte("language: en\nprofiles:\n  - name: Work\n    provider: custom\n    host: imap.example.test\n    port: 993\n    username: ada@example.com\n    email: ada@example.com\n    password_env: MAIL_SORTER_PASSWORD_WORK\n")
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runCmd(t, "--config", path, "run", "--profile", "Work", "--confirm")
	if code != 1 || !strings.Contains(stderr, "dry run") {
		t.Fatalf("confirm code=%d stderr=%q", code, stderr)
	}
	state, err := config.StateDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(context.Background(), filepath.Join(state, "mailsorter.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateRun(context.Background(), store.Run{Profile: "Work", Mailbox: "INBOX", UIDValidity: 1}, []store.Row{
		{UID: 4, Subject: "Hello", From: "Ada <ada@example.com>", Status: store.RowPending, Category: "personal", Action: "move", Folder: "Personal"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runCmd(t, "--config", path, "run", "--profile", "Work", "--override", "4=work")
	if code != 0 || !strings.Contains(stdout, "category: work") || !strings.Contains(stdout, "action: move") || !strings.Contains(stdout, "folder: Work") {
		t.Fatalf("override code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatal("override changed the config file")
	}
	stdout, stderr, code = runCmd(t, "--config", path, "run", "--profile", "Work", "--confirm")
	if code != 0 || !strings.Contains(stdout, "Confirmation stored") {
		t.Fatalf("confirm code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	got, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatal("confirm changed the config file")
	}
}

func TestLogSummaryOmitsSubject(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	logRun(logger, "dry", engine.Report{
		Run: store.Run{ID: 1, Profile: "Work", Mailbox: "INBOX"},
		Rows: []store.Row{{
			UID: 4, Subject: "secret-subject", From: "Ada <ada@example.com>", Category: "work",
		}},
	}, time.Millisecond)
	text := buf.String()
	if strings.Contains(text, "secret-subject") || strings.Contains(text, "ada@example.com") {
		t.Fatalf("log = %s", text)
	}
	if !strings.Contains(text, `"mode":"dry"`) || !strings.Contains(text, `"profile":"Work"`) {
		t.Fatalf("log = %s", text)
	}
}

func TestStoreAndCacheShareFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mailsorter.db")
	ctx := context.Background()
	db, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	cache, err := classify.OpenCache(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cache.Close() }()
	decision := classify.Decision{Category: "work", Confidence: 0.9, Provider: "jev", Source: "jev"}
	if err := cache.Put(ctx, "k", decision); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateRun(ctx, store.Run{Profile: "Work", Mailbox: "INBOX", UIDValidity: 1}, nil); err != nil {
		t.Fatal(err)
	}
	got, ok, err := cache.Get(ctx, "k")
	if err != nil || !ok || got.Category != "work" {
		t.Fatalf("cache = %+v ok=%v err=%v", got, ok, err)
	}
}

func TestCSVIsPrivate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.csv")
	rows := []store.Row{{
		UID: 4, MessageID: "<a@example.com>", From: "Ada <ada@example.com>", Subject: "Invoice, March",
		Category: "work", Confidence: 1, Provider: "rule", Action: "move", Folder: "Work", Source: "rule", Status: store.RowPending,
	}}
	if err := writeCSV(path, rows); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(text), "Invoice, March") {
		t.Fatalf("csv = %s", text)
	}
}
