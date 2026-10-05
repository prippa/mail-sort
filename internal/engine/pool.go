package engine

import (
	"context"
	"errors"
	"sync"

	"github.com/prippa/mail-sort/internal/classify"
)

const maxClassifierErrors = 5

type outcome struct {
	decision classify.Decision
	err      error
}

// classifyOrdered runs a worker pool and stops after five consecutive errors.
// Messages after that point are left out of the result.
func classifyOrdered(ctx context.Context, inputs []classify.Input, clf Classifier, workers int) ([]outcome, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if len(inputs) == 0 {
		return nil, false, nil
	}
	if clf == nil {
		return nil, false, errors.New("engine: classifier is missing")
	}
	if workers < 1 {
		workers = 1
	}
	ctx, cancel := context.WithCancel(ctx)
	outcomes := make([]outcome, len(inputs))
	filled := make([]bool, len(inputs))
	type result struct {
		index int
		item  outcome
	}
	results := make(chan result, workers)
	var wg sync.WaitGroup
	finishOnce := sync.Once{}
	finish := func() {
		finishOnce.Do(func() {
			go func() {
				wg.Wait()
				close(results)
			}()
		})
		for range results {
		}
	}
	defer finish()
	defer cancel()

	next := 0
	take := 0
	inFlight := 0
	streak := 0
	aborted := false
	var fatal error

	submitMore := func() {
		if aborted || fatal != nil {
			return
		}
		ahead := workers
		if streak > 0 {
			ahead = maxClassifierErrors - streak
		}
		if ahead < 0 {
			ahead = 0
		}
		if ahead > workers {
			ahead = workers
		}
		for next < len(inputs) && next < take+ahead && inFlight < workers {
			i := next
			next++
			inFlight++
			wg.Add(1)
			go func() {
				defer wg.Done()
				decision, err := clf.Classify(ctx, inputs[i])
				results <- result{index: i, item: outcome{decision: decision, err: err}}
			}()
		}
	}

	for {
		submitMore()
		if inFlight == 0 {
			break
		}
		item := <-results
		inFlight--
		outcomes[item.index] = item.item
		filled[item.index] = true
		for take < len(inputs) && filled[take] {
			err := outcomes[take].err
			if err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
				fatal = err
				cancel()
				break
			}
			if err != nil {
				streak++
				if streak >= maxClassifierErrors {
					aborted = true
					cancel()
					take++
					break
				}
			} else {
				streak = 0
			}
			take++
		}
		if aborted || fatal != nil {
			break
		}
	}
	if fatal != nil {
		return nil, false, fatal
	}
	if aborted {
		return outcomes[:take], true, nil
	}
	return outcomes, false, nil
}
