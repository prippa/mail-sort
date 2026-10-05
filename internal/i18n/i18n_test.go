package i18n

import "testing"

func TestCatalogsMatch(t *testing.T) {
	t.Parallel()
	if len(catalog[EN]) == 0 {
		t.Fatal("english catalog is empty")
	}
	if len(catalog[EN]) != len(catalog[RU]) {
		t.Fatalf("en=%d ru=%d", len(catalog[EN]), len(catalog[RU]))
	}
	for key, value := range catalog[EN] {
		if value == "" {
			t.Fatalf("empty english value for %q", key)
		}
		ru, ok := catalog[RU][key]
		if !ok {
			t.Fatalf("missing russian key %q", key)
		}
		if ru == "" {
			t.Fatalf("empty russian value for %q", key)
		}
	}
	for key := range catalog[RU] {
		if _, ok := catalog[EN][key]; !ok {
			t.Fatalf("russian key %q has no english entry", key)
		}
	}
}

func TestDetect(t *testing.T) {
	t.Parallel()
	tests := []struct {
		locale string
		want   Lang
	}{
		{locale: "ru_RU.UTF-8", want: RU},
		{locale: "ru", want: RU},
		{locale: "en_US.UTF-8", want: EN},
		{locale: "", want: EN},
	}
	for _, tt := range tests {
		if got := Detect(tt.locale); got != tt.want {
			t.Fatalf("Detect(%q) = %q, want %q", tt.locale, got, tt.want)
		}
	}
}

func TestFromEnv(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		"LC_ALL":      "C",
		"LC_MESSAGES": "ru_RU.UTF-8",
		"LANG":        "en_US.UTF-8",
	}
	getenv := func(key string) string { return env[key] }
	if got := FromEnv(getenv); got != RU {
		t.Fatalf("FromEnv = %q", got)
	}
	env["LC_ALL"] = "en_GB.UTF-8"
	if got := FromEnv(getenv); got != EN {
		t.Fatalf("FromEnv = %q", got)
	}
}

func TestMissingKeyFallsBack(t *testing.T) {
	t.Parallel()
	if got := T(RU, "missing.key"); got != "missing.key" {
		t.Fatalf("T = %q", got)
	}
	if got := T(RU, "action.start"); got != "Старт" {
		t.Fatalf("T = %q", got)
	}
}
