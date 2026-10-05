package config

import (
	"path/filepath"
	"testing"
)

func TestAppDir(t *testing.T) {
	t.Parallel()
	if appDir("windows") != "MailSorter" {
		t.Fatalf("windows dir = %q", appDir("windows"))
	}
	if appDir("linux") != "mailsorter" {
		t.Fatalf("linux dir = %q", appDir("linux"))
	}
}

func TestDirUsesXDGConfigHome(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	t.Setenv("HOME", t.TempDir())

	dir, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(base, "mailsorter") {
		t.Fatalf("Dir() = %q", dir)
	}
	path, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, "config.yaml") {
		t.Fatalf("Path() = %q", path)
	}
}

func TestStateDir(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	t.Setenv("HOME", t.TempDir())

	dir, err := StateDir()
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(base, "mailsorter") {
		t.Fatalf("StateDir() = %q", dir)
	}
}

func TestStateDirIgnoresRelativeXDG(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", "relative/state")

	dir, err := StateDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".local", "state", "mailsorter")
	if dir != want {
		t.Fatalf("StateDir() = %q, want %q", dir, want)
	}
}
