package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"sentinel/internal/config"
	"sentinel/internal/domain"
	"sentinel/internal/telegram"
)

type messageRoleCheck func(context.Context, int64, int64) (bool, error)

func (f messageRoleCheck) IsAdmin(ctx context.Context, chat, user int64) (bool, error) {
	return f(ctx, chat, user)
}

func TestIgnoreAdminMessageUsesChatRole(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		enabled, admin, failure bool
		chat, sender, user      int64
		want, wantErr           bool
		calls                   int
	}{
		{name: "disabled", admin: true, chat: testChat, user: 123},
		{name: "admin not allowlisted", enabled: true, admin: true, chat: testChat, user: 123, want: true, calls: 1},
		{name: "owner", enabled: true, admin: true, chat: testChat, user: 111, want: true, calls: 1},
		{name: "allowlisted member", enabled: true, chat: testChat, user: 111, calls: 1},
		{name: "member", enabled: true, chat: testChat, user: 123, calls: 1},
		{name: "anonymous admin", enabled: true, chat: testChat, sender: testChat, want: true},
		{name: "other channel with fake from", enabled: true, admin: true, chat: testChat, sender: testChat - 1, user: 123},
		{name: "no author", enabled: true, chat: testChat},
		{name: "private", enabled: true, admin: true, chat: 123, user: 123},
		{name: "admin destination", enabled: true, admin: true, chat: adminChat, user: 111},
		{name: "lookup failure", enabled: true, chat: testChat, user: 123, failure: true, wantErr: true, calls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := config.Defaults()
			c.Telegram.AdminChatID = adminChat
			c.Telegram.AllowedAdminIDs = []int64{111}
			c.Moderation.IgnoreAdminMessages = tc.enabled
			calls := 0
			e := &Engine{Config: c, Admins: messageRoleCheck(func(_ context.Context, chat, user int64) (bool, error) {
				calls++
				if chat != tc.chat || user != tc.user {
					t.Fatal("role lookup is not chat-scoped")
				}
				if tc.failure {
					return false, errors.New("role unavailable")
				}
				return tc.admin, nil
			})}
			m := domain.Message{ChatID: tc.chat, ChatType: "supergroup"}
			if tc.user != 0 {
				m.UserID = &tc.user
			}
			if tc.sender != 0 {
				m.SenderChatID = &tc.sender
			}
			got, err := e.ignoreAdminMessage(context.Background(), m)
			if got != tc.want || (err != nil) != tc.wantErr || calls != tc.calls {
				t.Fatalf("ignored=%v, error=%v, calls=%d; want %v, %v, %d", got, err, calls, tc.want, tc.wantErr, tc.calls)
			}
		})
	}
}

func TestAdminMessagesExcludedBeforeDurableContent(t *testing.T) {
	e := testEngine(t)
	e.Config.Moderation.IgnoreAdminMessages = true
	e.Admins = messageRoleCheck(func(_ context.Context, _ int64, user int64) (bool, error) { return user == 123, nil })
	ctx := context.Background()
	m := msg(1, 123, "https://unknown.example")
	safe := prepared(t, e, domain.Update{ID: 1, Message: &m})
	if safe.Message != nil {
		t.Fatal("admin message reached durable inbox")
	}
	deliver(t, e, domain.Update{ID: 1, Message: &m})
	edit := time.Now().UTC().Add(time.Second)
	m.EditedAt = &edit
	m.Caption, m.Text = m.Text, ""
	deliver(t, e, domain.Update{ID: 2, Message: &m})
	for _, table := range []string{"message_journal", "message_keys", "message_revisions", "message_urls", "risk_assessments", "ai_jobs", "chat_user_stats", "daily_activity", "incidents", "outbox"} {
		if n := number(t, e, "SELECT count(*) FROM "+table); n != 0 {
			t.Fatalf("ignored message created %s rows: %d", table, n)
		}
	}
	if n, err := e.Store.Offset(ctx, e.BotID); err != nil || n != 2 {
		t.Fatalf("ignored updates did not advance durable cursor: %d, %v", n, err)
	}
	member := msg(3, 124, "https://unknown.example")
	deliver(t, e, domain.Update{ID: 3, Message: &member})
	if n := number(t, e, `SELECT count(*) FROM ai_jobs WHERE priority='mandatory'`); n != 1 {
		t.Fatal("member mandatory job skipped", n)
	}
	// Turning the option off restores the normal first-observed review for the admin.
	e.Config.Moderation.IgnoreAdminMessages = false
	m = msg(4, 123, "https://unknown.example")
	deliver(t, e, domain.Update{ID: 4, Message: &m})
	if n := number(t, e, `SELECT count(*) FROM ai_jobs WHERE priority='mandatory'`); n != 2 {
		t.Fatal("disabled ignore flag still skipped admin review", n)
	}
}

