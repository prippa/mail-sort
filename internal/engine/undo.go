package engine

import (
	"context"
	"errors"

	"github.com/prippa/mail-sort/internal/classify"
	"github.com/prippa/mail-sort/internal/store"
)

// UndoOptions selects a filed run and, optionally, some source UIDs.
type UndoOptions struct {
	Profile string
	RunID   int64
	UIDs    []uint32
}

// Undo moves filed messages back to the source mailbox.
// A copy is left in place: removing it would delete mail.
func Undo(ctx context.Context, box Mailbox, db *store.DB, opt UndoOptions) (Report, error) {
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	if opt.Profile == "" {
		return Report{}, errors.New("engine: profile is empty")
	}
	var (
		run  store.Run
		rows []store.Row
		err  error
	)
	if opt.RunID > 0 {
		run, rows, err = db.LoadRun(ctx, opt.RunID)
		if err == nil && run.Profile != opt.Profile {
			return Report{}, ErrNothingToUndo
		}
	} else {
		run, rows, err = db.LatestFiled(ctx, opt.Profile)
	}
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Report{}, ErrNothingToUndo
		}
		return Report{}, err
	}
	selected := uidSet(opt.UIDs)
	if len(selected) > 0 {
		have := make(map[uint32]struct{}, len(rows))
		for _, row := range rows {
			have[row.UID] = struct{}{}
		}
		for uid := range selected {
			if _, ok := have[uid]; !ok {
				return Report{}, ErrUnknownUID
			}
		}
	}
	if !hasApplied(rows, selected) {
		return Report{}, ErrNothingToUndo
	}
	saveCtx := context.WithoutCancel(ctx)
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			if _, finishErr := db.FinishUndo(saveCtx, run.ID); finishErr != nil {
				return Report{}, finishErr
			}
			return Report{}, err
		}
		if len(selected) > 0 {
			if _, ok := selected[row.UID]; !ok {
				continue
			}
		}
		if row.Status != store.RowApplied {
			continue
		}
		if row.Filed != store.FiledMove {
			detail := ""
			if row.Filed == store.FiledCopy {
				detail = "copy left in place"
			}
			if err := db.MarkUndone(saveCtx, row.ID, detail, 0); err != nil {
				return Report{}, err
			}
			if row.Filed == store.FiledCopy {
				continue
			}
			if err := db.ClearApplied(saveCtx, run.Profile, run.Mailbox, row.MessageID, run.UIDValidity, row.UID, row.FiledValidity, row.FiledUID); err != nil {
				return Report{}, err
			}
			continue
		}
		uid := row.DestUID
		if uid == 0 {
			if row.MessageID == "" {
				if err := db.SetRowDetail(saveCtx, row.ID, "undo has no destination uid"); err != nil {
					return Report{}, err
				}
				continue
			}
			found, ok, findErr := box.FindUID(ctx, row.Folder, row.MessageID)
			if findErr != nil || !ok {
				detail := "undo could not find the filed message"
				if findErr != nil {
					detail = findErr.Error()
				}
				if err := db.SetRowDetail(saveCtx, row.ID, detail); err != nil {
					return Report{}, err
				}
				continue
			}
			uid = found
		}
		filed, moveErr := box.MoveUID(ctx, row.Folder, uid, row.MessageID, run.Mailbox)
		if moveErr != nil {
			if err := db.SetRowDetail(saveCtx, row.ID, moveErr.Error()); err != nil {
				return Report{}, err
			}
			continue
		}
		if err := db.MarkUndone(saveCtx, row.ID, "moved back to "+run.Mailbox, filed.DestUID); err != nil {
			return Report{}, err
		}
		if err := db.ClearApplied(saveCtx, run.Profile, run.Mailbox, row.MessageID, run.UIDValidity, row.UID, row.FiledValidity, row.FiledUID); err != nil {
			return Report{}, err
		}
	}
	if _, err := db.FinishUndo(saveCtx, run.ID); err != nil {
		return Report{}, err
	}
	run, rows, err = db.LoadRun(saveCtx, run.ID)
	if err != nil {
		return Report{}, err
	}
	return reportOf(run, rows), nil
}

// Override changes one pending row on the open dry run.
func Override(ctx context.Context, db *store.DB, profile string, uid uint32, cats []classify.Category, category string) (store.Row, error) {
	if err := ctx.Err(); err != nil {
		return store.Row{}, err
	}
	cat, ok := categoryByKey(cats, category)
	if !ok {
		return store.Row{}, ErrUnknownCategory
	}
	row, err := db.UpdatePending(ctx, profile, uid, cat.Key, cat.Action, cat.Folder)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.Row{}, ErrNoDryRun
		}
		if errors.Is(err, store.ErrRowNotFound) {
			return store.Row{}, ErrUnknownUID
		}
		return store.Row{}, err
	}
	return row, nil
}

// Confirm stores the profile confirmation after a dry run exists.
func Confirm(ctx context.Context, db *store.DB, profile string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := db.Confirm(ctx, profile); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrNoDryRun
		}
		return err
	}
	return nil
}

func categoryByKey(cats []classify.Category, key string) (classify.Category, bool) {
	if key == classify.NeverTouch {
		return classify.Category{Key: classify.NeverTouch, Action: classify.NeverTouch}, true
	}
	for _, cat := range cats {
		if cat.Key == key {
			return cat, true
		}
	}
	return classify.Category{}, false
}

func hasApplied(rows []store.Row, selected map[uint32]struct{}) bool {
	for _, row := range rows {
		if len(selected) > 0 {
			if _, ok := selected[row.UID]; !ok {
				continue
			}
		}
		if row.Status == store.RowApplied {
			return true
		}
	}
	return false
}

func uidSet(uids []uint32) map[uint32]struct{} {
	if len(uids) == 0 {
		return nil
	}
	out := make(map[uint32]struct{}, len(uids))
	for _, uid := range uids {
		out[uid] = struct{}{}
	}
	return out
}
