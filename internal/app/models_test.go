package app

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"sentinel/internal/domain"
	"sentinel/internal/notify"
	"sentinel/internal/storage"
	"sentinel/migrations"
	"sentinel/prompts"
)

type catalogFake struct {
	ids []string
	err error
}

func (c catalogFake) Models(context.Context) ([]string, error) { return c.ids, c.err }

type partialAdmin struct{ denied int64 }

func (a partialAdmin) IsAdmin(_ context.Context, chat, user int64) (bool, error) {
	return chat != a.denied, nil
}
func panelState(t *testing.T, e *Engine, id int64) notify.ModelPanel {
	t.Helper()
	var raw []byte
	if err := e.Store.Pool.QueryRow(context.Background(), `SELECT payload FROM model_panels WHERE id=$1`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var p notify.ModelPanel
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	return p
}
func drainModelOutbox(t *testing.T, e *Engine, n *notifierFake) {
	t.Helper()
	for i := 0; i < 30; i++ {
		worked, err := e.DeliverOne(context.Background(), n)
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			return
		}
	}
	t.Fatal("outbox did not drain")
}
func openModelPanel(t *testing.T, e *Engine, update, user int64, text string) int64 {
	t.Helper()
	m := msg(update, user, text)
	m.ChatID = adminChat
	deliver(t, e, domain.Update{ID: update, Message: &m})
	return number(t, e, `SELECT id FROM model_panels WHERE owner_user_id=$1 ORDER BY id DESC LIMIT 1`, user)
}
func clickModel(t *testing.T, e *Engine, update, id, revision, user, receipt int64, action string, value int) {
	t.Helper()
	deliver(t, e, domain.Update{ID: update, Callback: &domain.Callback{ID: fmt.Sprintf("model-callback-%d", update), UserID: user, ChatID: adminChat, MessageID: receipt, Data: fmt.Sprintf("mdl:%d:%d:%s:%d", id, revision, action, value)}})
}
func TestRuntimeModelPagesConfirmRetryAndRestart(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	ids := []string{}
	for i := 0; i < 14; i++ {
		ids = append(ids, fmt.Sprintf("fixture/model-%02d", i))
	}
	e.Catalog = catalogFake{ids: ids}
	m := msg(1, 123, "Привет")
	deliver(t, e, domain.Update{ID: 1, Message: &m})
	old, err := e.Store.ClaimAI(ctx, 0, time.Minute, e.Config.AI.Model)
	if err != nil || old == nil {
		t.Fatal(old, err)
	}
	if err = e.Store.RetryAI(ctx, old, time.Hour, "provider_http_429"); err != nil {
		t.Fatal(err)
	}
	id := openModelPanel(t, e, 2, 111, "/model")
	n := &notifierFake{}
	drainModelOutbox(t, e, n)
	clickModel(t, e, 3, id, 1, 111, 77, "p", 1)
	p := panelState(t, e, id)
	if p.Page != 1 || p.Revision != 2 {
		t.Fatal(p)
	}
	drainModelOutbox(t, e, n)
	clickModel(t, e, 4, id, 2, 111, 77, "s", 7)
	if n := number(t, e, `SELECT count(*) FROM runtime_ai_model`); n != 0 {
		t.Fatal("selection applied without confirmation")
	}
	drainModelOutbox(t, e, n)
	clickModel(t, e, 5, id, 3, 111, 77, "a", 7)
	drainModelOutbox(t, e, n)
	setting, err := storage.CurrentModel(ctx, e.Store.Pool, e.Config.AI.Model)
	if err != nil || setting.Model != ids[7] || setting.Version != 1 {
		t.Fatal(setting, err)
	}
	if e.Config.AI.Model != "fake" {
		t.Fatal("shared config mutated")
	}
	clickModel(t, e, 6, id, 3, 111, 77, "a", 7)
	if n := number(t, e, `SELECT count(*) FROM model_change_events`); n != 1 {
		t.Fatal("stale callback changed model", n)
	}
	restarted := NewEngine(e.Store, e.Config, e.Admins, e.BotID, e.Username)
	job, err := restarted.Store.ClaimAI(ctx, 0, time.Minute, restarted.Config.AI.Model)
	if err != nil || job == nil {
		t.Fatal("change did not unblock pending review", job, err)
	}
	if job.ID != old.ID || job.Payload.Model != ids[7] || old.Payload.Model != "fake" || job.Payload.PromptVersion != prompts.CurrentVersion {
		t.Fatal("per-attempt selection/fencing", job, old)
	}
	if n := number(t, e, `SELECT count(*) FROM ai_attempts WHERE model=$1 AND prompt_version=$2`, ids[7], prompts.CurrentVersion); n != 1 {
		t.Fatal("attempt model audit missing", n)
	}
}
func TestModelPanelRightsReceiptExpiryAndCatalogFailure(t *testing.T) {
	e := testEngine(t)
	e.Catalog = catalogFake{ids: []string{"fixture/one", "fixture/two"}}
	e.Admins = partialAdmin{denied: testChat - 1}
	m := msg(1, 111, "/model")
	m.ChatID = adminChat
	deliver(t, e, domain.Update{ID: 1, Message: &m})
	if n := number(t, e, `SELECT count(*) FROM model_panels`); n != 0 {
		t.Fatal("one chat admin could change shared model")
	}
	e.Admins = &adminFake{true}
	id := openModelPanel(t, e, 2, 111, "/model two")
	n := &notifierFake{}
	drainModelOutbox(t, e, n)
	p := panelState(t, e, id)
	if len(p.Models) != 1 || p.Models[0] != "fixture/two" {
		t.Fatal("search", p)
	}
	clickModel(t, e, 3, id, 1, 222, 77, "s", 0)
	clickModel(t, e, 4, id, 1, 111, 78, "s", 0)
	e.Admins = partialAdmin{denied: testChat - 1}
	clickModel(t, e, 5, id, 1, 111, 77, "s", 0)
	if p := panelState(t, e, id); p.Selected != -1 {
		t.Fatal("unauthorized button selected model", p)
	}
	e.Admins = &adminFake{true}
	if _, err := e.Store.Pool.Exec(context.Background(), `UPDATE model_panels SET expires_at=now()-interval '1 second' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	clickModel(t, e, 6, id, 1, 111, 77, "s", 0)
	if n := number(t, e, `SELECT count(*) FROM model_change_events`); n != 0 {
		t.Fatal("expired panel changed settings")
	}
	e.Catalog = catalogFake{err: fmt.Errorf("fixture-private-provider-body")}
	if _, err := e.Store.Pool.Exec(context.Background(), `UPDATE model_panels SET created_at=now()-interval '10 seconds'`); err != nil {
		t.Fatal(err)
	}
	m = msg(7, 111, "/model")
	m.ChatID = adminChat
	deliver(t, e, domain.Update{ID: 7, Message: &m})
	if n := number(t, e, `SELECT count(*) FROM outbox WHERE payload::text LIKE '%fixture-private-provider-body%'`); n != 0 {
		t.Fatal("provider error body leaked")
	}
}
func TestConcurrentModelPanelsRejectStaleConfirmation(t *testing.T) {
	e := testEngine(t)
	e.Config.Telegram.AllowedAdminIDs = []int64{111, 222}
	e.Catalog = catalogFake{ids: []string{"fixture/one", "fixture/two"}}
	a := openModelPanel(t, e, 1, 111, "/model")
	b := openModelPanel(t, e, 2, 222, "/model")
	n := &notifierFake{}
	drainModelOutbox(t, e, n)
	clickModel(t, e, 3, a, 1, 111, 77, "s", 0)
	clickModel(t, e, 4, b, 1, 222, 77, "s", 1)
	drainModelOutbox(t, e, n)
	clickModel(t, e, 5, a, 2, 111, 77, "a", 0)
	clickModel(t, e, 6, b, 2, 222, 77, "a", 1)
	current, err := storage.CurrentModel(context.Background(), e.Store.Pool, e.Config.AI.Model)
	if err != nil || current.Model != "fixture/one" {
		t.Fatal("stale confirmation overwrote another admin", current, err)
	}
	if n := number(t, e, `SELECT count(*) FROM model_change_events`); n != 1 {
		t.Fatal(n)
	}
}
func TestModelPanelDeliveryRechecksPrivacyAndAllChatRights(t *testing.T) {
	for _, mode := range []string{"revoked", "public", "expired"} {
		t.Run(mode, func(t *testing.T) {
			e := testEngine(t)
			e.Catalog = catalogFake{ids: []string{"fixture/model"}}
			id := openModelPanel(t, e, 1, 111, "/model")
			n := &notifierFake{}
			switch mode {
			case "revoked":
				e.Admins = partialAdmin{denied: testChat - 1}
			case "public":
				n.public = true
			case "expired":
				if _, err := e.Store.Pool.Exec(context.Background(), `UPDATE model_panels SET expires_at=now()-interval '1 second' WHERE id=$1`, id); err != nil {
					t.Fatal(err)
				}
			}
			if worked, err := e.DeliverOne(context.Background(), n); err != nil || !worked {
				t.Fatal(worked, err)
			}
			if n.sends != 0 || n.edits != 0 {
				t.Fatal("model catalog leaked to unavailable destination")
			}
		})
	}
}

func TestRuntimeModelMigrationUpgradePreservesInbox(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	if _, err := e.Store.Pool.Exec(ctx, migrations.Down+`;DROP TABLE schema_migrations;`+migrations.Up+`;CREATE TABLE schema_migrations(version text PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now());INSERT INTO schema_migrations(version) VALUES('0001_init')`); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.Ingest(ctx, e.BotID, domain.Update{ID: 1}); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if n := number(t, e, `SELECT count(*) FROM schema_migrations`); n != int64(len(migrations.Ordered())) {
		t.Fatal(n)
	}
	if n := number(t, e, `SELECT count(*) FROM inbox`); n != 1 {
		t.Fatal("upgrade lost pending update")
	}
}
