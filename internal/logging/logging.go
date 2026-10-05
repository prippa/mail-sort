// Package logging writes JSON logs to a rotating file and redacts secrets.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/prippa/mail-sort/internal/secrets"
)

const (
	// DefaultMaxBytes is the size of the active log before it rotates.
	DefaultMaxBytes int64 = 5 << 20
	// DefaultMaxFiles is the active log plus rotated files.
	DefaultMaxFiles = 5
	redacted        = "[redacted]"
)

// Options controls redaction and rotation. Zero values use the defaults.
// LogSubjects stays off unless the user opts in.
type Options struct {
	LogSubjects bool
	MaxBytes    int64
	MaxFiles    int
}

func (o Options) withDefaults() Options {
	if o.MaxBytes <= 0 {
		o.MaxBytes = DefaultMaxBytes
	}
	if o.MaxFiles < 2 {
		o.MaxFiles = DefaultMaxFiles
	}
	return o
}

// Open creates or appends to the log file at path. The returned closer
// flushes rotation state. Mode 0600 because a later opt-in may store subjects.
func Open(path string, opts Options) (*slog.Logger, io.Closer, error) {
	opts = opts.withDefaults()
	rot, err := openRotator(path, opts.MaxBytes, opts.MaxFiles)
	if err != nil {
		return nil, nil, err
	}
	logger := slog.New(newRedactingHandler(slog.NewJSONHandler(rot, nil), opts))
	return logger, rot, nil
}

type redactingHandler struct {
	next slog.Handler
	opts Options
}

func newRedactingHandler(next slog.Handler, opts Options) slog.Handler {
	return redactingHandler{next: next, opts: opts}
}

func (h redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h redactingHandler) Handle(ctx context.Context, record slog.Record) error {
	cleaned := slog.NewRecord(record.Time, record.Level, record.Message, record.PC)
	record.Attrs(func(attr slog.Attr) bool {
		cleaned.AddAttrs(redactAttr(attr, h.opts))
		return true
	})
	return h.next.Handle(ctx, cleaned)
}

func (h redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	redactedAttrs := make([]slog.Attr, len(attrs))
	for i, attr := range attrs {
		redactedAttrs[i] = redactAttr(attr, h.opts)
	}
	return redactingHandler{next: h.next.WithAttrs(redactedAttrs), opts: h.opts}
}

func (h redactingHandler) WithGroup(name string) slog.Handler {
	return redactingHandler{next: h.next.WithGroup(name), opts: h.opts}
}

func redactAttr(attr slog.Attr, opts Options) slog.Attr {
	attr.Value = attr.Value.Resolve()
	if attr.Value.Kind() == slog.KindGroup {
		group := attr.Value.Group()
		redactedGroup := make([]slog.Attr, len(group))
		for i, child := range group {
			redactedGroup[i] = redactAttr(child, opts)
		}
		attr.Value = slog.GroupValue(redactedGroup...)
		return attr
	}
	if secrets.IsSensitiveLogKey(attr.Key, opts.LogSubjects) {
		attr.Value = slog.StringValue(redacted)
	}
	return attr
}

// EnsureDir creates the state directory used for logs. 0700 keeps the
// directory from being listed by other users on a shared machine.
func EnsureDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("log: create dir: %w", err)
	}
	return nil
}