func TestAdminIgnorePreservesAllMemberMandatoryTriggers(t *testing.T) {
	e := testEngine(t)
	e.Config.Moderation.IgnoreAdminMessages = true
	e.Config.AI.OptionalDailyRequestLimit = 0
	e.Admins = &adminFake{false}
	for i, text := range []string{"Привет", "https://unknown.example", "https://t.me/example_bot?start=1", "У вас скам"} {
		m := msg(int64(i+1), 123, text)
		deliver(t, e, domain.Update{ID: int64(i + 1), Message: &m})
	}
	if n := number(t, e, `SELECT count(*) FROM ai_jobs WHERE priority='mandatory' AND status='pending'`); n != 4 {
		t.Fatal("member mandatory triggers lost with admin ignore enabled", n)
	}
}

func TestAnonymousAdminIgnoredButExternalChannelIsAnalyzed(t *testing.T) {
	e := testEngine(t)
	e.Config.Moderation.IgnoreAdminMessages = true
	for i, sender := range []int64{testChat, testChat - 1} {
		m := msg(int64(i+1), 123, "https://unknown.example")
		m.UserID = nil
		m.SenderChatID = &sender
		deliver(t, e, domain.Update{ID: int64(i + 1), Message: &m})
	}
	if n := number(t, e, `SELECT count(*) FROM message_journal WHERE sender_chat_id=$1`, testChat); n != 0 {
		t.Fatal("anonymous admin journaled", n)
	}
	if n := number(t, e, `SELECT count(*) FROM ai_jobs WHERE message_id=2 AND priority='mandatory'`); n != 1 {
		t.Fatal("external channel gained admin exemption", n)
	}
}

