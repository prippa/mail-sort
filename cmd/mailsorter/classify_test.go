package main

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClassifyStdinRule(t *testing.T) {
	dir := isolate(t)
	configPath := filepath.Join(dir, "config.yaml")
	categories := filepath.Join(dir, "categories.yaml")
	if err := os.WriteFile(configPath, []byte("language: en\ncategories_file: categories.yaml\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := "categories:\n  - key: invoices_receipts\n    name: Invoices\n    description: Bills and receipts. Not newsletters.\n    folder: Invoices\n    action: move\nrules:\n  - from_domain: stripe.com\n    category: invoices_receipts\n"
	if err := os.WriteFile(categories, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	stdin := `{"from":"Stripe <billing@stripe.com>","subject":"Receipt","body":"Thanks for your payment"}`
	stdout, stderr, code := runCmdIn(t, strings.NewReader(stdin), "--config", configPath, "classify", "--stdin")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "category: invoices_receipts") || !strings.Contains(stdout, "source: rule") || !strings.Contains(stdout, "name: Invoices") || !strings.Contains(stdout, "folder: Invoices") {
		t.Fatalf("stdout=%s", stdout)
	}
	if strings.Contains(stderr, "starter") || strings.Contains(stdout, "Thanks for your payment") {
		t.Fatalf("stdout=%s stderr=%s", stdout, stderr)
	}
}

func TestClassifyUsage(t *testing.T) {
	isolate(t)
	_, stderr, code := runCmd(t, "classify")
	if code != 2 || !strings.Contains(stderr, "--stdin") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	_, stderr, code = runCmd(t, "classify", "--stdin")
	if code != 2 || !strings.Contains(stderr, "stdin is empty") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	stdout, _, code := runCmd(t, "categories", "export")
	if code != 0 || !strings.Contains(stdout, "invoices_receipts") || !strings.Contains(stdout, "security_alerts") {
		t.Fatalf("code=%d stdout=%s", code, stdout)
	}
}

func TestClassifyWithoutAKeyDoesNotDial(t *testing.T) {
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
	t.Setenv("TYPESAFE_API_KEY", "")
	configPath := filepath.Join(dir, "config.yaml")
	body := "language: en\nclassifiers:\n  - provider: jev\n    base_url: http://" + ln.Addr().String() + "\n"
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	stdin := `{"from":"a@example.com","subject":"Hello","body":"Just a note"}`
	_, stderr, code := runCmdIn(t, strings.NewReader(stdin), "--config", configPath, "classify", "--stdin")
	if code != 1 || !strings.Contains(stderr, "TYPESAFE_API_KEY") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	select {
	case <-connected:
		t.Fatal("missing key opened a connection")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestClassifyMockJev(t *testing.T) {
	dir := isolate(t)
	const key = "test-key-value"
	const marker = "unique-body-marker"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+key {
			http.Error(w, "missing key", http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{
			"model":"jev-1.13.0",
			"answers":{"folder":{"type":"choice","choice":"work","confidence":0.91,"probabilities":{"work":0.91,"needs_review":0.09}}},
			"usage":{"input_tokens":10,"output_tokens":2}
		}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("TYPESAFE_API_KEY", key)
	configPath := filepath.Join(dir, "config.yaml")
	body := "language: en\nclassifiers:\n  - provider: jev\n    base_url: " + srv.URL + "\n    price_input: 2\n"
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	stdin := `{"from":"boss@example.com","subject":"unique-subject-marker","body":"` + marker + `"}`
	stdout, stderr, code := runCmdIn(t, strings.NewReader(stdin), "--config", configPath, "classify", "--stdin")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	if strings.Contains(stderr, key) || strings.Contains(stdout, key) || strings.Contains(stdout, marker) {
		t.Fatalf("stdout=%s stderr=%s", stdout, stderr)
	}
	for _, want := range []string{"category: work", "source: jev", "model: jev-1.13.0", "name: Work", "tokens_in: 10", "cost_usd: 0.000020"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("missing %q in %s", want, stdout)
		}
	}
	logPath := filepath.Join(dir, "state", "mailsorter", "mailsorter.log")
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(logData), key) || strings.Contains(string(logData), marker) || strings.Contains(string(logData), "unique-subject-marker") {
		t.Fatalf("log=%s", logData)
	}
}
