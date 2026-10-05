package engine

import "errors"

var (
	// ErrNotConfirmed means apply was asked to file mail before a confirmation row exists.
	ErrNotConfirmed = errors.New("engine: profile is not confirmed")
	// ErrNoDryRun means confirm or override needs a dry run first.
	ErrNoDryRun = errors.New("engine: there is no dry run")
	// ErrNothingToApply means there is no open dry run.
	ErrNothingToApply = errors.New("engine: there is no dry run to apply")
	// ErrNothingToUndo means there is no filed run.
	ErrNothingToUndo = errors.New("engine: there is no applied run to undo")
	// ErrUnknownCategory means the override name is not in the category list.
	ErrUnknownCategory = errors.New("engine: unknown category")
	// ErrUnknownUID means that UID is not in the open dry run.
	ErrUnknownUID = errors.New("engine: that message is not in the dry run")
)
