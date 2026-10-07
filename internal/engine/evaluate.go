package engine

import (
	"context"
	"errors"
	"math"

	"github.com/prippa/mail-sort/internal/classify"
	"github.com/prippa/mail-sort/internal/store"
)

// MinLabels is how many labeled messages a suggestion needs.
// Fewer than this and the screen says there is not enough of the user's own data.
const MinLabels = 8

// Suggestion is a confidence cutoff swept from the user's labels.
// It is not a calibrated score.
type Suggestion struct {
	OK        bool
	Threshold float64
	Labeled   int
	Auto      int
	Wrong     int
}

// SuggestThreshold sweeps 0.50 through 0.95.
// It prefers the lowest cutoff that files at least one labeled message and
// misfiles none. If every cutoff still misfiles something, it returns the
// cutoff with the fewest mistakes, and the higher cutoff when those tie.
func SuggestThreshold(examples []store.Example) Suggestion {
	labeled := make([]store.Example, 0, len(examples))
	for _, item := range examples {
		if item.Predicted == "" || item.Category == "" {
			continue
		}
		if math.IsNaN(item.Confidence) || item.Confidence < 0 || item.Confidence > 1 {
			continue
		}
		labeled = append(labeled, item)
	}
	out := Suggestion{Labeled: len(labeled)}
	if len(labeled) < MinLabels {
		return out
	}
	var cautious Suggestion
	found := false
	for step := 50; step <= 95; step += 5 {
		threshold := float64(step) / 100
		auto, wrong := countAt(labeled, threshold)
		if auto == 0 {
			continue
		}
		if wrong == 0 {
			return Suggestion{OK: true, Threshold: threshold, Labeled: len(labeled), Auto: auto}
		}
		if !found || wrong < cautious.Wrong || (wrong == cautious.Wrong && threshold > cautious.Threshold) {
			found = true
			cautious = Suggestion{OK: true, Threshold: threshold, Labeled: len(labeled), Auto: auto, Wrong: wrong}
		}
	}
	if !found {
		return out
	}
	return cautious
}

func countAt(examples []store.Example, threshold float64) (int, int) {
	auto, wrong := 0, 0
	for _, item := range examples {
		if item.Confidence < threshold {
			continue
		}
		auto++
		if item.Predicted != item.Category {
			wrong++
		}
	}
	return auto, wrong
}

// Label records the user's category. A pending row keeps the action that
// apply will use. An already filed row only stores the label.
func Label(ctx context.Context, db *store.DB, profile string, uid uint32, cats []classify.Category, category string) (store.Row, error) {
	row, err := Override(ctx, db, profile, uid, cats, category)
	if err == nil {
		return row, nil
	}
	if !errors.Is(err, store.ErrNotPending) && !errors.Is(err, ErrNoDryRun) && !errors.Is(err, ErrUnknownUID) {
		return store.Row{}, err
	}
	cat, ok := categoryByKey(cats, category)
	if !ok {
		return store.Row{}, ErrUnknownCategory
	}
	labeled, labelErr := db.SetLabel(ctx, profile, uid, cat.Key)
	if labelErr != nil {
		if errors.Is(labelErr, store.ErrNotFound) {
			return store.Row{}, ErrUnknownUID
		}
		return store.Row{}, labelErr
	}
	return labeled, nil
}
