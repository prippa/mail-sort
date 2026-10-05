package engine

import (
	"context"
	"errors"
	"strings"

	"github.com/prippa/mail-sort/internal/classify"
	"github.com/prippa/mail-sort/internal/mail"
	"github.com/prippa/mail-sort/internal/message"
	"github.com/prippa/mail-sort/internal/store"
)

const (
	// DefaultWorkers is the classification pool size.
	DefaultWorkers = 4
	// DefaultMaxMoves is the filing cap for one apply.
	DefaultMaxMoves = 200
	// DefaultLimit is how many newest messages a plan reads.
	DefaultLimit = 200
)

// PlanOptions describes one dry run.
type PlanOptions struct {
	Profile string
	Mailbox string
	Read    mail.ReadOptions
	Workers int
}

// Report is a dry run, an apply, or an undo.
type Report struct {
	Run             store.Run
	Rows            []store.Row
	Aborted         bool
	CopyOnlyRefused bool
	Moves           int
	Copies          int
	Pending         int
	Errors          int
}

// Plan reads the mailbox, classifies messages that are not already filed, and
// stores a dry run. It does not create folders or move mail.
func Plan(ctx context.Context, box Mailbox, clf Classifier, db *store.DB, opt PlanOptions) (Report, error) {
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	if opt.Profile == "" {
		return Report{}, errors.New("engine: profile is empty")
	}
	if opt.Mailbox == "" {
		opt.Mailbox = "INBOX"
	}
	if opt.Read.Limit < 1 {
		opt.Read.Limit = DefaultLimit
	}
	if opt.Workers < 1 {
		opt.Workers = DefaultWorkers
	}
	batch, err := box.Read(ctx, opt.Mailbox, opt.Read)
	if err != nil {
		return Report{}, err
	}
	pending := make([]mail.ReadMessage, 0, len(batch.Messages))
	for _, msg := range batch.Messages {
		applied, err := db.Applied(ctx, opt.Profile, opt.Mailbox, batch.UIDValidity, msg.Message.UID, msg.Message.MessageID)
		if err != nil {
			return Report{}, err
		}
		if applied {
			continue
		}
		pending = append(pending, msg)
	}
	inputs := make([]classify.Input, len(pending))
	for i, msg := range pending {
		inputs[i] = classify.Input{Message: msg.Message, Headers: msg.Headers}
	}
	outcomes, aborted, err := classifyOrdered(ctx, inputs, clf, opt.Workers)
	if err != nil {
		return Report{}, err
	}
	rows := make([]store.Row, 0, len(outcomes))
	var (
		apiCalls  int
		tokensIn  int
		tokensOut int
		cost      float64
		hasCost   bool
	)
	for i, item := range outcomes {
		msg := pending[i].Message
		row := store.Row{
			UID:       msg.UID,
			MessageID: msg.MessageID,
			Subject:   msg.Subject,
			From:      formatFrom(msg.From),
			Status:    store.RowPending,
		}
		if item.err != nil {
			row.Status = store.RowError
			row.Detail = item.err.Error()
			rows = append(rows, row)
			continue
		}
		decision := item.decision
		row.Category = decision.Category
		row.Provider = decision.Provider
		row.Model = decision.Model
		row.Confidence = decision.Confidence
		row.Action = decision.Action
		row.Folder = decision.Folder
		row.Source = decision.Source
		if decision.Urgent {
			row.Detail = "urgent"
		}
		row.TokensIn = decision.InputTokens
		row.TokensOut = decision.OutputTokens
		if amount, ok := classify.CostUSD(decision.InputTokens, decision.OutputTokens, decision.PriceInput, decision.PriceOutput); ok {
			row.CostUSD = amount
			row.HasCost = true
			cost += amount
			hasCost = true
		}
		tokensIn += decision.InputTokens
		tokensOut += decision.OutputTokens
		if countsAPI(decision.Source) {
			apiCalls++
		}
		rows = append(rows, row)
	}
	id, err := db.CreateRun(ctx, store.Run{
		Profile:     opt.Profile,
		Mailbox:     opt.Mailbox,
		UIDValidity: batch.UIDValidity,
		APICalls:    apiCalls,
		TokensIn:    tokensIn,
		TokensOut:   tokensOut,
		CostUSD:     cost,
		HasCost:     hasCost,
	}, rows)
	if err != nil {
		return Report{}, err
	}
	run, stored, err := db.LoadRun(ctx, id)
	if err != nil {
		return Report{}, err
	}
	report := reportOf(run, stored)
	report.Aborted = aborted
	return report, nil
}

func countsAPI(source string) bool {
	switch source {
	case "", "rule", "cache":
		return false
	default:
		return true
	}
}

func formatFrom(list []message.Address) string {
	parts := make([]string, 0, len(list))
	for _, addr := range list {
		switch {
		case addr.Name != "" && addr.Email != "":
			parts = append(parts, addr.Name+" <"+addr.Email+">")
		case addr.Email != "":
			parts = append(parts, addr.Email)
		case addr.Name != "":
			parts = append(parts, addr.Name)
		}
	}
	return strings.Join(parts, "; ")
}

func reportOf(run store.Run, rows []store.Row) Report {
	report := Report{Run: run, Rows: rows}
	for _, row := range rows {
		switch row.Status {
		case store.RowError:
			report.Errors++
		case store.RowPending:
			report.Pending++
		case store.RowApplied:
			if row.Filed == store.FiledMove {
				report.Moves++
			}
			if row.Filed == store.FiledCopy {
				report.Copies++
			}
		}
	}
	return report
}
