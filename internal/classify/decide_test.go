package classify

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/prippa/mail-sort/internal/message"
)

func TestChoiceConfidence(t *testing.T) {
	t.Parallel()
	if math.Abs(ChoiceConfidence(0.7, 4)-0.6) > 1e-9 {
		t.Fatalf("n=4 got %v", ChoiceConfidence(0.7, 4))
	}
	if math.Abs(ChoiceConfidence(0.9, 3)-0.85) > 1e-9 {
		t.Fatalf("n=3 got %v", ChoiceConfidence(0.9, 3))
	}
	if ChoiceConfidence(0.1, 4) != 0 {
		t.Fatalf("below even split = %v", ChoiceConfidence(0.1, 4))
	}
}

func TestChain(t *testing.T) {
	t.Parallel()
	set := Set{Categories: []Category{{
		Key: "work", Name: "Work", Description: "Job mail. Not personal.",
		Folder: "Work", Action: "move", MinConfidence: 0.9,
	}}}
	low := &fakeProvider{name: "jev", model: "jev-1.13.0", min: 0.8, result: Result{Category: "work", Confidence: 0.85, Model: "jev-1.13.0"}}
	high := &fakeProvider{name: "openai_compatible", model: "user-model", min: 0.7, result: Result{Category: "work", Confidence: 0.95, Model: "user-model", Reason: "payroll"}}
	got, err := Classify(t.Context(), Input{Message: message.Message{Subject: "hello"}}, set, []Provider{low, high}, NewMemory())
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != "openai_compatible" || got.Source != "openai_compatible" || got.Folder != "Work" || low.calls != 1 || high.calls != 1 {
		t.Fatalf("decision=%+v low=%d high=%d", got, low.calls, high.calls)
	}

	blocked := &fakeProvider{name: "jev", model: "jev-1.13.0", min: 0.8, err: &KeyError{Env: "TYPESAFE_API_KEY"}}
	next := &fakeProvider{name: "openai_compatible", model: "user-model", min: 0.7, result: Result{Category: "work", Confidence: 0.99}}
	_, err = Classify(t.Context(), Input{Message: message.Message{Subject: "hello"}}, set, []Provider{blocked, next}, nil)
	if !errors.Is(err, ErrNoAPIKey) || next.calls != 0 {
		t.Fatalf("err=%v next=%d", err, next.calls)
	}

	denied := &fakeProvider{name: "jev", model: "jev-1.13.0", min: 0.8, err: &AuthError{Provider: "jev"}}
	_, err = Classify(t.Context(), Input{Message: message.Message{Subject: "hello"}}, set, []Provider{denied, next}, nil)
	if !errors.Is(err, ErrUnauthorized) || next.calls != 0 {
		t.Fatalf("err=%v next=%d", err, next.calls)
	}

	broken := &fakeProvider{name: "jev", model: "jev-1.13.0", min: 0.8, err: errors.New("classify: jev returned HTTP 500")}
	other := &fakeProvider{name: "anthropic", model: "user-model", min: 0.7, err: errors.New("classify: anthropic returned HTTP 500")}
	_, err = Classify(t.Context(), Input{Message: message.Message{Subject: "hello"}}, set, []Provider{broken, other}, nil)
	if err == nil || broken.calls != 1 || other.calls != 1 {
		t.Fatalf("err=%v broken=%d other=%d", err, broken.calls, other.calls)
	}

	uncertain := &fakeProvider{name: "jev", model: "jev-1.13.0", min: 0.8, margin: 0.2, result: Result{Category: "work", Confidence: 0.99, Model: "jev-1.13.0"}}
	got, err = Classify(t.Context(), Input{Message: message.Message{Subject: "hello"}}, set, []Provider{uncertain}, nil)
	if err != nil || got.Category != NeedsReview || got.Source != "gate" {
		t.Fatalf("decision=%+v err=%v", got, err)
	}
}

func TestCacheSkipsASecondCall(t *testing.T) {
	t.Parallel()
	set := Set{Categories: []Category{{
		Key: "work", Name: "Work", Description: "Job mail. Not personal.",
		Folder: "Work", Action: "move",
	}}}
	provider := &fakeProvider{name: "jev", model: "jev-1.13.0", min: 0.8, result: Result{Category: "work", Confidence: 0.91, Model: "jev-1.13.0"}}
	cache := NewMemory()
	in := Input{Message: message.Message{Subject: "hello", Body: "body"}}
	if _, err := Classify(t.Context(), in, set, []Provider{provider}, cache); err != nil {
		t.Fatal(err)
	}
	if _, err := Classify(t.Context(), in, set, []Provider{provider}, cache); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 {
		t.Fatalf("calls=%d", provider.calls)
	}
	second, err := Classify(t.Context(), in, set, []Provider{provider}, cache)
	if err != nil || second.Source != "cache" || second.Folder != "Work" {
		t.Fatalf("decision=%+v err=%v", second, err)
	}
}

