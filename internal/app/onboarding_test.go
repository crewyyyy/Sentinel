package app

import (
	"context"
	"fmt"
	"testing"
	"time"

	"sentinel/internal/domain"
)

const discoveredChat int64 = testChat - 99

type connectionAdmin struct {
	owner, bot  bool
	unavailable bool
}

func (a connectionAdmin) IsAdmin(_ context.Context, chat, user int64) (bool, error) {
	if a.unavailable {
		return false, fmt.Errorf("fixture role API unavailable")
	}
	if user == 999 {
		return a.bot, nil
	}
	return a.owner, nil
}
func discoveryEngine(t *testing.T) *Engine {
	e := testEngine(t)
	e.BotID = "999"
	e.Config.Telegram.ChatIDs = nil
	e.Admins = connectionAdmin{owner: true, bot: true}
	return e
}
func selfUpdate(update, actor int64, at time.Time, status string) domain.Update {
	return domain.Update{ID: update, Membership: &domain.Membership{ChatID: discoveredChat, ChatType: "supergroup", ChatTitle: "Fixture group", ActorID: actor, Date: at, Self: true, Old: domain.MemberState{UserID: 999, Status: "left"}, New: domain.MemberState{UserID: 999, Status: status}}}
}
func clickConnection(t *testing.T, e *Engine, update, request, revision, user, chat, receipt int64, action string) {
	t.Helper()
	deliver(t, e, domain.Update{ID: update, Callback: &domain.Callback{ID: fmt.Sprintf("join-%d", update), UserID: user, ChatID: chat, MessageID: receipt, Data: fmt.Sprintf("conn:%d:%d:%s", request, revision, action)}})
}
func TestChatDiscoveryConsentRestartRemovalAndRejoin(t *testing.T) {
	e := discoveryEngine(t)
	ctx := context.Background()
	n := &notifierFake{}
	for i, command := range []string{"/start", "/status", "/model"} {
		m := msg(int64(i+1), 111, command)
		m.ChatID = 111
		deliver(t, e, domain.Update{ID: m.ID, Message: &m})
	}
	drainModelOutbox(t, e, n)
	m := msg(100, 123, "before consent https://fixture.example")
	m.ChatID = discoveredChat
	deliver(t, e, domain.Update{ID: 4, Message: &m})
	if n := number(t, e, `SELECT count(*) FROM message_journal`); n != 0 {
		t.Fatal("collected before discovery", n)
	}
	at := time.Now().UTC()
	deliver(t, e, selfUpdate(5, 111, at, "administrator"))
	request := number(t, e, `SELECT id FROM chat_connection_requests`)
	drainModelOutbox(t, e, n)
	m = msg(101, 123, "before approval")
	m.ChatID = discoveredChat
	deliver(t, e, domain.Update{ID: 6, Message: &m})
	if n := number(t, e, `SELECT count(*) FROM ai_jobs`); n != 0 {
		t.Fatal("review before consent", n)
	}
	clickConnection(t, e, 7, request, 1, 111, 111, 77, "yes")
	drainModelOutbox(t, e, n)
	if n := number(t, e, `SELECT count(*) FROM managed_chats WHERE active AND approved_by=111`); n != 1 {
		t.Fatal("approval did not persist", n)
	}
	m = msg(102, 123, "after consent")
	m.ChatID = discoveredChat
	deliver(t, e, domain.Update{ID: 8, Message: &m})
	e = mustEngine(t, e.Store, e.Config, e.Admins, e.BotID, e.Username)
	m = msg(103, 123, "after restart")
	m.ChatID = discoveredChat
	deliver(t, e, domain.Update{ID: 9, Message: &m})
	if n := number(t, e, `SELECT count(*) FROM ai_jobs`); n != 1 {
		t.Fatal("first observed lost across dynamic-chat restart", n)
	}
	if len(e.Config.Telegram.ChatIDs) != 0 {
		t.Fatal("mutated deployment config")
	}
	e.Admins = partialAdmin{denied: discoveredChat}
	if allowed, err := e.modelAdmin(ctx, 111); err != nil || allowed {
		t.Fatal("global model permissions missed dynamic chat", allowed, err)
	}
	e.Admins = connectionAdmin{owner: true, bot: true}
	left := selfUpdate(10, 222, at.Add(time.Second), "left")
	left.Membership.Old.Status = "administrator"
	deliver(t, e, left)
	m = msg(104, 123, "after removal")
	m.ChatID = discoveredChat
	deliver(t, e, domain.Update{ID: 11, Message: &m})
	if n := number(t, e, `SELECT count(*) FROM message_journal`); n != 2 {
		t.Fatal("collected after removal", n)
	}
	clickConnection(t, e, 12, request, 1, 111, 111, 77, "yes")
	if n := number(t, e, `SELECT count(*) FROM managed_chats WHERE active`); n != 0 {
		t.Fatal("stale callback reactivated removed chat")
	}
	deliver(t, e, selfUpdate(13, 111, at, "administrator"))
	if n := number(t, e, `SELECT count(*) FROM chat_connection_requests WHERE status='pending'`); n != 0 {
		t.Fatal("old membership event reopened consent")
	}
	deliver(t, e, selfUpdate(14, 111, at.Add(2*time.Second), "administrator"))
	drainModelOutbox(t, e, n)
	revision := number(t, e, `SELECT revision FROM chat_connection_requests WHERE id=$1`, request)
	if n := number(t, e, `SELECT count(*) FROM managed_chats WHERE active`); n != 0 {
		t.Fatal("rejoin silently reused consent")
	}
	clickConnection(t, e, 15, request, revision, 111, 111, 77, "yes")
	if n := number(t, e, `SELECT count(*) FROM managed_chats WHERE active`); n != 1 {
		t.Fatal("fresh rejoin confirmation failed")
	}
}
func TestUnauthorizedDiscoveryDoesNotCollectOrNotify(t *testing.T) {
	for _, mode := range []string{"outsider", "wrong_bot", "private", "channel", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			e := discoveryEngine(t)
			u := selfUpdate(1, 111, time.Now().UTC(), "administrator")
			switch mode {
			case "outsider":
				u.Membership.ActorID = 222
			case "wrong_bot":
				u.Membership.New.UserID = 123
			case "private":
				u.Membership.ChatID = 111
				u.Membership.ChatType = "private"
			case "channel":
				u.Membership.ChatType = "channel"
			case "disabled":
				e.Config.Telegram.AutoDiscoverChats = false
			}
			deliver(t, e, u)
			for _, table := range []string{"chat_connection_requests", "managed_chats", "outbox", "message_journal", "ai_jobs"} {
				if n := number(t, e, `SELECT count(*) FROM `+table); n != 0 {
					t.Fatal("unauthorized discovery persisted/notified", mode, table, n)
				}
			}
		})
	}
}
func TestConnectionApprovalIsBoundToOwnerReceiptRightsAndExpiry(t *testing.T) {
	for _, mode := range []string{"owner", "dialog", "receipt", "expired", "revoked", "bot_not_admin", "unavailable", "stale", "disabled", "reject"} {
		t.Run(mode, func(t *testing.T) {
			e := discoveryEngine(t)
			deliver(t, e, selfUpdate(1, 111, time.Now().UTC(), "administrator"))
			request := number(t, e, `SELECT id FROM chat_connection_requests`)
			drainModelOutbox(t, e, &notifierFake{})
			user, chat, receipt, revision := int64(111), int64(111), int64(77), int64(1)
			action := "yes"
			switch mode {
			case "owner":
				user = 222
			case "dialog":
				chat = adminChat
			case "receipt":
				receipt = 78
			case "expired":
				if _, err := e.Store.Pool.Exec(context.Background(), `UPDATE chat_connection_requests SET expires_at=now()-interval '1 second'`); err != nil {
					t.Fatal(err)
				}
			case "revoked":
				e.Admins = connectionAdmin{bot: true}
			case "bot_not_admin":
				e.Admins = connectionAdmin{owner: true}
			case "unavailable":
				e.Admins = connectionAdmin{unavailable: true}
			case "stale":
				revision = 2
			case "disabled":
				e.Config.Telegram.AutoDiscoverChats = false
			case "reject":
				action = "no"
			}
			clickConnection(t, e, 2, request, revision, user, chat, receipt, action)
			if n := number(t, e, `SELECT count(*) FROM managed_chats WHERE active`); n != 0 {
				t.Fatal("unsafe activation", mode, n)
			}
			if n := number(t, e, `SELECT count(*) FROM inbox WHERE status<>'done'`); n != 0 {
				t.Fatal("unavailable role API blocked promotion events behind callback", n)
			}
			if mode == "reject" {
				if n := number(t, e, `SELECT count(*) FROM chat_connection_requests WHERE status='rejected'`); n != 1 {
					t.Fatal("rejection not saved")
				}
			}
		})
	}
}
func TestConnectionDeliveryPrivateAndCurrentRights(t *testing.T) {
	for _, mode := range []string{"public", "revoked", "bot_not_admin"} {
		t.Run(mode, func(t *testing.T) {
			e := discoveryEngine(t)
			deliver(t, e, selfUpdate(1, 111, time.Now().UTC(), "administrator"))
			n := &notifierFake{}
			switch mode {
			case "public":
				n.public = true
			case "revoked":
				e.Admins = connectionAdmin{bot: true}
			case "bot_not_admin":
				e.Admins = connectionAdmin{owner: true}
			}
			if _, err := e.DeliverOne(context.Background(), n); err != nil {
				t.Fatal(err)
			}
			if n.sends != 0 {
				t.Fatal("unauthorized connection disclosure", mode)
			}
		})
	}
}
func TestDiscoveryPendingSurvivesRestartAndStartReschedulesDM(t *testing.T) {
	e := discoveryEngine(t)
	deliver(t, e, selfUpdate(1, 111, time.Now().UTC(), "administrator"))
	request := number(t, e, `SELECT id FROM chat_connection_requests`)
	n := &notifierFake{fail: true}
	if _, err := e.DeliverOne(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	e = mustEngine(t, e.Store, e.Config, e.Admins, e.BotID, e.Username)
	m := msg(2, 111, "/start")
	m.ChatID = 111
	deliver(t, e, domain.Update{ID: 2, Message: &m})
	n = &notifierFake{}
	drainModelOutbox(t, e, n)
	if n.sends != 2 {
		t.Fatal("start did not redeliver pending consent and help", n.sends)
	}
	clickConnection(t, e, 3, request, 1, 111, 111, 77, "yes")
	if n := number(t, e, `SELECT count(*) FROM managed_chats WHERE active`); n != 1 {
		t.Fatal("restart lost candidate")
	}
}

type unopenedDialog struct{ notifierFake }

func (*unopenedDialog) PrivateDestination(context.Context, int64) (bool, error) {
	return false, fmt.Errorf("fixture dialog not started")
}
func TestStartReschedulesFailedDestinationLookupWithoutStaleLease(t *testing.T) {
	e := discoveryEngine(t)
	deliver(t, e, selfUpdate(1, 111, time.Now().UTC(), "administrator"))
	if _, err := e.DeliverOne(context.Background(), &unopenedDialog{}); err != nil {
		t.Fatal(err)
	}
	if n := number(t, e, `SELECT count(*) FROM outbox WHERE status='pending' AND lease_until IS NULL AND last_error='destination_unavailable'`); n != 1 {
		t.Fatal("pre-send failure left a stale lease")
	}
	m := msg(2, 111, "/start")
	m.ChatID = 111
	deliver(t, e, domain.Update{ID: 2, Message: &m})
	n := &notifierFake{}
	drainModelOutbox(t, e, n)
	if n.sends != 2 {
		t.Fatal("private consent was not redelivered after start", n.sends)
	}
}
func TestPreConsentBacklogIsNotCollectedAfterActivation(t *testing.T) {
	e := discoveryEngine(t)
	ctx := context.Background()
	deliver(t, e, selfUpdate(1, 111, time.Now().UTC(), "administrator"))
	id := number(t, e, `SELECT id FROM chat_connection_requests`)
	drainModelOutbox(t, e, &notifierFake{})
	clickConnection(t, e, 2, id, 1, 111, 111, 77, "yes")
	m := msg(1, 123, "old backlog")
	m.ChatID = discoveredChat
	m.Date = time.Now().UTC().Add(-time.Hour)
	deliver(t, e, domain.Update{ID: 3, Message: &m})
	if n := number(t, e, `SELECT count(*) FROM message_journal`); n != 0 {
		t.Fatal("queued pre-consent history collected")
	}
	edit := time.Now().UTC()
	m.EditedAt = &edit
	m.Text = "newly observed edit"
	deliver(t, e, domain.Update{ID: 4, Message: &m})
	if n := number(t, e, `SELECT count(*) FROM message_journal`); n != 1 {
		t.Fatal("current edit incorrectly treated as old backlog")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if safe, err := e.Prepare(cancelled, domain.Update{ID: 5, Message: &m}); err == nil || safe.Message != nil {
		t.Fatal("failed consent lookup acknowledged or exposed content")
	}
}

func TestStaticChatRemovalOverridesConfigurationAndAutoFlag(t *testing.T) {
	e := testEngine(t)
	e.Config.Telegram.AutoDiscoverChats = false
	at := time.Now().UTC()
	u := selfUpdate(1, 111, at, "left")
	u.Membership.ChatID = testChat
	deliver(t, e, u)
	m := msg(1, 123, "not retained after removal")
	deliver(t, e, domain.Update{ID: 2, Message: &m})
	if n := number(t, e, `SELECT count(*) FROM message_journal`); n != 0 {
		t.Fatal("configured fallback bypassed inactive consent")
	}
	u = selfUpdate(3, 111, at.Add(time.Second), "administrator")
	u.Membership.ChatID = testChat
	deliver(t, e, u)
	m = msg(2, 123, "configured consent restored")
	deliver(t, e, domain.Update{ID: 4, Message: &m})
	if n := number(t, e, `SELECT count(*) FROM message_journal`); n != 1 {
		t.Fatal("explicit configured chat could not reconnect")
	}
}
