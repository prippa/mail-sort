// Package i18n holds the English and Russian UI catalogs.
// Model-facing category criteria stay English; this catalog is for display.
package i18n

import "strings"

// Lang is a UI language code.
type Lang string

const (
	EN Lang = "en"
	RU Lang = "ru"
)

// catalog keys are display strings. Every EN key must have an RU entry.
var catalog = map[Lang]map[string]string{
	EN: {
		"app.name":               "MailSorter",
		"action.start":           "Start",
		"action.cancel":          "Cancel",
		"action.next":            "Next",
		"category.needs_review":  "Needs review",
		"category.keep_in_inbox": "Keep in Inbox",
		"category.never_touch":   "Do not touch",
	},
	RU: {
		"app.name":               "MailSorter",
		"action.start":           "Старт",
		"action.cancel":          "Отмена",
		"action.next":            "Далее",
		"category.needs_review":  "Нужно проверить",
		"category.keep_in_inbox": "Оставить во входящих",
		"category.never_touch":   "Не трогать",
	},
}

// T returns the string for key. A missing translation falls back to English,
// then to the key itself so a gap is visible in the UI.
func T(lang Lang, key string) string {
	if value, ok := lookup(lang, key); ok {
		return value
	}
	if value, ok := lookup(EN, key); ok {
		return value
	}
	return key
}

func lookup(lang Lang, key string) (string, bool) {
	table, ok := catalog[lang]
	if !ok {
		return "", false
	}
	value, ok := table[key]
	if !ok || value == "" {
		return "", false
	}
	return value, true
}

// Detect maps a locale such as ru_RU.UTF-8 to a catalog language.
func Detect(locale string) Lang {
	locale = strings.ToLower(locale)
	if strings.HasPrefix(locale, "ru") {
		return RU
	}
	return EN
}

// FromEnv reads LC_ALL, LC_MESSAGES, then LANG. C and POSIX mean English.
func FromEnv(getenv func(string) string) Lang {
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		value := getenv(key)
		if value == "" || value == "C" || value == "POSIX" {
			continue
		}
		return Detect(value)
	}
	return EN
}