func TestCacheKeyChangesWithFeaturesAndCategories(t *testing.T) {
	t.Parallel()
	cats := withReserved([]Category{{Key: "work", Description: "Job mail. Not personal.", Folder: "Work", Action: "move"}})
	left := featuresFrom(Input{Message: message.Message{Body: "one"}})
	right := left
	right.Body = "two"
	a, err := cacheKey(left, cats, "jev", "jev-1.13.0", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := cacheKey(right, cats, "jev", "jev-1.13.0", "")
	if err != nil || a == b {
		t.Fatalf("features did not change the key")
	}
	cats[0].Description = "Different rubric. Not the old one."
	c, err := cacheKey(left, cats, "jev", "jev-1.13.0", "")
	if err != nil || a == c {
		t.Fatal("categories did not change the key")
	}
}

func TestNoClassifier(t *testing.T) {
	t.Parallel()
	_, err := Classify(t.Context(), Input{Message: message.Message{Subject: "hi"}}, Starter(), nil, nil)
	if err == nil || err.Error() != "classify: no classifier is enabled" {
		t.Fatal(err)
	}
}

type fakeProvider struct {
	name    string
	model   string
	min     float64
	margin  float64
	calls   int
	err     error
	result  Result
	lastCtx context.Context
}

func (f *fakeProvider) Name() string           { return f.name }
func (f *fakeProvider) Model() string          { return f.model }
func (f *fakeProvider) MinConfidence() float64 { return f.min }
func (f *fakeProvider) MinMargin() float64     { return f.margin }
func (f *fakeProvider) PriceInput() float64    { return 0 }
func (f *fakeProvider) PriceOutput() float64   { return 0 }
func (f *fakeProvider) Classify(ctx context.Context, _ Features, _ []Category) (Result, error) {
	f.calls++
	f.lastCtx = ctx
	if f.err != nil {
		return Result{}, f.err
	}
	return f.result, nil
}

type urgentFake struct {
	*fakeProvider
	cutoff float64
}

func (u urgentFake) UrgentMin() float64 { return u.cutoff }

func TestUrgentMoveStaysInInbox(t *testing.T) {
	t.Parallel()
	cats := withReserved([]Category{{Key: "work", Description: "Job mail. Not personal.", Folder: "Work", Action: "move"}})
	set := Set{Categories: cats}
	provider := urgentFake{
		fakeProvider: &fakeProvider{
			name: "jev", model: "jev-1.13.0", min: 0.8,
			result: Result{Category: "work", Confidence: 0.95, HasNoul: true, Noul: 0.91},
		},
		cutoff: 0.8,
	}
	decision, err := Classify(t.Context(), Input{Message: message.Message{Subject: "Need this today"}}, set, []Provider{provider}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Urgent || decision.Action != "none" || decision.Folder != "" || decision.Category != "work" {
		t.Fatalf("decision=%+v", decision)
	}

	provider.result.Noul = 0.2
	provider.calls = 0
	quiet, err := Classify(t.Context(), Input{Message: message.Message{Subject: "Need this today"}}, set, []Provider{provider}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if quiet.Urgent || quiet.Action != "move" || quiet.Folder != "Work" {
		t.Fatalf("quiet=%+v", quiet)
	}

	provider.result.Noul = 0.95
	provider.calls = 0
	labelSet := Set{Categories: []Category{{Key: "work", Description: "Job mail. Not personal.", Folder: "Work", Action: "label"}}}
	labeled, err := Classify(t.Context(), Input{Message: message.Message{Subject: "Need this today"}}, labelSet, []Provider{provider}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !labeled.Urgent || labeled.Action != "label" || labeled.Folder != "Work" {
		t.Fatalf("label=%+v", labeled)
	}

	provider.result.Confidence = 0.2
	provider.calls = 0
	uncertain, err := Classify(t.Context(), Input{Message: message.Message{Subject: "Need this today"}}, labelSet, []Provider{provider}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !uncertain.Urgent || uncertain.Action != "none" || uncertain.Folder != "" || uncertain.Category != "work" {
		t.Fatalf("uncertain=%+v", uncertain)
	}
}
