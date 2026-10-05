package config

import (
	"os"
	"path/filepath"
	"strings"
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
			ClientID:    "desktop-client",
			Tenant:      "common",
			DeviceCode:  true,
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
	if len(loaded.Profiles) != 1 || loaded.Profiles[0].PasswordEnv != "MAIL_SORTER_PASSWORD_WORK" || loaded.Profiles[0].ClientID != "desktop-client" || loaded.Profiles[0].Tenant != "common" || !loaded.Profiles[0].DeviceCode {
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
	secretClient := []byte("profiles:\n  - name: Work\n    provider: gmail\n    username: ada@example.com\n    email: ada@example.com\n    auth: oauth_google\n    client_secret: nope\n")
	if _, err := parse(secretClient); err == nil || strings.Contains(err.Error(), "nope") {
		t.Fatalf("client secret err = %v", err)
	}
}
