package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prippa/mail-sort/internal/buildinfo"
)

func TestVersion(t *testing.T) {
	isolate(t)
	stdout, stderr, code := runCmd(t, "version")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	if stdout != buildinfo.Version+"\n" {
		t.Fatalf("stdout=%q", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr=%q", stderr)
	}
}

func TestHelp(t *testing.T) {
	isolate(t)
	stdout, _, code := runCmd(t, "help")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(stdout, "mailsorter") {
		t.Fatalf("stdout=%q", stdout)
	}
}

func TestNotImplemented(t *testing.T) {
	isolate(t)
	_, stderr, code := runCmd(t, "watch")
	if code != 2 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "watch is not implemented yet (phase 6)") {
		t.Fatalf("stderr=%q", stderr)
	}
}

func TestDefaultCommandIsUI(t *testing.T) {
	isolate(t)
	ctx, cancel := context.WithCancel(context.Background())
	origCtx := newUIContext
	origBrowser := launchBrowser
	newUIContext = func() (context.Context, context.CancelFunc) { return ctx, func() {} }
	launchBrowser = false
	t.Cleanup(func() {
		newUIContext = origCtx
		launchBrowser = origBrowser
		cancel()
	})
	var buf safeBuf
	done := make(chan int, 1)
	go func() {
		done <- run(nil, strings.NewReader(""), &buf, io.Discard)
	}()
	deadline := time.Now().Add(5 * time.Second)
	text := ""
	for time.Now().Before(deadline) {
		text = buf.String()
		if strings.Contains(text, "http://127.0.0.1:") && strings.Contains(text, "/#") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 || !strings.Contains(text, "http://127.0.0.1:") {
			t.Fatalf("code=%d stdout=%q", code, text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ui did not stop")
	}
	if strings.Contains(text, "not implemented") {
		t.Fatalf("stdout=%q", text)
	}
}

type safeBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestUnknownCommand(t *testing.T) {
	isolate(t)
	_, stderr, code := runCmd(t, "explode")
	if code != 2 || !strings.Contains(stderr, "unknown command") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestConfigSecretIsNotEchoed(t *testing.T) {
	dir := isolate(t)
	const secret = "config-secret-value"
	path := filepath.Join(dir, "config.yaml")
	body := []byte("password: " + secret + "\n")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runCmd(t, "--config", path, "run")
	if code != 1 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	if strings.Contains(stderr, secret) {
		t.Fatal("stderr contains the secret value")
	}
	if !strings.Contains(stderr, `field "password"`) {
		t.Fatalf("stderr=%q", stderr)
	}
	logPath := filepath.Join(dir, "state", "mailsorter", "mailsorter.log")
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatal("log file was created for a rejected config")
	}
}

func TestTestConnRequiresProfile(t *testing.T) {
	isolate(t)
	_, stderr, code := runCmd(t, "test-conn")
	if code != 2 || !strings.Contains(stderr, "--profile") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestOAuthDoesNotDial(t *testing.T) {
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
	const secret = "oauth-secret-do-not-print"
	body := "profiles:\n  - name: Work\n    provider: gmail\n    host: " + host + "\n    port: " + port + "\n    auth: oauth_google\n    username: ada@example.com\n    email: ada@example.com\n    password_env: MAIL_SORTER_PASSWORD_WORK\n"
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAIL_SORTER_PASSWORD_WORK", secret)
	_, stderr, code := runCmd(t, "--config", path, "test-conn", "--profile", "Work")
	if code != 2 || !strings.Contains(stderr, "phase 5") || strings.Contains(stderr, secret) {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	select {
	case <-connected:
		t.Fatal("oauth opened a connection")
	case <-time.After(50 * time.Millisecond):
	}
}

func runCmd(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	return runCmdIn(t, strings.NewReader(""), args...)
}

func runCmdIn(t *testing.T, stdin io.Reader, args ...string) (string, string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, stdin, &stdout, &stderr)
	return stdout.String(), stderr.String(), code
}

func isolate(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("HOME", filepath.Join(root, "home"))
	return root
}
