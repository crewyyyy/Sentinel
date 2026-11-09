package app

import (
	"context"
	"sentinel/internal/domain"
	"testing"
	"time"
)

func TestNoContentEditDoesNotCreateReview(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	m := msg(1, 123, "https://unknown.example")
	deliver(t, e, domain.Update{ID: 1, Message: &m})
	edit := time.Now().UTC().Add(time.Second)
	m.EditedAt = &edit
	deliver(t, e, domain.Update{ID: 2, Message: &m})
	if n := number(t, e, `SELECT count(*) FROM ai_jobs`); n != 1 {
		t.Fatal("unchanged edit paid twice", n)
	}
	if n := number(t, e, `SELECT edit_version FROM message_journal`); n != 1 {
		t.Fatal("unchanged edit created revision", n)
	}
	if n, err := e.Store.Offset(ctx, e.BotID); err != nil || n != 2 {
		t.Fatal(n, err)
	}
}
func TestAdminRepliesCountUniqueAdminDays(t *testing.T) {
	e := testEngine(t)
	m := msg(1, 123, "вопрос")
	deliver(t, e, domain.Update{ID: 1, Message: &m})
	for i := int64(2); i < 7; i++ {
		reply := msg(i, 111, "ответ администратора")
		reply.ReplyID = m.ID
		reply.Reply = &m
		deliver(t, e, domain.Update{ID: i, Message: &reply})
	}
	if n := number(t, e, `SELECT admin_replies FROM daily_activity WHERE chat_id=$1 AND user_id=123`, testChat); n != 1 {
		t.Fatal("same admin spam inflated trust", n)
	}
	if n := number(t, e, `SELECT active_days FROM chat_user_stats WHERE chat_id=$1 AND user_id=123`, testChat); n != 1 {
		t.Fatal(n)
	}
}
func TestPrivateRecipientRevocationAndPublicDestination(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	e.Config.Notifications.SendToPrivateAdmins = true
	m := msg(1, 123, "Купите мой API https://unknown.example?ref=1")
	deliver(t, e, domain.Update{ID: 1, Message: &m})
	n := &notifierFake{public: true}
	if _, err := e.DeliverOne(ctx, n); err != nil {
		t.Fatal(err)
	}
	if n.sends != 0 {
		t.Fatal("sent private case to public group")
	}
	n.public = false
	e.Admins.(*adminFake).valid = false
	if _, err := e.DeliverOne(ctx, n); err != nil {
		t.Fatal(err)
	}
	if n.sends != 0 {
		t.Fatal("revoked admin received private evidence")
	}
	if n := number(t, e, `SELECT count(*) FROM outbox WHERE last_error='recipient_not_authorized'`); n != 1 {
		t.Fatal(n)
	}
}
func TestCursorZeroAndFIFO(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	if n, err := e.Store.Offset(ctx, e.BotID); err != nil || n != -1 {
		t.Fatal("initial cursor must not skip update zero", n, err)
	}
	first := msg(1, 123, "Привет")
	second := msg(2, 123, "после")
	if err := e.Store.Ingest(ctx, e.BotID, prepared(t, e, domain.Update{ID: 0, Message: &first})); err != nil {
		t.Fatal(err)
	}
	if n, err := e.Store.Offset(ctx, e.BotID); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	if err := e.Store.Ingest(ctx, e.BotID, prepared(t, e, domain.Update{ID: 1, Message: &second})); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.Pool.Exec(ctx, `UPDATE inbox SET available_at=now()+interval '1 hour' WHERE id=0`); err != nil {
		t.Fatal(err)
	}
	if worked, err := e.ProcessOne(ctx); err != nil || worked {
		t.Fatal("overtook pending first-observed event", worked, err)
	}
	if _, err := e.Store.Pool.Exec(ctx, `UPDATE inbox SET available_at=now() WHERE id=0`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	if n := number(t, e, `SELECT first_observed_message_id FROM chat_user_stats WHERE chat_id=$1 AND user_id=123`, testChat); n != 1 {
		t.Fatal(n)
	}
}
func TestForgetWithoutRetainedEvidence(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	m := msg(1, 123, "привет")
	deliver(t, e, domain.Update{ID: 1, Message: &m})
	if _, err := e.Store.Pool.Exec(ctx, `UPDATE ai_jobs SET status='done'`); err != nil {
		t.Fatal(err)
	}
	if err := e.Forget(ctx, testChat, 123); err != nil {
		t.Fatal(err)
	}
	if n := number(t, e, `SELECT count(*) FROM message_keys`); n != 0 {
		t.Fatal("identity key not removed", n)
	}
}