func TestAdminIgnoreKeepsReportsCommandsAndHistoricalJobs(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	m := msg(1, 123, "https://unknown.example")
	deliver(t, e, domain.Update{ID: 1, Message: &m})
	var before []byte
	if err := e.Store.Pool.QueryRow(ctx, `SELECT payload FROM ai_jobs WHERE message_id=1`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	e.Config.Moderation.IgnoreAdminMessages = true
	// A new administrator can report a target that was never stored, even another admin's message.
	report := msg(2, 111, "/report проверить")
	target := msg(9, 124, "https://other.example")
	report.ReplyID, report.Reply = target.ID, &target
	deliver(t, e, domain.Update{ID: 2, Message: &report})
	if n := number(t, e, `SELECT count(*) FROM user_reports WHERE reporter_id=111 AND target_message_id=9`); n != 1 {
		t.Fatal("admin report lost", n)
	}
	if n := number(t, e, `SELECT count(*) FROM ai_jobs WHERE message_id=9 AND priority='mandatory'`); n != 1 {
		t.Fatal("explicit report did not enqueue target", n)
	}
	if n := number(t, e, `SELECT count(*) FROM message_journal WHERE message_id=2`); n != 0 {
		t.Fatal("report command was analyzed as ordinary admin message", n)
	}
	start := msg(3, 111, "/start")
	start.ChatID = 111
	start.ChatType = "private"
	deliver(t, e, domain.Update{ID: 3, Message: &start})
	if n := number(t, e, `SELECT count(*) FROM control_panels WHERE owner_user_id=111`); n != 1 {
		t.Fatal("private start command stopped working", n)
	}
	var after []byte
	if err := e.Store.Pool.QueryRow(ctx, `SELECT payload FROM ai_jobs WHERE message_id=1`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("ignore flag rewrote existing job")
	}
}

func TestAdminRoleErrorsAndQueuedMessagesAreNotDropped(t *testing.T) {
	e := testEngine(t)
	e.Config.Moderation.IgnoreAdminMessages = true
	ctx := context.Background()
	m := msg(1, 123, "https://unknown.example")
	e.Admins = messageRoleCheck(func(context.Context, int64, int64) (bool, error) {
		return false, errors.New("role unavailable")
	})
	if _, err := e.Prepare(ctx, domain.Update{ID: 1, Message: &m}); err == nil {
		t.Fatal("role lookup failure was silently ignored")
	}
	if n, err := e.Store.Offset(ctx, e.BotID); err != nil || n != -1 {
		t.Fatal("failed preparation advanced cursor", n, err)
	}
	// Simulate already-durable evidence prepared before the option was enabled.
	e.Config.Moderation.IgnoreAdminMessages = false
	u := prepared(t, e, domain.Update{ID: 1, Message: &m})
	if err := e.Store.Ingest(ctx, e.BotID, u); err != nil {
		t.Fatal(err)
	}
	e.Config.Moderation.IgnoreAdminMessages = true
	if worked, err := e.ProcessOne(ctx); !worked || err == nil {
		t.Fatal("failed role check completed queued message", worked, err)
	}
	if n := number(t, e, `SELECT count(*) FROM inbox WHERE status='pending' AND attempts=1`); n != 1 {
		t.Fatal("role error lost pending inbox", n)
	}
	if _, err := e.Store.Pool.Exec(ctx, `UPDATE inbox SET available_at=now()`); err != nil {
		t.Fatal(err)
	}
	e.Admins = &adminFake{true}
	if worked, err := e.ProcessOne(ctx); !worked || err != nil {
		t.Fatal("role retry failed", worked, err)
	}
	if n := number(t, e, `SELECT count(*) FROM inbox WHERE status='done' AND payload='{}'::jsonb`); n != 1 {
		t.Fatal("ignored queued content not cleared", n)
	}
	if n := number(t, e, `SELECT count(*) FROM ai_jobs`); n != 0 {
		t.Fatal("queued admin message was still reviewed", n)
	}
}

func TestTelegramSenderIdentityControlsAdminIgnore(t *testing.T) {
	e := &Engine{Config: config.Defaults(), Admins: messageRoleCheck(func(context.Context, int64, int64) (bool, error) {
		t.Fatal("sender_chat fake from must not be queried")
		return false, nil
	})}
	e.Config.Moderation.IgnoreAdminMessages = true
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{`{"update_id":1,"message":{"message_id":1,"chat":{"id":-1001111111111,"type":"supergroup"},"sender_chat":{"id":-1001111111111,"type":"supergroup"},"from":{"id":123},"text":"test"}}`, true},
		{`{"update_id":2,"message":{"message_id":2,"chat":{"id":-1001111111111,"type":"supergroup"},"sender_chat":{"id":-1003333333333,"type":"channel"},"from":{"id":123},"text":"test"}}`, false},
	} {
		u, err := telegram.Decode(json.RawMessage(tc.raw))
		if err != nil {
			t.Fatal(err)
		}
		got, err := e.ignoreAdminMessage(context.Background(), *u.Message)
		if err != nil || got != tc.want {
			t.Fatal(got, err)
		}
	}
}
