package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"sentinel/internal/config"
	"sentinel/internal/domain"
	"sentinel/internal/notify"
	"sentinel/internal/review"
	"sentinel/internal/storage"
	"sentinel/migrations"
)

const testChat int64 = -1001111111111
const adminChat int64 = -1002222222222

type adminFake struct{ valid bool }

func (a *adminFake) IsAdmin(context.Context, int64, int64) (bool, error) { return a.valid, nil }
func testEngine(t *testing.T) *Engine {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL unset; integration test skipped")
	}
	ctx := context.Background()
	base, err := storage.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("test_%d", time.Now().UnixNano())
	if _, err = base.Pool.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := parsed.Query()
	q.Set("search_path", schema)
	parsed.RawQuery = q.Encode()
	s, err := storage.New(ctx, parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.Close()
		if _, err := base.Pool.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Error(err)
		}
		base.Close()
	})
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	c := config.Defaults()
	c.Telegram.ChatIDs = []int64{testChat, testChat - 1}
	c.Telegram.AdminChatID = adminChat
	c.Telegram.AllowedAdminIDs = []int64{111}
	c.AI.Model = "fake"
	c.Notifications.BatchWindowSeconds = 0
	return NewEngine(s, c, &adminFake{true}, "testbot", "our_bot")
}
func msg(id, user int64, text string) domain.Message {
	return domain.Message{ChatID: testChat, ID: id, UserID: &user, Date: time.Now().UTC(), Text: text, ChatType: "supergroup", ContentType: "text", Version: 1}
}
func prepared(t *testing.T, e *Engine, u domain.Update) domain.Update {
	t.Helper()
	safe, err := e.Prepare(context.Background(), u)
	if err != nil {
		t.Fatal(err)
	}
	return safe
}
func deliver(t *testing.T, e *Engine, u domain.Update) {
	t.Helper()
	ctx := context.Background()
	u = prepared(t, e, u)
	if err := e.Store.Ingest(ctx, e.BotID, u); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
}
func number(t *testing.T, e *Engine, q string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := e.Store.Pool.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func TestMigrationsAtomicDedupAndRestart(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	// Up/down/up must work on an otherwise clean, disposable schema.
	if _, err := e.Store.Pool.Exec(ctx, migrations.Down+`;DROP TABLE schema_migrations`); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	m := msg(1, 123, "Привет!")
	deliver(t, e, domain.Update{ID: 1, Message: &m})
	deliver(t, e, domain.Update{ID: 1, Message: &m})
	if n := number(t, e, `SELECT count(*) FROM ai_jobs`); n != 1 {
		t.Fatal("first or dedup failed", n)
	}
	if n := number(t, e, `SELECT messages_count FROM chat_user_stats WHERE chat_id=$1 AND user_id=123`, testChat); n != 1 {
		t.Fatal("double count", n)
	}
	e = NewEngine(e.Store, e.Config, e.Admins, e.BotID, e.Username)
	m = msg(2, 123, "Ещё привет")
	deliver(t, e, domain.Update{ID: 2, Message: &m})
	if n := number(t, e, `SELECT count(*) FROM ai_jobs`); n != 1 {
		t.Fatal("restart retriggered first", n)
	}
	original := m.Date
	m.Text = "https://unknown.example"
	edit := original.Add(time.Second)
	m.EditedAt = &edit
	deliver(t, e, domain.Update{ID: 3, Message: &m})
	deliver(t, e, domain.Update{ID: 4, Message: &m})
	if n := number(t, e, `SELECT messages_count FROM chat_user_stats WHERE chat_id=$1 AND user_id=123`, testChat); n != 2 {
		t.Fatal(n)
	}
	if n := number(t, e, `SELECT count(*) FROM ai_jobs`); n != 2 {
		t.Fatal("edit replay changed jobs", n)
	}
	if n := number(t, e, `SELECT edit_version FROM message_journal WHERE message_id=2`); n != 2 {
		t.Fatal(n)
	}
	if _, err := e.Store.Pool.Exec(ctx, `ALTER TABLE inbox ADD CONSTRAINT reject_999 CHECK(id<>999)`); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.Ingest(ctx, e.BotID, domain.Update{ID: 999}); err == nil {
		t.Fatal("ingest should fail")
	}
	if n := number(t, e, `SELECT count(*) FROM processed_updates WHERE update_id=999`); n != 0 {
		t.Fatal("non-atomic dedup")
	}
	if n, err := e.Store.Offset(ctx, e.BotID); err != nil || n != 4 {
		t.Fatal(n, err)
	}
}
func TestMustJobsBudgetLeasesAndRecovery(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	m := msg(1, 123, "Привет")
	deliver(t, e, domain.Update{ID: 1, Message: &m})
	for i, text := range []string{"https://unknown.example", "https://t.me/example_bot?start=1", "У вас скам", "У другого API дешевле"} {
		m = msg(int64(i+2), 123, text)
		deliver(t, e, domain.Update{ID: int64(i + 2), Message: &m})
	}
	if n := number(t, e, `SELECT count(*) FROM ai_jobs WHERE priority='mandatory' AND status='pending'`); n != 5 {
		t.Fatal("lost MUST", n)
	}
	var wg sync.WaitGroup
	ids := make(chan int64, 10)
	errs := make(chan error, 10)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j, err := e.Store.ClaimAI(ctx, 0, time.Minute)
			if err != nil {
				errs <- err
			} else if j != nil {
				ids <- j.ID
			}
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	seen := map[int64]bool{}
	for id := range ids {
		if seen[id] {
			t.Fatal("duplicate active paid attempt", id)
		}
		seen[id] = true
	}
	if len(seen) != 5 {
		t.Fatal("mandatory budget suppressed", seen)
	}
	if _, err := e.Store.Pool.Exec(ctx, `UPDATE ai_jobs SET lease_until=now()-interval '1 second' WHERE id=(SELECT min(id) FROM ai_jobs)`); err != nil {
		t.Fatal(err)
	}
	j, err := e.Store.ClaimAI(ctx, 0, time.Minute)
	if err != nil || j == nil || j.Attempts != 2 {
		t.Fatal(j, err)
	}
	stale := *j
	stale.Token = 1
	tx, err := e.Store.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = storage.FinishAI(ctx, tx, &stale, domain.ReviewResponse{}); err == nil {
		t.Fatal("stale lease accepted")
	}
	tx.Rollback(ctx)
	if err = e.Store.RetryAI(ctx, j, time.Second, "timeout"); err != nil {
		t.Fatal(err)
	}
	var status string
	if err = e.Store.Pool.QueryRow(ctx, `SELECT status FROM ai_jobs WHERE id=$1`, j.ID).Scan(&status); err != nil || status != "pending" {
		t.Fatal(status, err)
	}
	// Optional budget cannot consume beyond the limit, and zero budget leaves jobs pending.
	if _, err = e.Store.Pool.Exec(ctx, `UPDATE ai_jobs SET status='done'; INSERT INTO ai_jobs(chat_id,message_id,priority,payload) VALUES(1,1,'optional','{}'),(1,2,'optional','{}')`); err != nil {
		t.Fatal(err)
	}
	if j, err = e.Store.ClaimAI(ctx, 0, time.Minute); err != nil || j != nil {
		t.Fatal(j, err)
	}
	if j, err = e.Store.ClaimAI(ctx, 1, time.Minute); err != nil || j == nil {
		t.Fatal(j, err)
	}
	if j, err = e.Store.ClaimAI(ctx, 1, time.Minute); err != nil || j != nil {
		t.Fatal(j, err)
	}
}
func TestReportsRBACAndNoFalseSanctions(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	m := msg(1, 123, "У вас скам, деньги не вернули")
	deliver(t, e, domain.Update{ID: 1, Message: &m})
	for i := int64(0); i < 15; i++ {
		r := msg(10+i, 200+i, "/report@our_bot проверьте")
		r.ReplyID = m.ID
		r.Reply = &m
		deliver(t, e, domain.Update{ID: 10 + i, Message: &r})
	}
	if n := number(t, e, `SELECT count(*) FROM user_reports`); n != 15 {
		t.Fatal(n)
	}
	if n := number(t, e, `SELECT count(*) FROM incidents`); n != 1 {
		t.Fatal("report flood", n)
	}
	id := number(t, e, `SELECT id FROM incidents LIMIT 1`)
	if _, err := e.Store.Pool.Exec(ctx, `INSERT INTO alerts(incident_id,destination_chat_id,telegram_message_id,idempotency_key) VALUES($1,$2,77,'fixture')`, id, adminChat); err != nil {
		t.Fatal(err)
	}
	for _, cb := range []domain.Callback{{ID: "forged", UserID: 999, ChatID: adminChat, MessageID: 77, Data: fmt.Sprintf("act:spam:%d", id)}, {ID: "wrongcard", UserID: 111, ChatID: adminChat, MessageID: 78, Data: fmt.Sprintf("act:spam:%d", id)}, {ID: "legit", UserID: 111, ChatID: adminChat, MessageID: 77, Data: fmt.Sprintf("act:spam:%d", id)}} {
		deliver(t, e, domain.Update{ID: 100 + int64(len(cb.ID)), Callback: &cb})
	}
	if n := number(t, e, `SELECT count(*) FROM feedback_events`); n != 1 {
		t.Fatal("RBAC failure", n)
	}
	if n := number(t, e, `SELECT count(*) FROM moderation_events`); n != 0 {
		t.Fatal("label became sanction", n)
	}
	e.Admins.(*adminFake).valid = false
	cb := domain.Callback{ID: "revoked", UserID: 111, ChatID: adminChat, MessageID: 77, Data: fmt.Sprintf("act:benign:%d", id)}
	deliver(t, e, domain.Update{ID: 200, Callback: &cb})
	if n := number(t, e, `SELECT count(*) FROM feedback_events`); n != 1 {
		t.Fatal("revoked admin accepted", n)
	}
	fake := msg(90, 111, "/mute 10m")
	deliver(t, e, domain.Update{ID: 201, Message: &fake})
	if n := number(t, e, `SELECT count(*) FROM moderation_events`); n != 0 {
		t.Fatal("text became sanction", n)
	}
	cm := domain.Membership{ChatID: testChat, ActorID: 111, Date: time.Now().UTC(), Old: domain.MemberState{Status: "member", UserID: 123}, New: domain.MemberState{Status: "kicked", UserID: 123}}
	deliver(t, e, domain.Update{ID: 202, Membership: &cm})
	if n := number(t, e, `SELECT count(*) FROM moderation_events WHERE evidence_level='verified'`); n != 1 {
		t.Fatal(n)
	}
	var raw []byte
	if err := e.Store.Pool.QueryRow(ctx, `SELECT payload FROM incidents WHERE id=$1`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var c notify.Card
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Events) != 1 || c.Events[0].RelatedMessageID != nil {
		t.Fatal("invented relation", c)
	}
	var exported bytes.Buffer
	if err := e.Export(ctx, &exported); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(exported.String(), "chat_id") || strings.Contains(exported.String(), "user_id") {
		t.Fatal("IDs leaked")
	}
}

type providerFake struct{ fail bool }

func (p providerFake) Review(_ context.Context, r domain.ReviewRequest) (domain.ReviewResponse, error) {
	if p.fail {
		return domain.ReviewResponse{}, &review.Error{Code: "provider_http_429", RetryAfter: 17 * time.Second}
	}
	return domain.ReviewResponse{Result: domain.ReviewResult{Category: "phishing", Severity: "high", Recommendation: "review", ExplanationRU: "Проверьте ссылку.", NeedsHumanReview: true}}, nil
}

type notifierFake struct {
	sends, edits int
	fail         bool
	public       bool
}

func (n *notifierFake) PrivateDestination(context.Context, int64) (bool, error) {
	return !n.public, nil
}

func (n *notifierFake) Send(context.Context, int64, string, *models.InlineKeyboardMarkup) (int64, error) {
	n.sends++
	if n.fail {
		return 0, fmt.Errorf("timeout")
	}
	return 77, nil
}
func (n *notifierFake) Edit(context.Context, int64, int64, string, *models.InlineKeyboardMarkup) error {
	n.edits++
	return nil
}
func (n *notifierFake) AnswerCallback(context.Context, string, string) error { return nil }
func TestFullFlowRetriesOutboxAndPrivacy(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	m := msg(1, 123, "Купите мой API дешевле https://evil.example?ref=x&token=secretvalue api_key=secretvalue")
	deliver(t, e, domain.Update{ID: 1, Message: &m})
	if _, err := e.ReviewOne(ctx, providerFake{fail: true}); err != nil {
		t.Fatal(err)
	}
	if n := number(t, e, `SELECT count(*) FROM ai_jobs WHERE status='pending'`); n != 1 {
		t.Fatal(n)
	}
	if _, err := e.Store.Pool.Exec(ctx, `UPDATE ai_jobs SET available_at=now(); UPDATE ai_provider_state SET retry_at=now()`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ReviewOne(ctx, providerFake{}); err != nil {
		t.Fatal(err)
	}
	if n := number(t, e, `SELECT count(*) FROM ai_reviews`); n != 1 {
		t.Fatal(n)
	}
	n := &notifierFake{fail: true}
	if _, err := e.DeliverOne(ctx, n); err != nil {
		t.Fatal(err)
	}
	if n.sends != 1 {
		t.Fatal(n)
	}
	if _, err := e.Store.Pool.Exec(ctx, `UPDATE outbox SET available_at=now()`); err != nil {
		t.Fatal(err)
	}
	n.fail = false
	if _, err := e.DeliverOne(ctx, n); err != nil {
		t.Fatal(err)
	}
	if n.sends != 2 {
		t.Fatal(n)
	}
	if n := number(t, e, `SELECT count(*) FROM alerts WHERE telegram_message_id=77`); n != 1 {
		t.Fatal(n)
	}
	var secrets int64
	if err := e.Store.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM ai_jobs WHERE payload::text LIKE '%secretvalue%')+(SELECT count(*) FROM message_journal WHERE payload::text LIKE '%secretvalue%')+(SELECT count(*) FROM incidents WHERE payload::text LIKE '%secretvalue%')`).Scan(&secrets); err != nil || secrets != 0 {
		t.Fatal("secret persisted", secrets, err)
	}
	m.Text = "Исправлено: документация https://github.com/"
	edit := time.Now().UTC().Add(time.Second)
	m.EditedAt = &edit
	deliver(t, e, domain.Update{ID: 2, Message: &m})
	if _, err := e.DeliverOne(ctx, n); err != nil {
		t.Fatal(err)
	}
	if n.edits != 1 {
		t.Fatal("card not edited", n)
	}
}
func TestRetentionIsolationAndFirstObserved(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	e.Config.Retention.MaxRecentMessagesPerChat = 3
	for i := int64(1); i <= 7; i++ {
		m := msg(i, 123, "нормально")
		if i > 5 {
			m.ChatID = testChat - 1
		}
		deliver(t, e, domain.Update{ID: i, Message: &m})
	}
	if err := e.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if n := number(t, e, `SELECT count(*) FROM message_journal WHERE chat_id=$1`, testChat); n != 3 {
		t.Fatal(n)
	}
	if n := number(t, e, `SELECT count(*) FROM message_journal WHERE chat_id=$1`, testChat-1); n != 2 {
		t.Fatal("cross chat cleanup", n)
	}
	if n := number(t, e, `SELECT count(*) FROM ai_jobs`); n != 2 {
		t.Fatal("first not chat scoped", n)
	}
	m := msg(9, 123, "после очистки")
	deliver(t, e, domain.Update{ID: 9, Message: &m})
	if n := number(t, e, `SELECT count(*) FROM ai_jobs`); n != 2 {
		t.Fatal("cleanup forgot first", n)
	}
	if _, err := e.Store.Pool.Exec(ctx, `UPDATE message_journal SET retention_until=now()-interval '1 day'`); err != nil {
		t.Fatal(err)
	}
	if err := e.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if n := number(t, e, `SELECT count(*) FROM message_journal`); n != 0 {
		t.Fatal(n)
	}
	if n := number(t, e, `SELECT count(*) FROM ai_jobs WHERE status='pending'`); n != 2 {
		t.Fatal("pending evidence dropped", n)
	}
}
