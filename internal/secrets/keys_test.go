package secrets

import "testing"

func TestIsSecretConfigKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		key  string
		want bool
	}{
		{key: "password", want: true},
		{key: "Password", want: true},
		{key: "api-key", want: true},
		{key: "refresh_token", want: true},
		{key: "imap_password", want: true},
		{key: "client_secret", want: true},
		{key: "password_env", want: false},
		{key: "client_id", want: false},
		{key: "host", want: false},
		{key: "username", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			t.Parallel()
			if got := IsSecretConfigKey(tt.key); got != tt.want {
				t.Fatalf("IsSecretConfigKey(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestIsSensitiveLogKey(t *testing.T) {
	t.Parallel()
	if !IsSensitiveLogKey("subject", false) {
		t.Fatal("subject is redacted unless logging subjects is enabled")
	}
	if IsSensitiveLogKey("subject", true) {
		t.Fatal("subject is kept when logging subjects is enabled")
	}
	if !IsSensitiveLogKey("body", true) {
		t.Fatal("body is always redacted")
	}
	if !IsSensitiveLogKey("password_env", false) {
		t.Fatal("password_env is redacted in logs")
	}
	if IsSensitiveLogKey("host", false) {
		t.Fatal("host is not a secret")
	}
}
