package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveRoundTripAndPrivacy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := Config{
		Language: "ru",
		Profiles: []Profile{{
			Name:        "Work",
			Provider:    "fastmail",
			Username:    "ada@example.com",
			Email:       "ada@example.com",
			PasswordEnv: "MAIL_SORTER_PASSWORD_WORK",
			Auth:        "password",
		}},
		Privacy: Privacy{RedactEmail: true, LocalOnly: true},
	}
	if err := Save(t.Context(), path, cfg); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	loaded, err := Load(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Language != "ru" || !loaded.Privacy.RedactEmail || !loaded.Privacy.LocalOnly || loaded.Privacy.RulesOnly {
		t.Fatalf("%+v", loaded)
	}
	if len(loaded.Profiles) != 1 || loaded.Profiles[0].PasswordEnv != "MAIL_SORTER_PASSWORD_WORK" {
		t.Fatalf("%+v", loaded.Profiles)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "" {
		t.Fatal("empty config")
	}

	bad := []byte("privacy:\n  token: true\n")
	if _, err := parse(bad); err == nil {
		t.Fatal("secret-like privacy field was accepted")
	}
	unknown := []byte("privacy:\n  extra: true\n")
	if _, err := parse(unknown); err == nil {
		t.Fatal("unknown privacy field was accepted")
	}
}
