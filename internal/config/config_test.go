package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAcceptsProfile(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, `
language: ru
profiles:
  - name: Work
    provider: gmail
    host: imap.gmail.com
    port: 993
    security: implicit_tls
    username: ada@example.com
    password_env: MAIL_SORTER_PASSWORD_WORK
    email: ada@example.com
`)
	cfg, err := Load(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Language != "ru" {
		t.Fatalf("language = %q", cfg.Language)
	}
	if len(cfg.Profiles) != 1 {
		t.Fatalf("profiles = %d", len(cfg.Profiles))
	}
	got := cfg.Profiles[0]
	if got.Name != "Work" || got.Port != 993 || got.Security != "implicit_tls" || got.PasswordEnv != "MAIL_SORTER_PASSWORD_WORK" {
		t.Fatalf("profile = %+v", got)
	}
}

func TestLoadEmptyFile(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, "\n# nothing yet\n")
	cfg, err := Load(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Language != "" || len(cfg.Profiles) != 0 {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestLoadRejectsSecretsAndBadShape(t *testing.T) {
	t.Parallel()
	const secret = "hunter2-do-not-echo"
	tests := []struct {
		name    string
		body    string
		want    string
		wantErr bool
	}{
		{
			name: "password field",
			body: "password: " + secret + "\n",
			want: `field "password"`,
		},
		{
			name: "nested api key",
			body: "profiles:\n  - name: a\n    host:\n      api_key: " + secret + "\n",
			want: `field "api_key"`,
		},
		{
			name: "hyphenated token",
			body: "refresh-token: " + secret + "\n",
			want: `field "refresh-token"`,
		},
		{
			name: "unknown field",
			body: "nickname: ada\n",
			want: `unknown field "nickname"`,
		},
		{
			name: "bad port",
			body: "profiles:\n  - name: a\n    port: " + secret + "\n",
			want: `field "port"`,
		},
		{
			name: "bad security",
			body: "profiles:\n  - name: a\n    security: " + secret + "\n",
			want: `field "security"`,
		},
		{
			name: "alias",
			body: "a: &id\n  name: a\nprofiles:\n  - *id\n",
			want: "anchors and aliases",
		},
		{
			name: "syntax",
			body: "profiles: [\n  \"" + secret + "\"\n",
			want: "invalid YAML",
		},
		{
			name: "duplicate profile",
			body: "profiles:\n  - name: Work\n  - name: Work\n",
			want: `duplicate profile name "Work"`,
		},
		{
			name: "missing name",
			body: "profiles:\n  - host: imap.example\n",
			want: "missing a name",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := writeConfig(t, tt.body)
			_, err := Load(context.Background(), path)
			if err == nil {
				t.Fatal("expected an error")
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatal("error contains the secret value")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q does not contain %q", err.Error(), tt.want)
			}
		})
	}
}

func TestLoadTooLarge(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := make([]byte, maxConfigBytes+1)
	for i := range body {
		body[i] = 'a'
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "1MiB") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadPhase1Fields(t *testing.T) {
	t.Parallel()
	const fingerprint = "ab"
	path := writeConfig(t, `
profiles:
  - name: Work
    provider: zoho
    host_id: personal
    auth: password
    discover: false
    max_chars: 800
    cert_sha256: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
    ca_file: /tmp/ca.pem
    password_env: MAIL_SORTER_PASSWORD_WORK
`)
	cfg, err := Load(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.Profiles[0]
	if got.HostID != "personal" || got.Auth != "password" || got.Discover || got.MaxChars != 800 || got.CAFile != "/tmp/ca.pem" {
		t.Fatalf("profile = %+v", got)
	}
	bad := writeConfig(t, "profiles:\n  - name: Work\n    cert_sha256: "+fingerprint+"\n")
	_, err = Load(context.Background(), bad)
	if err == nil || strings.Contains(err.Error(), fingerprint) || !strings.Contains(err.Error(), "cert_sha256") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadCanceled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Load(ctx, filepath.Join(t.TempDir(), "config.yaml"))
	if err == nil {
		t.Fatal("expected cancellation")
	}
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
