package engine

import (
	"testing"

	"github.com/prippa/mail-sort/internal/store"
)

func TestSuggestThresholdNeedsLabels(t *testing.T) {
	t.Parallel()
	got := SuggestThreshold([]store.Example{{Predicted: "work", Category: "work", Confidence: 0.9}})
	if got.OK || got.Labeled != 1 {
		t.Fatalf("%+v", got)
	}
}

func TestSuggestThresholdPrefersACleanCutoff(t *testing.T) {
	t.Parallel()
	examples := make([]store.Example, 0, 8)
	for i := 0; i < 6; i++ {
		examples = append(examples, store.Example{Predicted: "work", Category: "work", Confidence: 0.9})
	}
	examples = append(examples,
		store.Example{Predicted: "work", Category: "personal", Confidence: 0.69},
		store.Example{Predicted: "work", Category: "work", Confidence: 0.7},
	)
	got := SuggestThreshold(examples)
	if !got.OK || got.Threshold != 0.70 || got.Wrong != 0 || got.Labeled != 8 {
		t.Fatalf("%+v", got)
	}
}

func TestSuggestThresholdReportsRemainingMistakes(t *testing.T) {
	t.Parallel()
	examples := make([]store.Example, 0, 8)
	for i := 0; i < 8; i++ {
		examples = append(examples, store.Example{Predicted: "work", Category: "personal", Confidence: 0.99})
	}
	got := SuggestThreshold(examples)
	if !got.OK || got.Threshold != 0.95 || got.Wrong != 8 {
		t.Fatalf("%+v", got)
	}
}
