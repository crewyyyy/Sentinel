package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"sentinel/internal/domain"
	"sentinel/internal/notify"
)

type legitimateProvider struct{}

func (legitimateProvider) Review(context.Context, domain.ReviewRequest) (domain.ReviewResponse, error) {
	return domain.ReviewResponse{Result: domain.ReviewResult{Category: "legitimate", Severity: "none", Recommendation: "ignore", ExplanationRU: "Обычное сообщение."}}, nil
}

func TestReportShowsActualCompletedRevisionReview(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	original := msg(1, 123, "Привет!")
	deliver(t, e, domain.Update{ID: 1, Message: &original})
	if worked, err := e.ReviewOne(ctx, legitimateProvider{}); err != nil || !worked {
		t.Fatal(worked, err)
	}
	if n := number(t, e, `SELECT count(*) FROM incidents`); n != 0 {
		t.Fatal("benign review created an accusing card", n)
	}
	r := msg(2, 456, "/report проверьте")
	r.ReplyID, r.Reply = original.ID, &original
	deliver(t, e, domain.Update{ID: 2, Message: &r})
	var raw []byte
	if err := e.Store.Pool.QueryRow(ctx, `SELECT payload FROM incidents`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var card notify.Card
	if err := json.Unmarshal(raw, &card); err != nil {
		t.Fatal(err)
	}
	if card.AI == nil || card.AIState != "done" || card.AI.Result.Category != "legitimate" {
		t.Fatal("actual completed review was hidden as pending", card)
	}
	if n := number(t, e, `SELECT count(*) FROM ai_jobs WHERE message_id=1`); n != 1 {
		t.Fatal("report duplicated completed review", n)
	}
}

func TestExportUsesLabelTimeSnapshotNotLaterEdit(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	original := msg(1, 123, "Купите мой API https://fixture.example?ref=1")
	deliver(t, e, domain.Update{ID: 1, Message: &original})
	id := number(t, e, `SELECT id FROM incidents`)
	label := msg(2, 111, fmt.Sprintf("/feedback %d spam", id))
	label.ChatID = adminChat
	deliver(t, e, domain.Update{ID: 2, Message: &label})
	edited := original
	edited.Text = "Теперь обычное сообщение"
	at := time.Now().UTC().Add(time.Second)
	edited.EditedAt = &at
	deliver(t, e, domain.Update{ID: 3, Message: &edited})
	var output bytes.Buffer
	if err := e.Export(ctx, &output); err != nil {
		t.Fatal(err)
	}
	var sample Sample
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &sample); err != nil {
		t.Fatal(err)
	}
	if sample.Text != original.Text || sample.Label != "spam" {
		t.Fatal("old human label was applied to new evidence", sample)
	}
}

func TestReportQueueAndArbitrationRequireCurrentRights(t *testing.T) {
	e := testEngine(t)
	m := msg(1, 123, "Купите мой API https://fixture.example?ref=1")
	deliver(t, e, domain.Update{ID: 1, Message: &m})
	id := number(t, e, `SELECT id FROM incidents`)
	r := msg(2, 456, "/report проверьте")
	r.ReplyID, r.Reply = m.ID, &m
	deliver(t, e, domain.Update{ID: 2, Message: &r})
	reportID := number(t, e, `SELECT id FROM user_reports`)
	e.Admins = &adminFake{valid: false}
	for i, command := range []string{fmt.Sprintf("/reports %d", testChat), fmt.Sprintf("/reportcase %d", reportID), fmt.Sprintf("/resolve %d spam", id)} {
		m := msg(int64(10+i), 111, command)
		m.ChatID = adminChat
		deliver(t, e, domain.Update{ID: m.ID, Message: &m})
	}
	if n := number(t, e, `SELECT count(*) FROM authorization_audit WHERE NOT allowed`); n != 3 {
		t.Fatal("new commands skipped current rights checks", n)
	}
	if n := number(t, e, `SELECT count(*) FROM feedback_events`); n != 0 {
		t.Fatal("revoked administrator arbitrated a label", n)
	}
}
