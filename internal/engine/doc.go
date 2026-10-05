// Package engine plans a dry run, applies it after confirmation, and undoes
// a filed run. Confirmation lives in SQLite. The first plan does not move mail.
// Apply never deletes mail and never changes the read flag.
package engine
