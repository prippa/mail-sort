package mail

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/emersion/go-imap/v2"
)

// ErrMoveUnavailable means the server advertises neither MOVE nor UIDPLUS.
// Copy-only is still possible. Nothing was changed.
var ErrMoveUnavailable = errors.New("imap: server cannot move mail; copy-only is available")

// Filed is the destination UID recorded for undo.
// A zero DestUID means the server did not return one and Message-ID search missed.
type Filed struct {
	DestUID uint32
}

// ExpungeFailed means the message was copied and UID EXPUNGE of that UID failed.
// DestUID is the copy. The source is still in the mailbox.
type ExpungeFailed struct {
	DestUID uint32
	Err     error
}

func (e *ExpungeFailed) Error() string {
	return fmt.Sprintf("imap: copied message to uid %d and could not remove the source: %v", e.DestUID, e.Err)
}

func (e *ExpungeFailed) Unwrap() error { return e.Err }

// EnsureMailbox creates a missing mailbox and subscribes to it.
// Trash, Junk, Drafts, and Sent are refused.
func (s *Session) EnsureMailbox(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("imap: mailbox is empty")
	}
	if protectedMailboxName(name) {
		return fmt.Errorf("imap: refusing to create %q", name)
	}
	folders, err := s.ListFolders(ctx)
	if err != nil {
		return err
	}
	exists := false
	for _, folder := range folders {
		if folder.Name == name {
			exists = true
			break
		}
	}
	if !exists {
		if err := s.CreateMailbox(ctx, name); err != nil {
			again, listErr := s.ListFolders(ctx)
			if listErr != nil {
				return err
			}
			found := false
			for _, folder := range again {
				if folder.Name == name {
					found = true
					break
				}
			}
			if !found {
				return err
			}
		}
	}
	return s.do(ctx, func() error {
		if err := s.client.Subscribe(name).Wait(); err != nil {
			return fmt.Errorf("imap: subscribe %s: %w", name, err)
		}
		return nil
	})
}

// MoveUID moves one message.
// MOVE is used only when the server advertises MOVE or IMAP4rev2.
// imapclient.Client.Move falls back to COPY, STORE, and EXPUNGE when MOVE is
// absent, and that EXPUNGE is not limited to one UID, so it is not used here.
// Without MOVE, UIDPLUS copies the message, marks that UID \Deleted, and UID EXPUNGEs it.
// Without either capability the mailbox is left unchanged.
func (s *Session) MoveUID(ctx context.Context, mailbox string, uid uint32, messageID, dest string) (Filed, error) {
	if err := ctx.Err(); err != nil {
		return Filed{}, err
	}
	if uid == 0 || strings.TrimSpace(mailbox) == "" || strings.TrimSpace(dest) == "" {
		return Filed{}, errors.New("imap: mailbox or uid is empty")
	}
	caps, err := s.Capabilities(ctx)
	if err != nil {
		return Filed{}, err
	}
	if !caps.Move && !caps.UIDPlus {
		return Filed{}, ErrMoveUnavailable
	}
	var destUID uint32
	copied := false
	err = s.do(ctx, func() error {
		// Read-write select does not set \Seen. No flag is stored.
		if _, err := s.client.Select(mailbox, nil).Wait(); err != nil {
			return fmt.Errorf("imap: select %s: %w", mailbox, err)
		}
		set := imap.UIDSetNum(imap.UID(uid))
		if caps.Move {
			data, err := s.client.Move(set, dest).Wait()
			if err != nil {
				return fmt.Errorf("imap: move: %w", err)
			}
			if data != nil {
				if parsed, ok := onlyUID(data.DestUIDs); ok {
					destUID = uint32(parsed)
				}
			}
			return nil
		}
		data, err := s.client.Copy(set, dest).Wait()
		if err != nil {
			return fmt.Errorf("imap: copy: %w", err)
		}
		copied = true
		if data != nil {
			if parsed, ok := oneUID(data.DestUIDs); ok {
				destUID = uint32(parsed)
			}
		}
		// UID EXPUNGE removes only matching messages that already have \Deleted.
		if _, err := s.client.Store(set, &imap.StoreFlags{
			Op:     imap.StoreFlagsAdd,
			Silent: true,
			Flags:  []imap.Flag{imap.FlagDeleted},
		}, nil).Collect(); err != nil {
			return err
		}
		seqs, err := s.client.UIDExpunge(set).Collect()
		if err != nil {
			return err
		}
		if len(seqs) == 0 {
			return errors.New("imap: uid expunge removed nothing")
		}
		return nil
	})
	if err != nil && copied {
		return Filed{DestUID: destUID}, &ExpungeFailed{DestUID: destUID, Err: err}
	}
	if err != nil {
		return Filed{}, err
	}
	if destUID == 0 && strings.TrimSpace(messageID) != "" {
		found, ok, findErr := s.FindUID(ctx, dest, messageID)
		if findErr != nil {
			return Filed{}, findErr
		}
		if ok {
			destUID = found
		}
	}
	return Filed{DestUID: destUID}, nil
}

// CopyUID copies one message and leaves the source where it is.
func (s *Session) CopyUID(ctx context.Context, mailbox string, uid uint32, messageID, dest string) (Filed, error) {
	if err := ctx.Err(); err != nil {
		return Filed{}, err
	}
	if uid == 0 || strings.TrimSpace(mailbox) == "" || strings.TrimSpace(dest) == "" {
		return Filed{}, errors.New("imap: mailbox or uid is empty")
	}
	var destUID uint32
	err := s.do(ctx, func() error {
		if _, err := s.client.Select(mailbox, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
			return fmt.Errorf("imap: examine %s: %w", mailbox, err)
		}
		data, err := s.client.Copy(imap.UIDSetNum(imap.UID(uid)), dest).Wait()
		if err != nil {
			return fmt.Errorf("imap: copy: %w", err)
		}
		if data != nil {
			if parsed, ok := oneUID(data.DestUIDs); ok {
				destUID = uint32(parsed)
			}
		}
		return nil
	})
	if err != nil {
		return Filed{}, err
	}
	if destUID == 0 && strings.TrimSpace(messageID) != "" {
		found, ok, findErr := s.FindUID(ctx, dest, messageID)
		if findErr != nil {
			return Filed{}, findErr
		}
		if ok {
			destUID = found
		}
	}
	return Filed{DestUID: destUID}, nil
}

// onlyUID reads a MOVE destination set.
// VERIFY: a dynamic COPYUID has no numeric UID. Callers then search by Message-ID.
func onlyUID(set imap.NumSet) (imap.UID, bool) {
	if set == nil {
		return 0, false
	}
	switch value := set.(type) {
	case imap.UIDSet:
		return oneUID(value)
	case *imap.UIDSet:
		if value == nil {
			return 0, false
		}
		return oneUID(*value)
	default:
		return 0, false
	}
}

func oneUID(set imap.UIDSet) (imap.UID, bool) {
	nums, ok := set.Nums()
	if !ok || len(nums) != 1 {
		return 0, false
	}
	return nums[0], true
}
