package classify

import (
	"testing"

	"github.com/prippa/mail-sort/internal/message"
)

func TestRulesMatchBeforeAProvider(t *testing.T) {
	t.Parallel()
	set := Set{
		Categories: []Category{{
			Key: "invoices_receipts", Name: "Invoices", Description: "Bills. Not news.",
			Folder: "Invoices", Action: "move",
		}},
		Rules: []Rule{
			{FromDomain: "stripe.com", Category: "invoices_receipts"},
			{To: "me@example.com", Subject: `(?i)invoice`, Category: "invoices_receipts"},
			{Keywords: []string{"payroll"}, Category: "invoices_receipts"},
			{Attachment: ".pdf", Header: "X-Test", Category: "invoices_receipts"},
			{From: "boss@example.com", Action: NeverTouch},
		},
	}
	provider := &fakeProvider{name: "jev", model: "jev-1.13.0", min: 0.8, result: Result{Category: "invoices_receipts", Confidence: 1}}

	got, err := Classify(t.Context(), Input{Message: message.Message{
		From: []message.Address{{Name: "Stripe", Email: "Billing@News.Stripe.com"}},
	}}, set, []Provider{provider}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Category != "invoices_receipts" || got.Source != "rule" || got.Folder != "Invoices" || provider.calls != 0 {
		t.Fatalf("decision=%+v calls=%d", got, provider.calls)
	}

	got, err = Classify(t.Context(), Input{Message: message.Message{
		To:      []message.Address{{Email: "me+news@example.com"}},
		Subject: "Your Invoice",
	}}, set, []Provider{provider}, nil)
	if err != nil || got.Source != "rule" || provider.calls != 0 {
		t.Fatalf("decision=%+v err=%v calls=%d", got, err, provider.calls)
	}

	got, err = Classify(t.Context(), Input{Message: message.Message{
		To:      []message.Address{{Email: "me+news@example.com"}},
		Subject: "Hello",
		Body:    "Payroll is ready",
	}}, set, []Provider{provider}, nil)
	if err != nil || got.Category != "invoices_receipts" || provider.calls != 0 {
		t.Fatalf("keyword decision=%+v err=%v", got, err)
	}

	got, err = Classify(t.Context(), Input{
		Message: message.Message{Attachments: []message.Attachment{{Name: "a.PDF"}}},
		Headers: map[string][]string{"X-Test": {"1"}},
	}, set, []Provider{provider}, nil)
	if err != nil || got.Source != "rule" {
		t.Fatalf("attachment decision=%+v err=%v", got, err)
	}

	got, err = Classify(t.Context(), Input{Message: message.Message{
		From: []message.Address{{Email: "boss@example.com"}},
		Body: "please file this",
	}}, set, []Provider{provider}, nil)
	if err != nil || got.Action != NeverTouch || provider.calls != 0 {
		t.Fatalf("never_touch=%+v err=%v calls=%d", got, err, provider.calls)
	}
}

func TestPlusAddressDoesNotWidenATaggedRule(t *testing.T) {
	t.Parallel()
	if matchMailbox("me+news@example.com", "me@example.com") {
		t.Fatal("tagged rule matched the base address")
	}
	if !matchMailbox("me+news@example.com", "Me+News@Example.com") {
		t.Fatal("exact tagged address did not match")
	}
	if !matchMailbox("me@example.com", "me+news@example.com") {
		t.Fatal("base rule did not match a plus address")
	}
}
