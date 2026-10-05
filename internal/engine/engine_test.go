package engine

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/prippa/mail-sort/internal/classify"
	"github.com/prippa/mail-sort/internal/mail"
	"github.com/prippa/mail-sort/internal/message"
	"github.com/prippa/mail-sort/internal/store"
)

func TestDryRunDoesNotFileAndApplyRequiresConfirmation(t *testing.T) {
	ctx := context.Background()
	db := openEngineDB(t)
	box := &fakeBox{batch: mail.ReadBatch{UIDValidity: 4, Messages: []mail.ReadMessage{testMessage(7, "Invoice", "<inv@example.com>")}}}
	clf := scripted{fn: func(in classify.Input) (classify.Decision, error) {
		return classify.Decision{Category: "work", Confidence: 1, Provider: "rule", Source: "rule", Action: "move", Folder: "Work"}, nil
	}}
	report, err := Plan(ctx, box, &clf, db, PlanOptions{Profile: "Work", Mailbox: "INBOX"})
	if err != nil {
		t.Fatal(err)
	}
	if box.moves != 0 || box.copies != 0 || len(box.ensured) != 0 {
		t.Fatalf("dry run filed mail: %+v", box)
	}
	if report.Run.Status != store.StatusDry || len(report.Rows) != 1 || report.Rows[0].Subject != "Invoice" {
		t.Fatalf("report = %+v", report)
	}
	if _, err := Apply(ctx, box, db, ApplyOptions{Profile: "Work"}); err != ErrNotConfirmed {
		t.Fatalf("apply err = %v", err)
	}
	if box.moves != 0 {
		t.Fatal("unconfirmed apply moved mail")
	}
}

