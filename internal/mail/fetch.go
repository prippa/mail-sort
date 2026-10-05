package mail

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/textproto"
	"sort"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/prippa/mail-sort/internal/message"
)

// Capabilities reports the tokens test-conn prints.
func (s *Session) Capabilities(ctx context.Context) (Caps, error) {
	if err := ctx.Err(); err != nil {
		return Caps{}, err
	}
	set := s.client.Caps()
	rev2 := set.Has(imap.CapIMAP4rev2)
	return Caps{
		Move:       rev2 || set.Has(imap.CapMove),
		UIDPlus:    rev2 || set.Has(imap.CapUIDPlus),
		Idle:       rev2 || set.Has(imap.CapIdle),
		SpecialUse: rev2 || set.Has(imap.CapSpecialUse),
		GmailExt:   set.Has("X-GM-EXT-1"),
	}, nil
}

// ListFolders returns every mailbox from LIST. SPECIAL-USE attributes are
// requested when the server advertises that capability.
func (s *Session) ListFolders(ctx context.Context) ([]Folder, error) {
	var folders []Folder
	err := s.do(ctx, func() error {
		var opts *imap.ListOptions
		if s.client.Caps().Has(imap.CapSpecialUse) || s.client.Caps().Has(imap.CapIMAP4rev2) {
			opts = &imap.ListOptions{ReturnSpecialUse: true}
		}
		data, err := s.client.List("", "*", opts).Collect()
		if err != nil {
			return fmt.Errorf("imap: list folders: %w", err)
		}
		folders = make([]Folder, 0, len(data))
		for _, item := range data {
			if item == nil || item.Mailbox == "" {
				continue
			}
			folders = append(folders, Folder{
				Name:       item.Mailbox,
				Delimiter:  item.Delim,
				SpecialUse: specialAttrs(item.Attrs),
			})
		}
		sort.Slice(folders, func(i, j int) bool { return folders[i].Name < folders[j].Name })
		return nil
	})
	return folders, err
}

// CreateMailbox creates a mailbox. The CLI does not call this.
func (s *Session) CreateMailbox(ctx context.Context, name string) error {
	return s.do(ctx, func() error {
		if err := s.client.Create(name, nil).Wait(); err != nil {
			return fmt.Errorf("imap: create mailbox: %w", err)
		}
		return nil
	})
}

// FetchNewest examines mailbox and returns up to limit messages, newest UID
// first. The mailbox is opened read-only and every body section uses BODY.PEEK,
// so the read flag is left unchanged.
func (s *Session) FetchNewest(ctx context.Context, mailbox string, limit, maxChars int) ([]message.Message, error) {
	if limit < 1 {
		return nil, fmt.Errorf("imap: limit must be at least 1")
	}
	var out []message.Message
	err := s.do(ctx, func() error {
		if _, err := s.client.Select(mailbox, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
			return fmt.Errorf("imap: examine %s: %w", mailbox, err)
		}
		found, err := s.client.UIDSearch(&imap.SearchCriteria{}, nil).Wait()
		if err != nil {
			return fmt.Errorf("imap: search %s: %w", mailbox, err)
		}
		uids := found.AllUIDs()
		if len(uids) == 0 {
			return nil
		}
		sort.Slice(uids, func(i, j int) bool { return uids[i] > uids[j] })
		if len(uids) > limit {
			uids = uids[:limit]
		}
		var set imap.UIDSet
		for _, uid := range uids {
			set.AddNum(uid)
		}
		bufs, err := s.client.Fetch(set, metaOptions()).Collect()
		if err != nil {
			return fmt.Errorf("imap: fetch %s: %w", mailbox, err)
		}
		out = make([]message.Message, 0, len(bufs))
		for _, buf := range bufs {
			if buf == nil {
				continue
			}
			msg, err := s.cleanMessage(buf, maxChars)
			if err != nil {
				return fmt.Errorf("imap: message uid %d: %w", buf.UID, err)
			}
			out = append(out, msg)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].UID > out[j].UID })
		return nil
	})
	return out, err
}

