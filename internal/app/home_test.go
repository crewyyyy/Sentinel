package app

import (
	"context"
	"fmt"

	"github.com/go-telegram/bot"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"sentinel/internal/domain"
)

type homeRecorder struct {
	notifierFake
	text string
	kb   *models.InlineKeyboardMarkup
}

func (n *homeRecorder) Send(ctx context.Context, chat int64, text string, kb *models.InlineKeyboardMarkup) (int64, error) {
	n.text, n.kb = text, kb
	return n.notifierFake.Send(ctx, chat, text, kb)
}
func (n *homeRecorder) Edit(ctx context.Context, chat, message int64, text string, kb *models.InlineKeyboardMarkup) error {
	n.text, n.kb = text, kb
	return n.notifierFake.Edit(ctx, chat, message, text, kb)
}
func (n *homeRecorder) SendBanner(ctx context.Context, chat int64, text string, kb *models.InlineKeyboardMarkup) (int64, error) {
	n.text, n.kb = text, kb
	return n.notifierFake.SendBanner(ctx, chat, text, kb)
}
func (n *homeRecorder) EditCaption(ctx context.Context, chat, message int64, text string, kb *models.InlineKeyboardMarkup) error {
	n.text, n.kb = text, kb
	return n.notifierFake.EditCaption(ctx, chat, message, text, kb)
}
func openHomePanel(t *testing.T, e *Engine, update int64, text string, n *homeRecorder) int64 {
	t.Helper()
	m := msg(update, 111, text)
	m.ChatID = adminChat
	deliver(t, e, domain.Update{ID: update, Message: &m})
	drainModelOutbox(t, e, n)
	return number(t, e, `SELECT id FROM control_panels ORDER BY id DESC LIMIT 1`)
}
func clickHome(t *testing.T, e *Engine, update, id int64, action string, value int, n *homeRecorder) {
	t.Helper()
	p, err := readControlPanel(context.Background(), e.Store.Pool, id, false)
	if err != nil {
		t.Fatal(err)
	}
	deliver(t, e, domain.Update{ID: update, Callback: &domain.Callback{ID: fmt.Sprintf("home-%d", update), UserID: 111, ChatID: adminChat, MessageID: 77, Data: fmt.Sprintf("nav:%d:%d:%s:%d", id, p.Revision, action, value)}})
	drainModelOutbox(t, e, n)
}

func TestHomeNavigationEditsOneMessageAndSurvivesRestart(t *testing.T) {
	e := testEngine(t)
	n := &homeRecorder{}
	id := openHomePanel(t, e, 1, "/start", n)
	if n.sends != 1 || n.edits != 0 || len(n.kb.InlineKeyboard) != 3 || strings.Contains(n.text, "-100") {
		t.Fatal("start is not a short private menu")
	}
	for i, view := range []string{"connect", "help", "status", "chats", "home"} {
		clickHome(t, e, int64(i+2), id, view, 0, n)
		p, err := readControlPanel(context.Background(), e.Store.Pool, id, false)
		if err != nil || p.View != view {
			t.Fatal("view not saved", view, err)
		}
	}
	if n.sends != 1 || n.edits != 5 {
		t.Fatal("navigation spammed new messages", n.sends, n.edits)
	}
	e = mustEngine(t, e.Store, e.Config, e.Admins, e.BotID, e.Username)
	clickHome(t, e, 10, id, "help", 0, n)
	if n.sends != 1 || n.edits != 6 || !strings.Contains(n.text, "/report") {
		t.Fatal("restart lost menu or help")
	}
	if n.bannerSends != 1 || n.captionEdits != 6 {
		t.Fatal("banner was resent or navigation edited text instead of caption", n.bannerSends, n.captionEdits)
	}
}

func TestLegacyHomeReceiptKeepsTextEditing(t *testing.T) {
	e := testEngine(t)
	n := &homeRecorder{}
	id := openHomePanel(t, e, 1, "/start", n)
	if _, err := e.Store.Pool.Exec(context.Background(), `UPDATE outbox SET result='{"message_id":77}'::jsonb WHERE kind='control_panel'`); err != nil {
		t.Fatal(err)
	}
	clickHome(t, e, 2, id, "help", 0, n)
	if n.bannerSends != 1 || n.edits != 1 || n.captionEdits != 0 {
		t.Fatal("legacy text receipt was edited as a photo", n.bannerSends, n.edits, n.captionEdits)
	}
}

type failingCaptionNotifier struct {
	homeRecorder
	err error
}