func TestApplyUndoIdempotencyCapAndLabel(t *testing.T) {
	ctx := context.Background()
	db := openEngineDB(t)
	box := &fakeBox{batch: mail.ReadBatch{UIDValidity: 4, Messages: []mail.ReadMessage{
		testMessage(7, "Invoice", "<inv@example.com>"),
		testMessage(8, "News", "<news@example.com>"),
		testMessage(9, "Later", "<later@example.com>"),
	}}}
	clf := scripted{fn: func(in classify.Input) (classify.Decision, error) {
		if in.Message.Subject == "News" {
			return classify.Decision{Category: "news", Confidence: 0.9, Provider: "jev", Source: "jev", Action: "label", Folder: "News", InputTokens: 1000, PriceInput: 1}, nil
		}
		return classify.Decision{Category: "work", Confidence: 1, Provider: "rule", Source: "rule", Action: "move", Folder: "Work"}, nil
	}}
	if _, err := Plan(ctx, box, &clf, db, PlanOptions{Profile: "Work", Mailbox: "INBOX"}); err != nil {
		t.Fatal(err)
	}
	if err := Confirm(ctx, db, "Work"); err != nil {
		t.Fatal(err)
	}
	first, err := Apply(ctx, box, db, ApplyOptions{Profile: "Work", MaxMoves: 1})
	if err != nil {
		t.Fatal(err)
	}
	if first.Moves != 1 || first.Pending == 0 || box.moves != 1 || box.copies != 0 {
		t.Fatalf("first = %+v box=%+v", first, box)
	}
	second, err := Apply(ctx, box, db, ApplyOptions{Profile: "Work", MaxMoves: 1})
	if err != nil {
		t.Fatal(err)
	}
	if second.Moves != 0 || second.Copies != 1 || box.moves != 1 || box.copies != 1 {
		t.Fatalf("second = %+v box=%+v", second, box)
	}
	third, err := Apply(ctx, box, db, ApplyOptions{Profile: "Work", MaxMoves: 1})
	if err != nil {
		t.Fatal(err)
	}
	if third.Moves != 1 || box.moves != 2 {
		t.Fatalf("third = %+v box moves=%d", third, box.moves)
	}
	if _, err := Apply(ctx, box, db, ApplyOptions{Profile: "Work"}); err != ErrNothingToApply {
		t.Fatalf("second full apply err = %v", err)
	}
	calls := clf.calls
	again, err := Plan(ctx, box, &clf, db, PlanOptions{Profile: "Work", Mailbox: "INBOX"})
	if err != nil {
		t.Fatal(err)
	}
	if clf.calls != calls || len(again.Rows) != 0 {
		t.Fatalf("reclassified applied mail: calls %d -> %d rows %d", calls, clf.calls, len(again.Rows))
	}
	undone, err := Undo(ctx, box, db, UndoOptions{Profile: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	if box.moves != 4 {
		t.Fatalf("undo moves = %d, want the two source moves plus two moves back", box.moves)
	}
	var leftCopy bool
	for _, row := range undone.Rows {
		if row.Category == "news" && row.Status == store.RowUndone && row.Filed == store.FiledCopy {
			leftCopy = true
		}
	}
	if !leftCopy {
		t.Fatalf("label undo rows = %+v", undone.Rows)
	}
	if _, err := Plan(ctx, box, &clf, db, PlanOptions{Profile: "Work", Mailbox: "INBOX"}); err != nil {
		t.Fatal(err)
	}
	if clf.calls != calls+2 {
		t.Fatalf("calls = %d, want the two moved messages classified again", clf.calls)
	}
}

func TestMoveRefusedDoesNotCopy(t *testing.T) {
	ctx := context.Background()
	db := openEngineDB(t)
	box := &fakeBox{
		refuseMove: true,
		batch:      mail.ReadBatch{UIDValidity: 1, Messages: []mail.ReadMessage{testMessage(3, "Invoice", "<inv@example.com>")}},
	}
	clf := scripted{fn: func(classify.Input) (classify.Decision, error) {
		return classify.Decision{Category: "work", Action: "move", Folder: "Work", Source: "rule", Confidence: 1}, nil
	}}
	if _, err := Plan(ctx, box, &clf, db, PlanOptions{Profile: "Work", Mailbox: "INBOX"}); err != nil {
		t.Fatal(err)
	}
	if err := Confirm(ctx, db, "Work"); err != nil {
		t.Fatal(err)
	}
	report, err := Apply(ctx, box, db, ApplyOptions{Profile: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	if !report.CopyOnlyRefused || box.copies != 0 || box.moves != 0 {
		t.Fatalf("report = %+v box=%+v", report, box)
	}
}

func TestCopyOnlyLeavesTheSource(t *testing.T) {
	ctx := context.Background()
	db := openEngineDB(t)
	box := &fakeBox{batch: mail.ReadBatch{UIDValidity: 1, Messages: []mail.ReadMessage{testMessage(3, "Invoice", "<inv@example.com>")}}}
	clf := scripted{fn: func(classify.Input) (classify.Decision, error) {
		return classify.Decision{Category: "work", Action: "move", Folder: "Work", Source: "rule", Confidence: 1}, nil
	}}
	if _, err := Plan(ctx, box, &clf, db, PlanOptions{Profile: "Work", Mailbox: "INBOX"}); err != nil {
		t.Fatal(err)
	}
	if err := Confirm(ctx, db, "Work"); err != nil {
		t.Fatal(err)
	}
	report, err := Apply(ctx, box, db, ApplyOptions{Profile: "Work", CopyOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if report.Copies != 1 || report.Moves != 0 || box.moves != 0 {
		t.Fatalf("report = %+v moves=%d", report, box.moves)
	}
	if _, err := Undo(ctx, box, db, UndoOptions{Profile: "Work"}); err != nil {
		t.Fatal(err)
	}
	if box.moves != 0 {
		t.Fatal("undo removed a copy")
	}
}

func TestUIDValidityUsesMessageID(t *testing.T) {
	ctx := context.Background()
	db := openEngineDB(t)
	box := &fakeBox{
		validity: 9,
		found:    50,
		batch:    mail.ReadBatch{UIDValidity: 4, Messages: []mail.ReadMessage{testMessage(7, "Invoice", "<inv@example.com>")}},
	}
	clf := scripted{fn: func(classify.Input) (classify.Decision, error) {
		return classify.Decision{Category: "work", Action: "move", Folder: "Work", Source: "rule", Confidence: 1}, nil
	}}
	if _, err := Plan(ctx, box, &clf, db, PlanOptions{Profile: "Work", Mailbox: "INBOX"}); err != nil {
		t.Fatal(err)
	}
	if err := Confirm(ctx, db, "Work"); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(ctx, box, db, ApplyOptions{Profile: "Work"}); err != nil {
		t.Fatal(err)
	}
	if len(box.moveUIDs) != 1 || box.moveUIDs[0] != 50 {
		t.Fatalf("moved uids = %v", box.moveUIDs)
	}
}

func TestOverrideAndClassifierStop(t *testing.T) {
	ctx := context.Background()
	db := openEngineDB(t)
	messages := make([]mail.ReadMessage, 0, 6)
	for i := uint32(1); i <= 6; i++ {
		messages = append(messages, testMessage(i, "bad", "<bad@example.com>"))
	}
	messages = append(messages, testMessage(7, "ok", "<ok@example.com>"))
	box := &fakeBox{batch: mail.ReadBatch{UIDValidity: 1, Messages: messages}}
	clf := scripted{fn: func(in classify.Input) (classify.Decision, error) {
		if in.Message.Subject == "bad" {
			return classify.Decision{}, errors.New("classifier down")
		}
		return classify.Decision{Category: "work", Action: "move", Folder: "Work", Source: "rule", Confidence: 1}, nil
	}}
	report, err := Plan(ctx, box, &clf, db, PlanOptions{Profile: "Work", Mailbox: "INBOX", Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Aborted || clf.calls != 5 || len(report.Rows) != 5 {
		t.Fatalf("aborted=%v calls=%d rows=%d", report.Aborted, clf.calls, len(report.Rows))
	}
	db2 := openEngineDB(t)
	box2 := &fakeBox{batch: mail.ReadBatch{UIDValidity: 1, Messages: []mail.ReadMessage{testMessage(4, "Invoice", "<inv@example.com>")}}}
	keep := scripted{fn: func(classify.Input) (classify.Decision, error) {
		return classify.Decision{Category: "work", Action: "move", Folder: "Work", Source: "rule", Confidence: 1}, nil
	}}
	if _, err := Plan(ctx, box2, &keep, db2, PlanOptions{Profile: "Work", Mailbox: "INBOX"}); err != nil {
		t.Fatal(err)
	}
	row, err := Override(ctx, db2, "Work", 4, testCategories(), classify.KeepInInbox)
	if err != nil {
		t.Fatal(err)
	}
	if row.Action != "none" || !row.Override {
		t.Fatalf("row = %+v", row)
	}
	if err := Confirm(ctx, db2, "Work"); err != nil {
		t.Fatal(err)
	}
	applied, err := Apply(ctx, box2, db2, ApplyOptions{Profile: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	if applied.Moves != 0 || box2.moves != 0 {
		t.Fatalf("override still moved: %+v", applied)
	}
}

func TestClassifierStreakResets(t *testing.T) {
	ctx := context.Background()
	inputs := make([]classify.Input, 11)
	for i := range inputs {
		inputs[i].Message.Subject = "bad"
	}
	inputs[4].Message.Subject = "ok"
	calls := 0
	clf := &scripted{fn: func(in classify.Input) (classify.Decision, error) {
		calls++
		if in.Message.Subject == "bad" {
			return classify.Decision{}, errors.New("down")
		}
		return classify.Decision{Category: "work"}, nil
	}}
	_, aborted, err := classifyOrdered(ctx, inputs, clf, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !aborted || calls != 10 {
		t.Fatalf("aborted=%v calls=%d", aborted, calls)
	}
}

type scripted struct {
	mu    sync.Mutex
	fn    func(classify.Input) (classify.Decision, error)
	calls int
}

func (s *scripted) Classify(_ context.Context, in classify.Input) (classify.Decision, error) {
	s.mu.Lock()
	s.calls++
	fn := s.fn
	s.mu.Unlock()
	return fn(in)
}

type fakeBox struct {
	batch      mail.ReadBatch
	validity   uint32
	found      uint32
	refuseMove bool
	moves      int
	copies     int
	moveUIDs   []uint32
	ensured    []string
}

func (f *fakeBox) Read(context.Context, string, mail.ReadOptions) (mail.ReadBatch, error) {
	return f.batch, nil
}

func (f *fakeBox) UIDValidity(context.Context, string) (uint32, error) {
	if f.validity != 0 {
		return f.validity, nil
	}
	return f.batch.UIDValidity, nil
}

func (f *fakeBox) FindUID(context.Context, string, string) (uint32, bool, error) {
	if f.found == 0 {
		return 0, false, nil
	}
	return f.found, true, nil
}

func (f *fakeBox) EnsureMailbox(_ context.Context, name string) error {
	f.ensured = append(f.ensured, name)
	return nil
}

func (f *fakeBox) MoveUID(_ context.Context, _ string, uid uint32, _, _ string) (mail.Filed, error) {
	if f.refuseMove {
		return mail.Filed{}, mail.ErrMoveUnavailable
	}
	f.moves++
	f.moveUIDs = append(f.moveUIDs, uid)
	return mail.Filed{DestUID: 1000 + uid}, nil
}

func (f *fakeBox) CopyUID(_ context.Context, _ string, uid uint32, _, _ string) (mail.Filed, error) {
	f.copies++
	return mail.Filed{DestUID: 2000 + uid}, nil
}

func testMessage(uid uint32, subject, id string) mail.ReadMessage {
	return mail.ReadMessage{
		Message: message.Message{
			UID:       uid,
			Subject:   subject,
			MessageID: id,
			From:      []message.Address{{Name: "Ada", Email: "ada@example.com"}},
		},
		Headers: map[string][]string{},
	}
}

func testCategories() []classify.Category {
	return []classify.Category{
		{Key: "work", Name: "Work", Folder: "Work", Action: "move"},
		{Key: classify.KeepInInbox, Name: "Keep", Action: "none"},
	}
}

func openEngineDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "mailsorter.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
