package mail

import (
	"context"
	"crypto/x509"
	"errors"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

func TestReadFiltersAndRefusesTrash(t *testing.T) {
	session, raw, _ := newSession(t, fullCaps())
	appendRaw(t, raw, "INBOX", textMessage("plain@example.com", "Plain", "hello"))
	appendFlags(t, raw, "INBOX", textMessage("seen@example.com", "Seen", "old"), []imap.Flag{imap.FlagSeen})
	appendFlags(t, raw, "INBOX", textMessage("flagged@example.com", "Flagged", "star"), []imap.Flag{imap.FlagFlagged})
	appendFlags(t, raw, "INBOX", textMessage("draft@example.com", "Draft", "wip"), []imap.Flag{imap.FlagDraft})
	if err := session.CreateMailbox(context.Background(), "Trash"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := session.Read(ctx, "Trash", ReadOptions{Limit: 10}); err == nil {
		t.Fatal("Trash was opened without an explicit selection")
	}
	batch, err := session.Read(ctx, "INBOX", ReadOptions{
		Limit:       10,
		UnreadOnly:  true,
		SkipFlagged: true,
		SkipDrafts:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Messages) != 1 || batch.Messages[0].Message.Subject != "Plain" {
		t.Fatalf("messages = %+v", batch.Messages)
	}
	if batch.UIDValidity == 0 {
		t.Fatal("uidvalidity was empty")
	}
	if _, err := raw.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		t.Fatal(err)
	}
	if seen(t, raw, imap.UID(batch.Messages[0].Message.UID)) {
		t.Fatal("read set \\Seen")
	}
	future, err := session.Read(ctx, "INBOX", ReadOptions{Limit: 10, Since: time.Now().Add(48 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if len(future.Messages) != 0 {
		t.Fatalf("future messages = %d", len(future.Messages))
	}
	allowed, err := session.Read(ctx, "Trash", ReadOptions{Limit: 10, AllowProtected: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(allowed.Messages) != 0 {
		t.Fatalf("trash messages = %d", len(allowed.Messages))
	}
}

func TestMoveLeavesOtherMailAndSeenUnset(t *testing.T) {
	for _, tc := range []struct {
		name string
		caps imap.CapSet
	}{
		{name: "move", caps: imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapMove: {}, imap.CapUIDPlus: {}}},
		{name: "uid expunge", caps: imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapUIDPlus: {}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session, raw, _ := newSession(t, tc.caps)
			ctx := context.Background()
			keep := appendRaw(t, raw, "INBOX", textMessage("keep@example.com", "Keep", "stay"))
			deleted := appendFlags(t, raw, "INBOX", textMessage("deleted@example.com", "Deleted", "gone flag"), []imap.Flag{imap.FlagDeleted})
			moved := appendRaw(t, raw, "INBOX", textMessage("moved@example.com", "Moved", "file me"))
			if err := session.EnsureMailbox(ctx, "Needs review"); err != nil {
				t.Fatal(err)
			}
			filed, err := session.MoveUID(ctx, "INBOX", uint32(moved), "<moved@example.com>", "Needs review")
			if err != nil {
				t.Fatal(err)
			}
			if filed.DestUID == 0 {
				t.Fatal("destination uid was empty")
			}
			if _, err := raw.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
				t.Fatal(err)
			}
			left := allUIDs(t, raw)
			if !containsUID(left, keep) || !containsUID(left, deleted) || containsUID(left, moved) {
				t.Fatalf("inbox uids after move = %v", left)
			}
			if seen(t, raw, keep) || seen(t, raw, deleted) {
				t.Fatal("move set \\Seen on mail that stayed")
			}
			if _, err := raw.Select("Needs review", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
				t.Fatal(err)
			}
			if seen(t, raw, imap.UID(filed.DestUID)) {
				t.Fatal("moved message was marked \\Seen")
			}
			back, err := session.MoveUID(ctx, "Needs review", filed.DestUID, "<moved@example.com>", "INBOX")
			if err != nil {
				t.Fatal(err)
			}
			if back.DestUID == 0 {
				t.Fatal("undo destination uid was empty")
			}
		})
	}
}

func TestMoveRefusedWithoutUIDPlus(t *testing.T) {
	session, raw, _ := newSession(t, imap.CapSet{imap.CapIMAP4rev1: {}})
	moved := appendRaw(t, raw, "INBOX", textMessage("stay@example.com", "Stay", "here"))
	_, err := session.MoveUID(context.Background(), "INBOX", uint32(moved), "<stay@example.com>", "Elsewhere")
	if !errors.Is(err, ErrMoveUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if _, err := raw.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		t.Fatal(err)
	}
	left := allUIDs(t, raw)
	if !containsUID(left, moved) {
		t.Fatalf("source uid was removed: %v", left)
	}
	if seen(t, raw, moved) {
		t.Fatal("refused move set \\Seen")
	}
}

func TestCopyLeavesSource(t *testing.T) {
	session, raw, _ := newSession(t, imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapUIDPlus: {}})
	ctx := context.Background()
	uid := appendRaw(t, raw, "INBOX", textMessage("copy@example.com", "Copy", "label"))
	if err := session.EnsureMailbox(ctx, "News"); err != nil {
		t.Fatal(err)
	}
	filed, err := session.CopyUID(ctx, "INBOX", uint32(uid), "<copy@example.com>", "News")
	if err != nil {
		t.Fatal(err)
	}
	if filed.DestUID == 0 {
		t.Fatal("copy destination uid was empty")
	}
	if _, err := raw.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		t.Fatal(err)
	}
	if !containsUID(allUIDs(t, raw), uid) {
		t.Fatal("copy removed the source")
	}
	if seen(t, raw, uid) {
		t.Fatal("copy set \\Seen")
	}
}

func newSession(t *testing.T, caps imap.CapSet) (*Session, *imapclient.Client, *x509.CertPool) {
	t.Helper()
	cert, pool, _ := newCert(t)
	const password = "s3cret-do-not-echo"
	addr := startIMAP(t, cert, implicitListener, caps, password)
	host, port := splitAddr(t, addr)
	caPath := writeCert(t, cert.Certificate[0])
	raw := dialRaw(t, addr, pool, "ada", password)
	account := Account{
		Endpoint: Endpoint{Host: host, Port: port, Security: ImplicitTLS, CAFile: caPath},
		Auth:     AuthPassword,
		Username: "ada",
	}
	session, err := NewClient().Connect(context.Background(), account, password)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session, raw, pool
}

func appendFlags(t *testing.T, client *imapclient.Client, mailbox, raw string, flags []imap.Flag) imap.UID {
	t.Helper()
	cmd := client.Append(mailbox, int64(len(raw)), &imap.AppendOptions{Flags: flags})
	if _, err := cmd.Write([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := cmd.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if data.UID == 0 {
		t.Fatal("append did not return a UID")
	}
	return data.UID
}

func allUIDs(t *testing.T, client *imapclient.Client) []imap.UID {
	t.Helper()
	found, err := client.UIDSearch(&imap.SearchCriteria{}, nil).Wait()
	if err != nil {
		t.Fatal(err)
	}
	return found.AllUIDs()
}

type recordingFiler struct {
	moveFrom string
	moveTo   string
	copyTo   string
}

func (f *recordingFiler) MoveMessage(_ context.Context, _ string, from, to string) error {
	f.moveFrom = from
	f.moveTo = to
	return nil
}

func (f *recordingFiler) CopyMessage(_ context.Context, _, to string) error {
	f.copyTo = to
	return nil
}

func TestFilerSkipsIMAP(t *testing.T) {
	session := &Session{}
	filer := &recordingFiler{}
	session.UseFiler(filer)
	if _, err := session.MoveUID(context.Background(), "INBOX", 0, "<a@b.c>", "Work"); err != nil {
		t.Fatal(err)
	}
	if filer.moveFrom != "INBOX" || filer.moveTo != "Work" {
		t.Fatalf("move %+v", filer)
	}
	if _, err := session.CopyUID(context.Background(), "INBOX", 0, "<a@b.c>", "Work"); err != nil {
		t.Fatal(err)
	}
	if filer.copyTo != "Work" {
		t.Fatalf("copy %+v", filer)
	}
}

func containsUID(uids []imap.UID, uid imap.UID) bool {
	for _, item := range uids {
		if item == uid {
			return true
		}
	}
	return false
}
