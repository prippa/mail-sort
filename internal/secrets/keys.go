// Package secrets names where credentials live and which keys must never be
// written to config or logs. Storage backends arrive in a later phase.
package secrets

import "strings"

const (
	// EnvTypeSafeAPIKey overrides the keyring entry for the Jev API key.
	EnvTypeSafeAPIKey = "TYPESAFE_API_KEY"
	// EnvMasterPassword unlocks the encrypted secret file when Secret Service
	// is unavailable. Headless runs require it in that case.
	EnvMasterPassword = "MAIL_SORTER_MASTER_PASSWORD"
	// EnvPasswordPrefix plus a profile's password_env suffix is the headless
	// mailbox password. The config stores the variable name, not the value.
	EnvPasswordPrefix = "MAIL_SORTER_PASSWORD_"
	// ServiceName is the keyring service. Entries hold refresh tokens and
	// other long-lived secrets, not access tokens.
	ServiceName = "mailsorter"
)

// CanonicalKey folds a YAML or log key so comparisons ignore case and hyphens.
func CanonicalKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	return strings.ReplaceAll(key, "-", "_")
}

// IsSecretConfigKey reports whether a config field would store a credential.
// password_env is a variable name, so it is allowed in YAML.
func IsSecretConfigKey(key string) bool {
	canonical := CanonicalKey(key)
	if canonical == "password_env" {
		return false
	}
	return secretLike(canonical)
}

// IsSensitiveLogKey reports whether a slog attribute value must be redacted.
// Subjects are redacted unless the user opted in. Bodies always are.
func IsSensitiveLogKey(key string, logSubjects bool) bool {
	canonical := CanonicalKey(key)
	switch canonical {
	case "body", "excerpt", "raw", "raw_body", "text", "html", "content", "message_body":
		return true
	case "subject":
		return !logSubjects
	default:
		return secretLike(canonical)
	}
}

func secretLike(canonical string) bool {
	switch canonical {
	case "password", "passwd", "secret", "token", "api_key", "apikey",
		"access_token", "refresh_token", "client_secret", "authorization",
		"master_password", "bearer", "private_key", "id_token":
		return true
	}
	if strings.Contains(canonical, "api_key") || strings.Contains(canonical, "apikey") || strings.Contains(canonical, "client_secret") {
		return true
	}
	for part := range strings.SplitSeq(canonical, "_") {
		switch part {
		case "password", "passwd", "secret", "token", "bearer":
			return true
		}
	}
	return false
}
