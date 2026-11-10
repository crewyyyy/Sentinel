package app

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"sentinel/internal/domain"
)

type panelRecorder struct {
	notifierFake
	text string
	kb   *models.InlineKeyboardMarkup
}

func (n *panelRecorder) Edit(ctx context.Context, chat, message int64, text string, kb *models.InlineKeyboardMarkup) error {
	n.text, n.kb = text, kb
	return n.notifierFake.Edit(ctx, chat, message, text, kb)
}

func TestConnectionDecisionEditsOriginalCardAndRemovesButtons(t *testing.T) {
	for _, action := range []string{"yes", "no"} {
		t.Run(action, func(t *testing.T) {
			e := discoveryEngine(t)
			deliver(t, e, selfUpdate(1, 111, time.Now().UTC(), "administrator"))
			request := number(t, e, `SELECT id FROM chat_connection_requests`)
			n := &panelRecorder{}
			drainModelOutbox(t, e, n)
			clickConnection(t, e, 2, request, 1, 111, 111, 77, action)
			drainModelOutbox(t, e, n)
			want := "Чат подключён"
			if action == "no" {
				want = "Подключение отклонено"
			}
			if n.sends != 1 || n.edits != 1 || !strings.Contains(n.text, want) || n.kb != nil {
				t.Fatal("decision did not replace the original pending card", n.sends, n.edits, n.text, n.kb)
			}
			if count := number(t, e, `SELECT count(*) FROM outbox WHERE status<>'done'`); count != 0 {
				t.Fatal("decision delivery stuck", count)
			}
		})
	}
}

func TestAIResultDoesNotWaitForBackgroundBatch(t *testing.T) {
	e := testEngine(t)
	e.Config.Notifications.BatchWindowSeconds = 20
	m := msg(1, 123, "Купите мой API https://fixture.example?ref=test")
	deliver(t, e, domain.Update{ID: 1, Message: &m})
	n := &notifierFake{}
	drainModelOutbox(t, e, n)
	if worked, err := e.ReviewOne(context.Background(), providerFake{}); err != nil || !worked {
		t.Fatal(worked, err)
	}
	if count := number(t, e, `SELECT count(*) FROM outbox WHERE status='pending' AND available_at<=now()`); count != 1 {
		t.Fatal("AI result waits for batch window", count)
	}
	drainModelOutbox(t, e, n)
	if n.edits != 1 {
		t.Fatal("AI result did not edit original card", n.edits)
	}
}

func TestFirstCardAndFeedbackDoNotWaitForBackgroundBatch(t *testing.T) {
	e := testEngine(t)
	e.Config.Notifications.BatchWindowSeconds = 20
	ctx := context.Background()
	m := msg(1, 123, "Купите мой API https://fixture.example?ref=test")
	deliver(t, e, domain.Update{ID: 1, Message: &m})
	id := number(t, e, `SELECT id FROM incidents`)
	if n := number(t, e, `SELECT count(*) FROM outbox WHERE available_at<=now() AND status='pending'`); n != 1 {
		t.Fatal("first card waits for batch window", n)
	}
	n := &notifierFake{}
	drainModelOutbox(t, e, n)
	tx, err := e.Store.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = e.queueCardAfter(ctx, tx, id, 20*time.Second); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if n := number(t, e, `SELECT count(*) FROM outbox WHERE status='pending' AND available_at>now()`); n != 1 {
		t.Fatal("background refresh did not retain optional batching", n)
	}
	command := msg(2, 111, fmt.Sprintf("/feedback %d benign", id))
	command.ChatID = adminChat
	deliver(t, e, domain.Update{ID: 2, Message: &command})
	if n := number(t, e, `SELECT count(*) FROM outbox WHERE payload->>'incident_id'=$1 AND status='pending' AND available_at<=now()`, fmt.Sprint(id)); n != 1 {
		t.Fatal("feedback stayed behind background batch", n)
	}
	drainModelOutbox(t, e, n)
	if n.edits != 1 {
		t.Fatal("feedback did not refresh promptly", n.edits)
	}
}
