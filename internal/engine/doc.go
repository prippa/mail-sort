// Package engine will plan, apply, undo, and watch mailbox changes.
// The first run of a profile is a dry run. Apply never deletes mail and never
// changes \Seen. Phase 0 has no engine yet.
package engine
