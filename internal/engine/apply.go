package engine

import (
	"context"
	"errors"

	"github.com/prippa/mail-sort/internal/classify"
	"github.com/prippa/mail-sort/internal/mail"
	"github.com/prippa/mail-sort/internal/store"
)

// ApplyOptions files the latest open dry run.
type ApplyOptions struct {
	Profile  string
	CopyOnly bool
	MaxMoves int
}

// Apply files the stored selection. It does not classify again.
// Mail is moved only when the profile has a confirmation row.
func Apply(ctx context.Context, box Mailbox, db *store.DB, opt ApplyOptions) (Report, error) {
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	if opt.Profile == "" {
		return Report{}, errors.New("engine: profile is empty")
	}
	if opt.MaxMoves < 1 {
		opt.MaxMoves = DefaultMaxMoves
	}
	ok, err := db.Confirmed(ctx, opt.Profile)
	if err != nil {
		return Report{}, err
	}
	if !ok {
		return Report{}, ErrNotConfirmed
	}
	run, rows, err := db.LatestOpen(ctx, opt.Profile)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Report{}, ErrNothingToApply
		}
		return Report{}, err
	}
	validity, err := box.UIDValidity(ctx, run.Mailbox)
	if err != nil {
		return Report{}, err
	}
	saveCtx := context.WithoutCancel(ctx)
	filed := 0
	var refused bool
	var moves, copies int
	ensured := map[string]error{}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			if _, finishErr := db.FinishRun(saveCtx, run.ID); finishErr != nil {
				return Report{}, finishErr
			}
			return Report{}, err
		}
		if row.Status != store.RowPending {
			continue
		}
		switch {
		case noop(row):
			uid, resolveErr := resolveUID(ctx, box, run, row, validity)
			if resolveErr != nil {
				if err := remember(saveCtx, db, run, row, store.RowError, "", 0, 0, 0, resolveErr.Error()); err != nil {
					return Report{}, err
				}
				continue
			}
			if err := remember(saveCtx, db, run, row, store.RowApplied, store.FiledNone, 0, uid, validity, ""); err != nil {
				return Report{}, err
			}
		case row.Action == "move" || row.Action == "label":
			if filed >= opt.MaxMoves {
				continue
			}
			filed++
			if row.Folder == "" || row.Folder == run.Mailbox {
				if err := remember(saveCtx, db, run, row, store.RowError, "", 0, 0, 0, "message has no destination folder"); err != nil {
					return Report{}, err
				}
				continue
			}
			if err := ensure(ctx, box, ensured, row.Folder); err != nil {
				if err := remember(saveCtx, db, run, row, store.RowError, "", 0, 0, 0, err.Error()); err != nil {
					return Report{}, err
				}
				continue
			}
			uid, resolveErr := resolveUID(ctx, box, run, row, validity)
			if resolveErr != nil {
				if err := remember(saveCtx, db, run, row, store.RowError, "", 0, 0, 0, resolveErr.Error()); err != nil {
					return Report{}, err
				}
				continue
			}
			copied, dest, fileErr := fileOne(ctx, box, run.Mailbox, uid, row, opt.CopyOnly)
			if errors.Is(fileErr, mail.ErrMoveUnavailable) {
				refused = true
				if err := remember(saveCtx, db, run, row, store.RowError, "", 0, 0, 0, fileErr.Error()); err != nil {
					return Report{}, err
				}
				continue
			}
			var expunge *mail.ExpungeFailed
			if errors.As(fileErr, &expunge) {
				if err := remember(saveCtx, db, run, row, store.RowError, store.FiledCopy, expunge.DestUID, uid, validity, expunge.Error()); err != nil {
					return Report{}, err
				}
				copies++
				continue
			}
			if fileErr != nil {
				if err := remember(saveCtx, db, run, row, store.RowError, "", 0, 0, 0, fileErr.Error()); err != nil {
					return Report{}, err
				}
				continue
			}
			kind := store.FiledMove
			if copied {
				kind = store.FiledCopy
				copies++
			} else {
				moves++
			}
			if err := remember(saveCtx, db, run, row, store.RowApplied, kind, dest, uid, validity, ""); err != nil {
				return Report{}, err
			}
		default:
			if err := remember(saveCtx, db, run, row, store.RowError, "", 0, 0, 0, "message has no action"); err != nil {
				return Report{}, err
			}
		}
	}
	if _, err := db.FinishRun(saveCtx, run.ID); err != nil {
		return Report{}, err
	}
	run, rows, err = db.LoadRun(saveCtx, run.ID)
	if err != nil {
		return Report{}, err
	}
	report := reportOf(run, rows)
	report.CopyOnlyRefused = refused
	report.Moves = moves
	report.Copies = copies
	return report, nil
}

func noop(row store.Row) bool {
	return row.Category == classify.NeverTouch || row.Action == classify.NeverTouch || row.Action == "none"
}

func ensure(ctx context.Context, box Mailbox, cache map[string]error, folder string) error {
	if err, ok := cache[folder]; ok {
		return err
	}
	err := box.EnsureMailbox(ctx, folder)
	cache[folder] = err
	return err
}

func resolveUID(ctx context.Context, box Mailbox, run store.Run, row store.Row, validity uint32) (uint32, error) {
	if validity == run.UIDValidity {
		return row.UID, nil
	}
	if row.MessageID == "" {
		return 0, errors.New("engine: uidvalidity changed and the message has no message-id")
	}
	found, ok, err := box.FindUID(ctx, run.Mailbox, row.MessageID)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, errors.New("engine: message was not found after uidvalidity changed")
	}
	return found, nil
}

func fileOne(ctx context.Context, box Mailbox, mailbox string, uid uint32, row store.Row, copyOnly bool) (bool, uint32, error) {
	if row.Action == "label" || copyOnly {
		filed, err := box.CopyUID(ctx, mailbox, uid, row.MessageID, row.Folder)
		return true, filed.DestUID, err
	}
	filed, err := box.MoveUID(ctx, mailbox, uid, row.MessageID, row.Folder)
	return false, filed.DestUID, err
}

func remember(ctx context.Context, db *store.DB, run store.Run, row store.Row, status, filed string, dest, filedUID, validity uint32, detail string) error {
	rememberRow := status == store.RowApplied || (status == store.RowError && filed == store.FiledCopy)
	return db.CommitRow(ctx, store.RowUpdate{
		ID:            row.ID,
		Status:        status,
		DestUID:       dest,
		Detail:        detail,
		Filed:         filed,
		FiledUID:      filedUID,
		FiledValidity: validity,
		Remember:      rememberRow,
		Profile:       run.Profile,
		Mailbox:       run.Mailbox,
		UID:           row.UID,
		MessageID:     row.MessageID,
		RunID:         run.ID,
	})
}
