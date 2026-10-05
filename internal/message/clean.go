package message

import (
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	spaceRun = regexp.MustCompile(`[ \t]+`)
	urlRun   = regexp.MustCompile(`https?://[^\s<>"']+`)
	wrote    = regexp.MustCompile(`(?i)^on .{1,300}wrote:\s*$`)
	russian  = regexp.MustCompile(`(?i)^.{0,200}написал(?:а|\(а\))?\s*:\s*$`)
	forward  = regexp.MustCompile(`(?i)^(?:begin forwarded message:?|-{2,}\s*original message\s*-{2,}|-{2,}\s*пересылаемое сообщение\s*-{2,})\s*$`)
	sigLine  = regexp.MustCompile(`^--[ \t]*$`)
)

// Clean strips quoted replies and signatures, replaces URLs with their
// hostname, collapses whitespace, and caps the result at maxChars runes.
// If stripping would leave the body empty, the unstripped text is collapsed
// and capped instead, so a message that is only a quote still has content.
func Clean(text string, maxChars int) string {
	if maxChars <= 0 {
		maxChars = DefaultMaxChars
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	stripped := stripQuotes(text)
	if strings.TrimSpace(stripped) == "" {
		stripped = text
	}
	stripped = replaceURLs(stripped)
	stripped = collapse(stripped)
	return capRunes(stripped, maxChars)
}

func stripQuotes(text string) string {
	lines := strings.Split(text, "\n")
	cut := len(lines)
	for i, line := range lines {
		if !quoteIntro(line) && !sigLine.MatchString(line) {
			continue
		}
		if strings.TrimSpace(strings.Join(lines[:i], "")) == "" {
			continue
		}
		cut = i
		break
	}
	lines = lines[:cut]
	end := len(lines)
	for end > 0 {
		trimmed := strings.TrimSpace(lines[end-1])
		if trimmed != "" && !strings.HasPrefix(trimmed, ">") {
			break
		}
		end--
	}
	if end < len(lines) && strings.TrimSpace(strings.Join(lines[:end], "")) != "" {
		lines = lines[:end]
	}
	return strings.Join(lines, "\n")
}

func quoteIntro(line string) bool {
	trimmed := strings.TrimSpace(line)
	return wrote.MatchString(trimmed) || russian.MatchString(trimmed) || forward.MatchString(trimmed)
}

func replaceURLs(text string) string {
	return urlRun.ReplaceAllStringFunc(text, func(match string) string {
		trimmed := strings.TrimRight(match, ".,);:!?")
		parsed, err := url.Parse(trimmed)
		if err != nil || parsed.Hostname() == "" {
			return "[link]"
		}
		return parsed.Hostname()
	})
}

func collapse(text string) string {
	text = strings.ReplaceAll(text, "\u00a0", " ")
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	blank := false
	for _, line := range lines {
		line = strings.TrimSpace(spaceRun.ReplaceAllString(line, " "))
		if line == "" {
			if len(out) > 0 && !blank {
				out = append(out, "")
				blank = true
			}
			continue
		}
		blank = false
		out = append(out, line)
	}
	if len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}

func capRunes(text string, maxChars int) string {
	if utf8.RuneCountInString(text) <= maxChars {
		return text
	}
	runes := []rune(text)
	return string(runes[:maxChars])
}
