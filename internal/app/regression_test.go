package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sentinel/internal/domain"
)

func TestEditAfterJournalPrune(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	m := msg(1, 123, "Привет")
	deliver(t, e, domain.Update{ID: 1, Message: &m})
	if _, err := e.Store.Pool.Exec(ctx, `DELETE FROM message_journal`); err != nil {
		t.Fatal(err)
	}
	m.Text = "https://unknown.example/new"
	ed := time.Now().UTC().Add(time.Second)
	m.EditedAt = &ed
	deliver(t, e, domain.Update{ID: 2, Message: &m})
	if n := number(t, e, `SELECT messages_count FROM chat_user_stats WHERE chat_id=$1 AND user_id=123`, testChat); n != 1 {
		t.Fatal("pruned edit double counted", n)
	}
	if n := number(t, e, `SELECT edit_version FROM message_journal`); n != 2 {
		t.Fatal("revision reused after prune", n)
	}
	if n := number(t, e, `SELECT count(*) FROM ai_jobs`); n != 2 {
		t.Fatal("new mandatory revision lost", n)
	}
}
func TestThousandMessageCapAndMetrics(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	m := msg(1, 123, "Привет")
	deliver(t, e, domain.Update{ID: 1, Message: &m})
	if _, err := e.Store.Pool.Exec(ctx, `INSERT INTO message_journal(chat_id,message_id,payload,occurred_at,retention_until) SELECT $1,n,'{}',now()+n*interval '1 second',now()+interval '7 days' FROM generate_series(2,1005) n`, testChat); err != nil {
		t.Fatal(err)
	}
	if err := e.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if n := number(t, e, `SELECT count(*) FROM message_journal`); n != 1000 {
		t.Fatal(n)
	}
	rec := httptest.NewRecorder()
	e.HealthHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "sentinel_ai_jobs_pending 1") {
		t.Fatal(rec.Code, rec.Body.String())
	}
}
func TestAttestationNotVerifiedAndManualLink(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	m := msg(1, 123, "Купите мой API https://unknown.example?ref=1")
	deliver(t, e, domain.Update{ID: 1, Message: &m})
	id := number(t, e, `SELECT id FROM incidents`)
	command := msg(2, 111, fmt.Sprintf("/attest %d warn Проверено вручную", id))
	command.ChatID = adminChat
	deliver(t, e, domain.Update{ID: 2, Message: &command})
	if n := number(t, e, `SELECT count(*) FROM moderation_events WHERE evidence_level='verified'`); n != 0 {
		t.Fatal("attestation became verified")
	}
	if n := number(t, e, `SELECT count(*) FROM moderation_events WHERE evidence_level='attested' AND source='admin_attested'`); n != 1 {
		t.Fatal(n)
	}
	eventID := number(t, e, `SELECT id FROM moderation_events`)
	command = msg(3, 111, fmt.Sprintf("/link %d %d", id, eventID))
	command.ChatID = adminChat
	deliver(t, e, domain.Update{ID: 3, Message: &command})
	if n := number(t, e, `SELECT count(*) FROM feedback_events WHERE label='link'`); n != 1 {
		t.Fatal(n)
	}
	if _, err := e.Store.Pool.Exec(ctx, `UPDATE ai_jobs SET status='done'`); err != nil {
		t.Fatal(err)
	}
	if err := e.Forget(ctx, testChat, 123); err == nil {
		t.Fatal("deleted retained case")
	}
}
func TestRuleSnapshotsAndOfflineIsolation(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	if err := e.RegisterRules(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.RegisterRules(ctx); err != nil {
		t.Fatal(err)
	}
	e.Rules.AIThreshold = 99
	if err := e.RegisterRules(ctx); err == nil {
		t.Fatal("silently changed existing rules")
	}
	foreign := msg(1, 123, "password=secretvalue")
	foreign.ChatID = -999
	u := prepared(t, e, domain.Update{ID: 1, Message: &foreign})
	if u.Message != nil {
		t.Fatal("collected unmanaged chat")
	}
	b, err := json.Marshal(u)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "secretvalue") {
		t.Fatal("unmanaged private text retained")
	}
	private := msg(2, 111, "password=privatevalue")
	private.ChatID = 111
	if u := prepared(t, e, domain.Update{ID: 2, Message: &private}); u.Message != nil {
		t.Fatal("collected unrelated private text from an admin")
	}
	private.Text = "/stats@another_bot"
	if u := prepared(t, e, domain.Update{ID: 3, Message: &private}); u.Message != nil {
		t.Fatal("consumed another bot command")
	}
}
func TestBenignAIAndLateReviewCannotOverrideNewRevision(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	m := msg(1, 123, "привет")
	deliver(t, e, domain.Update{ID: 1, Message: &m})
	m.Text = "исправлено"
	ed := time.Now().UTC().Add(time.Second)
	m.EditedAt = &ed
	deliver(t, e, domain.Update{ID: 2, Message: &m})
	if _, err := e.ReviewOne(ctx, providerFake{}); err != nil {
		t.Fatal(err)
	}
	if n := number(t, e, `SELECT count(*) FROM ai_reviews`); n != 1 {
		t.Fatal("old mandatory review not saved", n)
	}
	if n := number(t, e, `SELECT count(*) FROM incidents`); n != 0 {
		t.Fatal("late obsolete review created accusation", n)
	}
}
