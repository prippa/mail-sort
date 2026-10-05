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
		"run.dry":                "Dry run. Nothing was moved.",
		"run.confirmed":          "Confirmation stored. Mail stays where it is until you apply.",
		"run.applied":            "Apply finished.",
		"run.pending":            "Apply stopped at the move cap. Run apply again for the rest.",
		"run.not_confirmed":      "This profile is not confirmed. Nothing was moved.",
		"run.need_dry_run":       "Run a dry run before confirming.",
		"run.nothing_to_apply":   "There is no dry run to apply.",
		"run.stopped":            "Stopped after 5 consecutive classifier errors. Later messages were left in place.",
		"run.copy_only":          "This server cannot move mail. Apply again with --copy-only to copy and leave the original.",
		"undo.done":              "Undo finished.",
		"undo.nothing":           "There is no applied run to undo.",
		"undo.left_copy":         "Left in place. Removing a copy would delete mail.",
	},
	RU: {
		"app.name":               "MailSorter",
		"action.start":           "Старт",
		"action.cancel":          "Отмена",
		"action.next":            "Далее",
		"category.needs_review":  "Нужно проверить",
		"category.keep_in_inbox": "Оставить во входящих",
		"category.never_touch":   "Не трогать",
		"run.dry":                "Пробный запуск. Письма остались на месте.",
		"run.confirmed":          "Подтверждение записано. Почта останется на месте, пока запуск не применён.",
		"run.applied":            "Применение завершено.",
		"run.pending":            "Применение остановлено на пределе перемещений. Запустите применение ещё раз для остальных.",
		"run.not_confirmed":      "Этот профиль не подтверждён. Письма не перемещались.",
		"run.need_dry_run":       "Сначала выполните пробный запуск.",
		"run.nothing_to_apply":   "Нет пробного запуска, который можно применить.",
		"run.stopped":            "Остановка после пяти ошибок классификатора подряд. Остальные письма оставлены на месте.",
		"run.copy_only":          "Сервер не умеет перемещать почту. Повторите применение с --copy-only, чтобы скопировать письмо и оставить оригинал.",
		"undo.done":              "Отмена завершена.",
		"undo.nothing":           "Нет применённого запуска, который можно отменить.",
		"undo.left_copy":         "Оставлено на месте. Удаление копии удалило бы письмо.",
	},
}

// Catalog is a copy of the display strings for lang.
func Catalog(lang Lang) map[string]string {
	out := make(map[string]string, len(catalog[EN]))
	for key, english := range catalog[EN] {
		out[key] = english
		if value, ok := lookup(lang, key); ok {
			out[key] = value
		}
	}
	return out
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
