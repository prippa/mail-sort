// Package engine plans a dry run, applies it after confirmation, undoes a
// filed run, and files mail that arrives later. Confirmation lives in SQLite.
// The first plan does not move mail. Apply never deletes mail and never
// changes the read flag. A high Jev urgency answer keeps a move in the inbox.
// Labeled rows can suggest a confidence cutoff.
package engine
