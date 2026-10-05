package classify

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/prippa/mail-sort/internal/config"
)

const redactedText = "[redacted]"

// digitRunMin is a product choice. The spec says to mask long digit runs and
// does not set the length. Eight digits also covers many phone numbers.
// Email and phone masking wait for the privacy switches.
const digitRunMin = 8

var (
	cardPattern  = regexp.MustCompile(`\b\d(?:[ -]?\d){12,18}\b`)
	ibanPattern  = regexp.MustCompile(`(?i)\b[A-Z]{2}\d{2}[A-Z0-9 ]{11,48}`)
	digitRun     = regexp.MustCompile(`\d{` + strconv.Itoa(digitRunMin) + `,}`)
	emailPattern = regexp.MustCompile(`(?i)[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}`)
	phoneIntl    = regexp.MustCompile(`\+\d{1,3}(?:[\s().-]*\d){8,14}`)
	phoneParen   = regexp.MustCompile(`\(\d{3}\)[\s.-]*\d{3}[\s.-]*\d{4}`)
	phoneSpaced  = regexp.MustCompile(`\b\d{3}[\s.-]\d{3}[\s.-]\d{4}\b`)
)

// Redact masks card-like numbers, IBANs, and long digit runs.
func Redact(text string) string {
	return redact(text, config.Privacy{})
}

func redact(text string, priv config.Privacy) string {
	text = replaceIBANs(text)
	text = replaceCards(text)
	if priv.RedactEmail {
		text = emailPattern.ReplaceAllString(text, redactedText)
	}
	if priv.RedactPhone {
		text = phoneIntl.ReplaceAllString(text, redactedText)
		text = phoneParen.ReplaceAllString(text, redactedText)
		text = phoneSpaced.ReplaceAllString(text, redactedText)
	}
	return digitRun.ReplaceAllString(text, redactedText)
}

func replaceCards(text string) string {
	return cardPattern.ReplaceAllStringFunc(text, func(match string) string {
		digits := digitsOnly(match)
		if len(digits) < 13 || len(digits) > 19 {
			return match
		}
		return redactedText
	})
}

func replaceIBANs(text string) string {
	var b strings.Builder
	rest := text
	for {
		loc := ibanPattern.FindStringIndex(rest)
		if loc == nil {
			b.WriteString(rest)
			return b.String()
		}
		end, ok := validIBANEnd(rest[loc[0]:])
		if !ok {
			b.WriteString(rest[:loc[1]])
			rest = rest[loc[1]:]
			continue
		}
		b.WriteString(rest[:loc[0]])
		b.WriteString(redactedText)
		rest = rest[loc[0]+end:]
	}
}

func validIBANEnd(chunk string) (int, bool) {
	compact := make([]byte, 0, len(chunk))
	ends := make([]int, 0, len(chunk))
	for i := 0; i < len(chunk); {
		r, size := utf8.DecodeRuneInString(chunk[i:])
		if r == ' ' || r == '\t' {
			i += size
			continue
		}
		if r >= 'a' && r <= 'z' {
			r = r - 'a' + 'A'
		}
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			break
		}
		compact = append(compact, byte(r))
		i += size
		ends = append(ends, i)
		if len(compact) > 34 {
			break
		}
	}
	for n := len(compact); n >= 15; n-- {
		if ibanOK(string(compact[:n])) {
			return ends[n-1], true
		}
	}
	return 0, false
}

func ibanOK(value string) bool {
	if len(value) < 15 || len(value) > 34 {
		return false
	}
	rearranged := value[4:] + value[:4]
	n := 0
	for i := 0; i < len(rearranged); i++ {
		r := rearranged[i]
		switch {
		case r >= '0' && r <= '9':
			n = (n*10 + int(r-'0')) % 97
		case r >= 'A' && r <= 'Z':
			v := int(r-'A') + 10
			n = (n*10 + v/10) % 97
			n = (n*10 + v%10) % 97
		default:
			return false
		}
	}
	return n == 1
}

func digitsOnly(value string) string {
	var b strings.Builder
	for _, r := range value {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
