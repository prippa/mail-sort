// Package store keeps confirmation, dry-run, apply, and undo state in SQLite.
// The classification cache lives in the same mailsorter.db file and is opened
// by internal/classify. Both openers create their own tables.
package store
