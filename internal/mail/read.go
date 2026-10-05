package mail

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/prippa/mail-sort/internal/message"
)

// ReadOptions selects which messages a dry run reads.
type ReadOptions struct {
	UnreadOnly     bool
	Since          time.Time
	Limit          int
	SkipFlagged    bool
	SkipDrafts     bool
	MaxChars       int
	AllowProtected bool
}

// ReadMessage is one examined message. Headers are kept for rules.
type ReadMessage struct {
	Message message.Message
	Headers map[string][]string
	Seen    bool
	Flagged bool
	Draft   bool
}

// ReadBatch is a read-only look at one mailbox.
type ReadBatch struct {
	UIDValidity uint32
	Messages    []ReadMessage
}

// Read examines mailbox and returns up to Limit messages, newest UID first.
// The mailbox is opened read-only and every body section uses BODY.PEEK.
func (s *Session) Read(ctx context.Context, mailbox string, opt ReadOptions) (ReadBatch, error) {
	if err := ctx.Err(); err != nil {
		return ReadBatch{}, err
	}
	if opt.Limit < 1 {
		return ReadBatch{}, errors.New("imap: limit must be at least 1")
	}
	if err := s.rejectProtected(ctx, mailbox, opt.AllowProtected); err != nil {
		return ReadBatch{}, err
	}
	var batch ReadBatch
	var uids []imap.UID
	err := s.do(ctx, func() error {
		data, err := s.client.Select(mailbox, &imap.SelectOptions{ReadOnly: true}).Wait()
		if err != nil {
			return fmt.Errorf("imap: examine %s: %w", mailbox, err)
		}
		if data == nil {
			return fmt.Errorf("imap: examine %s: empty result", mailbox)
		}
		batch.UIDValidity = data.UIDValidity
		found, err := s.client.UIDSearch(searchCriteria(opt), nil).Wait()
		if err != nil {
			return fmt.Errorf("imap: search %s: %w", mailbox, err)
		}
		if found != nil {
			uids = found.AllUIDs()
		}
		return nil
	})
	if err != nil {
		return ReadBatch{}, err
	}
	sort.Slice(uids, func(i, j int) bool { return uids[i] > uids[j] })
	if len(uids) > opt.Limit {
		uids = uids[:opt.Limit]
	}
	batch.Messages = make([]ReadMessage, 0, len(uids))
	for _, uid := range uids {
		if err := ctx.Err(); err != nil {
			return ReadBatch{}, err
		}
		msg, err := s.readOne(ctx, uid, opt.MaxChars)
		if err != nil {
			return ReadBatch{}, fmt.Errorf("imap: message uid %d: %w", uid, err)
		}
		if opt.SkipFlagged && msg.Flagged {
			continue
		}
		if opt.SkipDrafts && msg.Draft {
			continue
		}
		if opt.UnreadOnly && msg.Seen {
			continue
		}
		batch.Messages = append(batch.Messages, msg)
	}
	return batch, nil
}

// UIDValidity examines mailbox and returns its UIDVALIDITY.
func (s *Session) UIDValidity(ctx context.Context, mailbox string) (uint32, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var validity uint32
	err := s.do(ctx, func() error {
		data, err := s.client.Select(mailbox, &imap.SelectOptions{ReadOnly: true}).Wait()
		if err != nil {
			return fmt.Errorf("imap: examine %s: %w", mailbox, err)
		}
		if data == nil {
			return fmt.Errorf("imap: examine %s: empty result", mailbox)
		}
		validity = data.UIDValidity
		return nil
	})
	return validity, err
}

// FindUID searches mailbox for a Message-ID. The search is a substring match.
// VERIFY: servers differ on whether the angle brackets are part of the match.
func (s *Session) FindUID(ctx context.Context, mailbox, messageID string) (uint32, bool, error) {
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return 0, false, nil
	}
	var found []imap.UID
	err := s.do(ctx, func() error {
		if _, err := s.client.Select(mailbox, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
			return fmt.Errorf("imap: examine %s: %w", mailbox, err)
		}
		data, err := s.client.UIDSearch(&imap.SearchCriteria{
			Header: []imap.SearchCriteriaHeaderField{{Key: "Message-ID", Value: messageID}},
		}, nil).Wait()
		if err != nil {
			return fmt.Errorf("imap: search %s: %w", mailbox, err)
		}
		if data != nil {
			found = data.AllUIDs()
		}
		return nil
	})
	if err != nil {
		return 0, false, err
	}
	if len(found) == 0 {
		return 0, false, nil
	}
	best := found[0]
	for _, uid := range found[1:] {
		if uid > best {
			best = uid
		}
	}
	return uint32(best), true, nil
}

func (s *Session) readOne(ctx context.Context, uid imap.UID, maxChars int) (ReadMessage, error) {
	var msg ReadMessage
	err := s.do(ctx, func() error {
		bufs, err := s.client.Fetch(imap.UIDSetNum(uid), metaOptions()).Collect()
		if err != nil {
			return err
		}
		if len(bufs) == 0 || bufs[0] == nil {
			return errors.New("message is missing")
		}
		header, err := parseHeader(headerBytes(bufs[0]))
		if err != nil {
			return err
		}
		prepared, err := s.cleanMessage(bufs[0], maxChars)
		if err != nil {
			return err
		}
		msg.Message = prepared
		msg.Headers = header
		if msg.Headers == nil {
			msg.Headers = map[string][]string{}
		}
		msg.Seen, msg.Flagged, msg.Draft = flagBits(bufs[0].Flags)
		return nil
	})
	return msg, err
}

func (s *Session) rejectProtected(ctx context.Context, mailbox string, allow bool) error {
	if allow {
		return nil
	}
	if protectedMailboxName(mailbox) {
		return fmt.Errorf("imap: refusing to open %q", mailbox)
	}
	folders, err := s.ListFolders(ctx)
	if err != nil {
		return err
	}
	for _, folder := range folders {
		if folder.Name != mailbox {
			continue
		}
		for _, use := range folder.SpecialUse {
			if protectedSpecialUse(use) {
				return fmt.Errorf("imap: refusing to open %q (%s)", mailbox, use)
			}
		}
	}
	return nil
}

func searchCriteria(opt ReadOptions) *imap.SearchCriteria {
	criteria := &imap.SearchCriteria{}
	if opt.UnreadOnly {
		criteria.NotFlag = append(criteria.NotFlag, imap.FlagSeen)
	}
	if opt.SkipFlagged {
		criteria.NotFlag = append(criteria.NotFlag, imap.FlagFlagged)
	}
	if opt.SkipDrafts {
		criteria.NotFlag = append(criteria.NotFlag, imap.FlagDraft)
	}
	if !opt.Since.IsZero() {
		year, month, day := opt.Since.Date()
		criteria.Since = time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	}
	return criteria
}

func flagBits(flags []imap.Flag) (seen, flagged, draft bool) {
	for _, flag := range flags {
		switch flag {
		case imap.FlagSeen:
			seen = true
		case imap.FlagFlagged:
			flagged = true
		case imap.FlagDraft:
			draft = true
		}
	}
	return seen, flagged, draft
}

func protectedMailboxName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "trash", "junk", "spam", "drafts", "sent", "sent messages", "sent items",
		"deleted", "deleted items", "deleted messages", "bin":
		return true
	default:
		return false
	}
}

func protectedSpecialUse(use string) bool {
	switch strings.ToLower(use) {
	case `\trash`, `\junk`, `\drafts`, `\sent`:
		return true
	default:
		return false
	}
}
