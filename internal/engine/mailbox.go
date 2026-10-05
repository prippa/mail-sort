package engine

import (
	"context"

	"github.com/prippa/mail-sort/internal/classify"
	"github.com/prippa/mail-sort/internal/mail"
)

// Mailbox is the IMAP surface this package needs. IMAP types stay in internal/mail.
type Mailbox interface {
	Read(ctx context.Context, mailbox string, opt mail.ReadOptions) (mail.ReadBatch, error)
	UIDValidity(ctx context.Context, mailbox string) (uint32, error)
	FindUID(ctx context.Context, mailbox, messageID string) (uint32, bool, error)
	EnsureMailbox(ctx context.Context, name string) error
	MoveUID(ctx context.Context, mailbox string, uid uint32, messageID, dest string) (mail.Filed, error)
	CopyUID(ctx context.Context, mailbox string, uid uint32, messageID, dest string) (mail.Filed, error)
}

// Classifier classifies one message. The CLI adapts internal/classify.Classify.
type Classifier interface {
	Classify(ctx context.Context, in classify.Input) (classify.Decision, error)
}

var _ Mailbox = (*mail.Session)(nil)
