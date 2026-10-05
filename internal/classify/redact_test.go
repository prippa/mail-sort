package classify

import (
	"strings"
	"testing"

	"github.com/prippa/mail-sort/internal/message"
)

func TestRedactCardsIBANAndDigitRuns(t *testing.T) {
	t.Parallel()
	in := "card 4111 1111 1111 1111 iban GB82 WEST 1234 5698 7654 32 run 12345678 keep 1234567"
	got := Redact(in)
	if strings.Contains(got, "4111") || strings.Contains(got, "WEST") || strings.Contains(got, "12345678") {
		t.Fatalf("redact = %q", got)
	}
	if !strings.Contains(got, "1234567") || strings.Count(got, redactedText) < 3 {
		t.Fatalf("redact = %q", got)
	}
}

func TestRulesSeeUnredactedText(t *testing.T) {
	t.Parallel()
	set := Set{
		Categories: []Category{{
			Key: "work", Name: "Work", Description: "Job mail. Not personal.",
			Folder: "Work", Action: "move",
		}},
		Rules: []Rule{{Keywords: []string{"4111111111111111"}, Category: "work"}},
	}
	got, err := Classify(t.Context(), Input{Message: message.Message{Body: "4111111111111111"}}, set, nil, nil)
	if err != nil || got.Source != "rule" {
		t.Fatalf("decision=%+v err=%v", got, err)
	}
}
