package classify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/url"
	"sort"
	"strings"

	"github.com/prippa/mail-sort/internal/config"
)

type privacyKey struct{}

// WithPrivacy attaches the classifier privacy switches to ctx.
func WithPrivacy(ctx context.Context, privacy config.Privacy) context.Context {
	return context.WithValue(ctx, privacyKey{}, privacy)
}

// PrivacyFrom returns the switches stored by WithPrivacy.
func PrivacyFrom(ctx context.Context) config.Privacy {
	privacy, _ := ctx.Value(privacyKey{}).(config.Privacy)
	return privacy
}

// LoopbackBase reports whether baseURL points at this computer.
func LoopbackBase(baseURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Host == "" {
		return false
	}
	host := parsed.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// RemoteConsentID identifies the remote classifiers and the privacy switches
// that change what they would receive. ok is false when no remote call can run.
func RemoteConsentID(specs []config.Classifier, privacy config.Privacy) (string, bool) {
	if privacy.RulesOnly {
		return "", false
	}
	parts := make([]string, 0, len(specs))
	for _, spec := range specs {
		base := classifierBase(spec)
		if base == "" || LoopbackBase(base) {
			continue
		}
		parts = append(parts, spec.Provider+"\n"+spec.Model+"\n"+base)
	}
	if len(parts) == 0 {
		return "", false
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n---\n") + privacySuffix(privacy)))
	return hex.EncodeToString(sum[:]), true
}

func classifierBase(spec config.Classifier) string {
	if base := strings.TrimSpace(spec.BaseURL); base != "" {
		return base
	}
	switch spec.Provider {
	case "jev":
		return defaultJevBase
	case "anthropic":
		return defaultAnthropic
	default:
		return ""
	}
}

func privacySuffix(privacy config.Privacy) string {
	return "\nemail=" + boolText(privacy.RedactEmail) +
		"\nphone=" + boolText(privacy.RedactPhone) +
		"\nsubject=" + boolText(privacy.SubjectOnly)
}

func boolText(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

func shapeFeatures(features Features, privacy config.Privacy) Features {
	if !privacy.SubjectOnly {
		return features
	}
	return Features{
		From:        features.From,
		Subject:     features.Subject,
		Attachments: []string{},
	}
}

// OutgoingFeatures is the redacted field list a provider would receive.
// It does not call a provider.
func OutgoingFeatures(ctx context.Context, in Input) Features {
	privacy := PrivacyFrom(ctx)
	return redactFeatures(shapeFeatures(featuresFrom(in), privacy), privacy)
}
