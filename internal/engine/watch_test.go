package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/prippa/mail-sort/internal/classify"
	"github.com/prippa/mail-sort/internal/mail"
	"github.com/prippa/mail-sort/internal/store"
)

func TestNextRefusesUntilThePlanIsApplied(t *testing.T) {
	ctx := context.Background()
	db := openEngineDB(t)
	box := &fakeBox{batch: mail.ReadBatch{UIDValidity: 4, Messages: []mail.ReadMessage{testMessage(7, "Invoice", "<inv@example.com>")}}}
	clf := scripted{fn: func(classify.Input) (classify.Decision, error) {
		return classify.Decision{Category: "work", Confidence: 1, Provider: "rule", Source: "rule", Action: "move", Folder: "Work"}, nil
	}}
	plan := PlanOptions{Profile: "Work", Mailbox: "INBOX"}
	apply := ApplyOptions{Profile: "Work"}
	if _, _, err := Next(ctx, box, &clf, db, plan, apply); !errors.Is(err, ErrNotConfirmed) || box.moves != 0 {
		t.Fatalf("unconfirmed err=%v moves=%d", err, box.moves)
	}
	if _, err := Plan(ctx, box, &clf, db, plan); err != nil {
		t.Fatal(err)
	}
	if err := Confirm(ctx, db, "Work"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Next(ctx, box, &clf, db, plan, apply); !errors.Is(err, ErrOpenPlan) || box.moves != 0 {
		t.Fatalf("open plan err=%v moves=%d", err, box.moves)
	}
	if _, err := Apply(ctx, box, db, apply); err != nil {
		t.Fatal(err)
	}
	box.batch.Messages = append(box.batch.Messages, testMessage(8, "New", "<new@example.com>"))
	report, changed, err := Next(ctx, box, &clf, db, plan, apply)
	if err != nil || !changed || report.Moves != 1 || box.moves != 2 {
		t.Fatalf("file err=%v changed=%v report=%+v moves=%d", err, changed, report, box.moves)
	}
	if _, changed, err = Next(ctx, box, &clf, db, plan, apply); err != nil || changed || box.moves != 2 {
		t.Fatalf("idle err=%v changed=%v moves=%d", err, changed, box.moves)
	}
	if _, _, err := db.LatestOpen(ctx, "Work"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("open plan err = %v", err)
	}
}

func TestNextContinuesAPartialApply(t *testing.T) {
	ctx := context.Background()
	db := openEngineDB(t)
	box := &fakeBox{batch: mail.ReadBatch{UIDValidity: 4, Messages: []mail.ReadMessage{testMessage(7, "Invoice", "<inv@example.com>")}}}
	clf := scripted{fn: func(classify.Input) (classify.Decision, error) {
		return classify.Decision{Category: "work", Confidence: 1, Provider: "rule", Source: "rule", Action: "move", Folder: "Work"}, nil
	}}
	plan := PlanOptions{Profile: "Work", Mailbox: "INBOX"}
	apply := ApplyOptions{Profile: "Work"}
	if _, err := Plan(ctx, box, &clf, db, plan); err != nil {
		t.Fatal(err)
	}
	if err := Confirm(ctx, db, "Work"); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(ctx, box, db, apply); err != nil {
		t.Fatal(err)
	}
	box.batch.Messages = []mail.ReadMessage{testMessage(10, "One", "<one@example.com>"), testMessage(11, "Two", "<two@example.com>")}
	apply.MaxMoves = 1
	report, changed, err := Next(ctx, box, &clf, db, plan, apply)
	if err != nil || !changed || report.Moves != 1 || report.Pending != 1 {
		t.Fatalf("cap err=%v changed=%v report=%+v", err, changed, report)
	}
	report, changed, err = Next(ctx, box, &clf, db, plan, apply)
	if err != nil || !changed || report.Moves != 1 || report.Pending != 0 || box.moves != 3 {
		t.Fatalf("rest err=%v changed=%v report=%+v moves=%d", err, changed, report, box.moves)
	}
}
