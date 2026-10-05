package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	_, stderr, code := runCmd(t, "run")
	if code != 2 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "run is not implemented yet (phase 3)") {
		t.Fatalf("stderr=%q", stderr)
	}
}

func TestDefaultCommandIsUI(t *testing.T) {
	isolate(t)
	_, stderr, code := runCmd(t)
	if code != 2 || !strings.Contains(stderr, "ui is not implemented yet (phase 4)") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
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

func runCmd(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
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