func (n *failingCaptionNotifier) EditCaption(context.Context, int64, int64, string, *models.InlineKeyboardMarkup) error {
	return n.err
}

func TestHomeMediaFailureRetryAndReplacement(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		newPhoto bool
	}{
		{"deleted", fmt.Errorf("%w: message to edit not found", bot.ErrorBadRequest), true},
		{"rate limited", bot.ErrorTooManyRequests, false},
		{"transport", fmt.Errorf("timeout"), false},
		{"unchanged", fmt.Errorf("%w: message is not modified", bot.ErrorBadRequest), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := testEngine(t)
			n := &failingCaptionNotifier{err: tc.err}
			id := openHomePanel(t, e, 1, "/start", &n.homeRecorder)
			if _, err := e.Store.Pool.Exec(context.Background(), `UPDATE outbox SET status='pending',available_at=now() WHERE kind='control_panel'`); err != nil {
				t.Fatal(err)
			}
			if worked, err := e.DeliverOne(context.Background(), n); err != nil || !worked {
				t.Fatal(worked, err)
			}
			want := 1
			if tc.newPhoto {
				want++
			}
			if n.bannerSends != want {
				t.Fatal("unexpected replacement upload", n.bannerSends, want)
			}
			if tc.name == "rate limited" || tc.name == "transport" {
				if count := number(t, e, `SELECT count(*) FROM outbox WHERE status='pending' AND result->>'banner'='true'`); count != 1 {
					t.Fatal("retry lost photo receipt", count)
				}
			}
			p, err := readControlPanel(context.Background(), e.Store.Pool, id, false)
			if err != nil || p.Receipt != 77 {
				t.Fatal("retry lost panel receipt", err)
			}
		})
	}
}

