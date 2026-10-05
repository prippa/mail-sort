package engine

import (
	"context"
	"errors"

	"github.com/prippa/mail-sort/internal/store"
)

// Next files mail that is not already filed, or continues a partial apply.
// A dry run left by `run` is left untouched. An empty mailbox does not create
// a run. Next does not move mail when the profile is not confirmed.
func Next(ctx context.Context, box Mailbox, clf Classifier, db *store.DB, plan PlanOptions, apply ApplyOptions) (Report, bool, error) {
	if err := ctx.Err(); err != nil {
		return Report{}, false, err
	}
	if plan.Profile == "" {
		return Report{}, false, errors.New("engine: profile is empty")
	}
	if plan.Mailbox == "" {
		plan.Mailbox = "INBOX"
	}
	if plan.Read.Limit < 1 {
		plan.Read.Limit = DefaultLimit
	}
	if plan.Workers < 1 {
		plan.Workers = DefaultWorkers
	}
	if apply.Profile == "" {
		apply.Profile = plan.Profile
	}
	if apply.MaxMoves < 1 {
		apply.MaxMoves = DefaultMaxMoves
	}
	ok, err := db.Confirmed(ctx, plan.Profile)
	if err != nil {
		return Report{}, false, err
	}
	if !ok {
		return Report{}, false, ErrNotConfirmed
	}
	open, _, err := db.LatestOpen(ctx, plan.Profile)
	if err == nil {
		if open.Status == store.StatusDry {
			return Report{}, false, ErrOpenPlan
		}
		report, err := Apply(ctx, box, db, apply)
		return report, true, err
	}
	if !errors.Is(err, store.ErrNotFound) {
		return Report{}, false, err
	}
	waiting, err := unfiled(ctx, box, db, plan)
	if err != nil {
		return Report{}, false, err
	}
	if waiting == 0 {
		return Report{}, false, nil
	}
	report, err := Plan(ctx, box, clf, db, plan)
	if err != nil {
		return Report{}, false, err
	}
	if len(report.Rows) == 0 {
		if _, err := db.FinishRun(ctx, report.Run.ID); err != nil {
			return Report{}, false, err
		}
		return report, false, nil
	}
	aborted := report.Aborted
	applied, err := Apply(ctx, box, db, apply)
	if err != nil {
		return Report{}, false, err
	}
	applied.Aborted = aborted
	return applied, true, nil
}

func unfiled(ctx context.Context, box Mailbox, db *store.DB, plan PlanOptions) (int, error) {
	batch, err := box.Read(ctx, plan.Mailbox, plan.Read)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, msg := range batch.Messages {
		applied, err := db.Applied(ctx, plan.Profile, plan.Mailbox, batch.UIDValidity, msg.Message.UID, msg.Message.MessageID)
		if err != nil {
			return 0, err
		}
		if !applied {
			n++
		}
	}
	return n, nil
}