func (s *Session) cleanMessage(buf *imapclient.FetchMessageBuffer, maxChars int) (message.Message, error) {
	header, err := parseHeader(headerBytes(buf))
	if err != nil {
		return message.Message{}, err
	}
	choice, attachments := chooseBody(buf.BodyStructure)
	raw, err := fetchBody(s.client, buf.UID, choice)
	if err != nil {
		return message.Message{}, err
	}
	input := message.Input{
		UID:         uint32(buf.UID),
		Header:      header,
		Attachments: attachments,
		MediaType:   choice.Media,
		Charset:     choice.Charset,
		Encoding:    choice.Encoding,
		HTML:        choice.HTML,
		Body:        raw,
		MaxChars:    maxChars,
	}
	if env := buf.Envelope; env != nil {
		input.From = addresses(env.From)
		input.To = addresses(env.To)
		input.Cc = addresses(env.Cc)
		input.Subject = env.Subject
		input.Date = env.Date
		input.MessageID = env.MessageID
	}
	return message.Prepare(input)
}

func fetchBody(client *imapclient.Client, uid imap.UID, choice bodyChoice) ([]byte, error) {
	section := &imap.FetchItemBodySection{
		Peek:      true,
		Part:      choice.Part,
		Specifier: choice.Specifier,
		Partial:   &imap.SectionPartial{Offset: 0, Size: choice.Limit},
	}
	bufs, err := client.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{
		UID:         true,
		BodySection: []*imap.FetchItemBodySection{section},
	}).Collect()
	if err != nil {
		return nil, err
	}
	if len(bufs) == 0 || len(bufs[0].BodySection) == 0 {
		return nil, fmt.Errorf("body is missing")
	}
	return bufs[0].BodySection[0].Bytes, nil
}

func metaOptions() *imap.FetchOptions {
	return &imap.FetchOptions{
		UID:           true,
		Flags:         true,
		Envelope:      true,
		BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
		BodySection: []*imap.FetchItemBodySection{{
			Peek:      true,
			Specifier: imap.PartSpecifierHeader,
			HeaderFields: []string{
				"From", "To", "Cc", "Subject", "Date",
				"List-Id", "List-Unsubscribe", "Auto-Submitted", "Precedence",
			},
		}},
	}
}

func headerBytes(buf *imapclient.FetchMessageBuffer) []byte {
	for _, section := range buf.BodySection {
		if section.Section != nil && section.Section.Specifier == imap.PartSpecifierHeader {
			return section.Bytes
		}
	}
	return nil
}

func parseHeader(raw []byte) (map[string][]string, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	buf := append([]byte{}, raw...)
	if !bytes.HasSuffix(buf, []byte("\n")) {
		buf = append(buf, '\r', '\n')
	}
	buf = append(buf, '\r', '\n')
	header, err := textproto.NewReader(bufio.NewReader(bytes.NewReader(buf))).ReadMIMEHeader()
	if err != nil {
		return nil, fmt.Errorf("read headers: %w", err)
	}
	return map[string][]string(header), nil
}

func addresses(in []imap.Address) []message.Address {
	out := make([]message.Address, 0, len(in))
	for _, addr := range in {
		email := addr.Addr()
		if email == "" {
			continue
		}
		out = append(out, message.Address{Name: addr.Name, Email: email})
	}
	return out
}

func specialAttrs(attrs []imap.MailboxAttr) []string {
	var out []string
	for _, attr := range attrs {
		switch attr {
		case imap.MailboxAttrAll, imap.MailboxAttrArchive, imap.MailboxAttrDrafts,
			imap.MailboxAttrFlagged, imap.MailboxAttrJunk, imap.MailboxAttrSent,
			imap.MailboxAttrTrash, imap.MailboxAttrImportant:
			out = append(out, string(attr))
		}
	}
	return out
}