func TestHomeCallbacksRequireOwnerReceiptRevisionAndExpiry(t *testing.T) {
	e := testEngine(t)
	n := &homeRecorder{}
	id := openHomePanel(t, e, 1, "/start", n)
	clickHome(t, e, 2, id, "chats", 0, n)
	for i, cb := range []domain.Callback{
		{UserID: 999, ChatID: adminChat, MessageID: 77},
		{UserID: 111, ChatID: adminChat, MessageID: 88},
		{UserID: 111, ChatID: adminChat - 1, MessageID: 77},
		{UserID: 111, ChatID: adminChat, MessageID: 77},
	} {
		cb.ID = fmt.Sprintf("home-forged-%d", i)
		rev := int64(2)
		if i == 3 {
			rev = 1
		}
		cb.Data = fmt.Sprintf("nav:%d:%d:connect:0", id, rev)
		deliver(t, e, domain.Update{ID: int64(10 + i), Callback: &cb})
		drainModelOutbox(t, e, n)
	}
	p, err := readControlPanel(context.Background(), e.Store.Pool, id, false)
	if err != nil || p.View != "chats" || p.Revision != 2 {
		t.Fatal("forged or stale callback changed menu", err)
	}
	if _, err = e.Store.Pool.Exec(context.Background(), `UPDATE control_panels SET expires_at=now()-interval '1 second' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	clickHome(t, e, 20, id, "connect", 0, n)
	p, _ = readControlPanel(context.Background(), e.Store.Pool, id, false)
	if p.Revision != 2 || n.sends != 1 {
		t.Fatal("expired menu accepted callback")
	}
}

func TestHomeRefreshDoesNotLeakRevokedChatNamesOrCounters(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	if _, err := e.Store.Pool.Exec(ctx, `INSERT INTO chats(chat_id,title) VALUES($1,'Visible fixture'),($2,'Revoked fixture')`, testChat, testChat-1); err != nil {
		t.Fatal(err)
	}
	for i, chat := range []int64{testChat, testChat - 1} {
		m := msg(1, 123, "Привет")
		m.ChatID = chat
		deliver(t, e, domain.Update{ID: int64(i + 1), Message: &m})
	}
	n := &homeRecorder{}
	id := openHomePanel(t, e, 3, "/chats", n)
	if !strings.Contains(n.text, "Revoked fixture") {
		t.Fatal("fixture chat missing before rights revocation")
	}
	e.Admins = partialAdmin{denied: testChat - 1}
	clickHome(t, e, 4, id, "chats", 0, n)
	if strings.Contains(n.text, "Revoked fixture") || !strings.Contains(n.text, "Visible fixture") {
		t.Fatal("stale menu disclosed revoked chat")
	}
	data, err := e.homeData(ctx, 111, "status")
	if err != nil || data.PendingReviews != 1 || len(data.ChatTitles) != 1 {
		t.Fatal("status leaked another chat's counters", err)
	}
	e.Admins = connectionAdmin{unavailable: true}
	clickHome(t, e, 5, id, "chats", 0, n)
	if strings.Contains(n.text, "Visible fixture") || !strings.Contains(n.text, "недоступны") {
		t.Fatal("unavailable rights were treated as access")
	}
	clickHome(t, e, 6, id, "help", 0, n)
	if !strings.Contains(n.text, "/report") {
		t.Fatal("help unnecessarily depends on chat API availability")
	}
}

func TestHomeOpensExistingModuleAndModelAuthorizationFlows(t *testing.T) {
	e := testEngine(t)
	e.Catalog = catalogFake{ids: []string{"fixture/model"}}
	n := &homeRecorder{}
	id := openHomePanel(t, e, 1, "/start", n)
	clickHome(t, e, 2, id, "modules", 0, n)
	if number(t, e, `SELECT count(*) FROM module_panels WHERE owner_user_id=111`) != 1 {
		t.Fatal("module button did not open existing settings")
	}
	clickHome(t, e, 3, id, "model", 0, n)
	if number(t, e, `SELECT count(*) FROM model_panels WHERE owner_user_id=111`) != 1 {
		t.Fatal("model button did not open catalog")
	}
	e.Admins = partialAdmin{denied: testChat - 1}
	clickHome(t, e, 4, id, "model", 0, n)
	if number(t, e, `SELECT count(*) FROM model_panels`) != 1 {
		t.Fatal("menu bypassed all-chat model permissions")
	}
}

func TestHomeKeepsApprovedTitleAfterRequestCleanup(t *testing.T) {
	e := discoveryEngine(t)
	ctx := context.Background()
	at := time.Now().UTC()
	deliver(t, e, selfUpdate(1, 111, at, "administrator"))
	request := number(t, e, `SELECT id FROM chat_connection_requests`)
	drainModelOutbox(t, e, &notifierFake{})
	clickConnection(t, e, 2, request, 1, 111, 111, 77, "yes")
	if _, err := e.Store.Pool.Exec(ctx, `UPDATE chat_connection_requests SET expires_at=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if err := e.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if number(t, e, `SELECT count(*) FROM chat_connection_requests`) != 0 {
		t.Fatal("expired request fixture was not cleaned")
	}
	data, err := e.homeData(ctx, 111, "chats")
	if err != nil || len(data.ChatTitles) != 1 || data.ChatTitles[0] != "Fixture group" {
		t.Fatal("request cleanup lost approved chat title", err)
	}
	update := selfUpdate(3, 111, at.Add(time.Second), "administrator")
	update.Membership.Old.Status = "administrator"
	update.Membership.ChatTitle = "Renamed fixture"
	deliver(t, e, update)
	data, err = e.homeData(ctx, 111, "chats")
	if err != nil || len(data.ChatTitles) != 1 || data.ChatTitles[0] != "Renamed fixture" {
		t.Fatal("observed membership did not refresh chat title", err)
	}
}

func TestHomeDeliveryChecksPrivacyAllowlistAndCleanup(t *testing.T) {
	for _, mode := range []string{"public", "revoked", "expired"} {
		t.Run(mode, func(t *testing.T) {
			e := testEngine(t)
			n := &homeRecorder{}
			id := openHomePanel(t, e, 1, "/start", n)
			if _, err := e.Store.Pool.Exec(context.Background(), `UPDATE outbox SET status='pending',available_at=now() WHERE kind='control_panel'`); err != nil {
				t.Fatal(err)
			}
			var sender Notifier = n
			switch mode {
			case "public":
				sender = &publicModuleNotifier{&n.notifierFake}
			case "revoked":
				e.Config.Telegram.AllowedAdminIDs = nil
			case "expired":
				if _, err := e.Store.Pool.Exec(context.Background(), `UPDATE control_panels SET expires_at=now()-interval '1 second' WHERE id=$1`, id); err != nil {
					t.Fatal(err)
				}
			}
			worked, err := e.DeliverOne(context.Background(), sender)
			if err != nil || !worked {
				t.Fatal(worked, err)
			}
			if n.sends != 1 || n.edits != 0 {
				t.Fatal("unsafe home delivery")
			}
			if mode == "expired" {
				if err = e.Cleanup(context.Background()); err != nil {
					t.Fatal(err)
				}
				if number(t, e, `SELECT count(*) FROM control_panels`) != 0 {
					t.Fatal("expired menu was not pruned")
				}
			}
		})
	}
}
