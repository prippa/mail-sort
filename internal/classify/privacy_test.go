package classify

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/prippa/mail-sort/internal/config"
	"github.com/prippa/mail-sort/internal/message"
)

func TestOptionalEmailAndPhoneRedaction(t *testing.T) {
	t.Parallel()
	in := "mail ada@example.com or +1 415 555 0134 or (415) 555-0199 or 415-555-0100 today"
	plain := Redact(in)
	if strings.Contains(plain, "[redacted]") && !strings.Contains(plain, "ada@example.com") {
		t.Fatalf("default redaction removed the email: %s", plain)
	}
	if !strings.Contains(plain, "ada@example.com") || !strings.Contains(plain, "415-555-0100") {
		t.Fatalf("default = %s", plain)
	}
	got := redact(in, config.Privacy{RedactEmail: true, RedactPhone: true})
	for _, leak := range []string{"ada@example.com", "415", "0199", "0100"} {
		if strings.Contains(got, leak) {
			t.Fatalf("leak %q in %s", leak, got)
		}
	}
	if redact("order 12", config.Privacy{RedactPhone: true}) != "order 12" {
		t.Fatal("short number was masked")
	}
}

func TestSubjectOnlyAndRulesOnly(t *testing.T) {
	t.Parallel()
	ctx := WithPrivacy(context.Background(), config.Privacy{SubjectOnly: true, RedactEmail: true})
	got := OutgoingFeatures(ctx, Input{Message: message.Message{
		From:    []message.Address{{Email: "ada@example.com"}},
		To:      []message.Address{{Email: "bob@example.com"}},
		Subject: "Invoice",
		Body:    "4111111111111111",
	}})
	if got.Body != "" || got.To != "" || got.Subject != "Invoice" || strings.Contains(got.From, "ada@") {
		t.Fatalf("%+v", got)
	}

	called := false
	provider := recordingProvider{called: &called}
	decision, err := Classify(WithPrivacy(context.Background(), config.Privacy{RulesOnly: true}), Input{
		Message: message.Message{Subject: "hello"},
	}, Starter(), []Provider{provider}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if called || decision.Category != NeedsReview || decision.Source != "gate" {
		t.Fatalf("called=%v decision=%+v", called, decision)
	}
}

func TestLocalOnlyDoesNotDial(t *testing.T) {
	t.Parallel()
	client := &jevClient{opts: providerOpts{
		kind: "jev", model: "jev-1.13.0", baseURL: "https://api.typesafe.ai", apiKey: "test-key-value",
		http: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Error("dialed")
			return nil, errors.New("dialed")
		})},
	}}
	_, err := client.Classify(WithPrivacy(t.Context(), config.Privacy{LocalOnly: true}), Features{Attachments: []string{}}, Starter().Categories)
	if err == nil || !strings.Contains(err.Error(), "local-only") {
		t.Fatal(err)
	}
}

func TestRemoteConsentID(t *testing.T) {
	t.Parallel()
	id, ok := RemoteConsentID([]config.Classifier{{Provider: "jev"}}, config.Privacy{})
	if !ok || id == "" {
		t.Fatal("jev needs consent")
	}
	if _, ok := RemoteConsentID([]config.Classifier{{Provider: "jev"}}, config.Privacy{RulesOnly: true}); ok {
		t.Fatal("rules only still required consent")
	}
	if _, ok := RemoteConsentID([]config.Classifier{{
		Provider: "openai_compatible",
		Model:    "local",
		BaseURL:  "http://127.0.0.1:11434/v1",
	}}, config.Privacy{}); ok {
		t.Fatal("loopback required consent")
	}
	next, ok := RemoteConsentID([]config.Classifier{{Provider: "jev"}}, config.Privacy{SubjectOnly: true})
	if !ok || next == id {
		t.Fatal("subject-only consent id did not change")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

type recordingProvider struct {
	called *bool
}

func (p recordingProvider) Name() string           { return "fake" }
func (p recordingProvider) Model() string          { return "fake" }
func (p recordingProvider) MinConfidence() float64 { return 0.7 }
func (p recordingProvider) MinMargin() float64     { return 0 }
func (p recordingProvider) PriceInput() float64    { return 0 }
func (p recordingProvider) PriceOutput() float64   { return 0 }
func (p recordingProvider) Classify(context.Context, Features, []Category) (Result, error) {
	*p.called = true
	return Result{}, errors.New("should not be called")
}
