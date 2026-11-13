package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"sentinel/internal/domain"
	"sentinel/internal/notify"
	"sentinel/internal/storage"
	"sentinel/internal/visual"
)

type mediaSourceFake struct {
	calls     int
	preview   bool
	err       error
	different bool
}

func (s *mediaSourceFake) Fetch(_ context.Context, m domain.Media, _ int64) ([]byte, bool, int, error) {
	s.calls++
	data := "same-image"
	if s.different {
		data = m.UniqueID
	}
	return []byte(data), s.preview, 3, s.err
}

type mediaRenderFake struct{ frames int }

func (r mediaRenderFake) Frames(_ context.Context, data []byte, _ int) ([][]byte, error) {
	n := max(1, r.frames)
	out := [][]byte{}
	for i := 0; i < n; i++ {
		out = append(out, append([]byte(nil), data...))
	}
	return out, nil
}

type mediaClassifierFake struct {
	calls int
	p     float64
	err   error
}

func (c *mediaClassifierFake) Classify(context.Context, []byte) (float64, error) {
	c.calls++
	return c.p, c.err
}

type mediaNotifierFake struct {
	notifierFake
	replyID int64
	text    string
}

func (n *mediaNotifierFake) Reply(ctx context.Context, chat, message int64, text string, kb *models.InlineKeyboardMarkup) (int64, error) {
	n.replyID = message
	n.text = text
	return n.Send(ctx, chat, text, kb)
}
func pmMedia(id int64, unique string) domain.Message {
	m := msg(id, 111, "")
	m.ChatID = 111
	m.ChatType = "private"
	m.ContentType = "sticker"
	m.Media = []domain.Media{{Kind: "sticker", FileID: "file-" + unique, UniqueID: unique, Format: "image"}}
	return m
}
func mediaEngine(t *testing.T) *Engine {
	e := testEngine(t)
	e.Config.Media.Enabled = true
	e.Config.FalKey = "fixture"
	return e
}
func drainMedia(t *testing.T, e *Engine, s *mediaSourceFake, c *mediaClassifierFake) {
	t.Helper()
	for i := 0; i < 40; i++ {
		worked, err := e.MediaOne(context.Background(), s, mediaRenderFake{frames: 3}, c)
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			return
		}
	}
	t.Fatal("media queue failed to drain")
}
func TestMediaPrivateDedupRestartAndFrameCache(t *testing.T) {
	e := mediaEngine(t)
	ctx := context.Background()
	s := &mediaSourceFake{}
	c := &mediaClassifierFake{p: .92}
	for i := int64(1); i <= 10; i++ {
		m := pmMedia(i, "same-sticker")
		m.Caption = "private caption must not persist"
		deliver(t, e, domain.Update{ID: i, Message: &m})
	}
	if n := number(t, e, `SELECT count(*) FROM visual_checks`); n != 1 {
		t.Fatal("same sticker enqueued repeatedly", n)
	}
	if n := number(t, e, `SELECT count(*) FROM visual_requests WHERE payload->>'caption'<>''`); n != 0 {
		t.Fatal("private caption persisted")
	}
	e = mustEngine(t, e.Store, e.Config, e.Admins, e.BotID, e.Username)
	drainMedia(t, e, s, c)
	if s.calls != 1 || c.calls != 1 {
		t.Fatal("paid duplicate frames or stickers", s.calls, c.calls)
	}
	m := pmMedia(11, "same-sticker")
	deliver(t, e, domain.Update{ID: 11, Message: &m})
	drainMedia(t, e, s, c)
	m = pmMedia(12, "different-id-same-pixels")
	deliver(t, e, domain.Update{ID: 12, Message: &m})
	drainMedia(t, e, s, c)
	if s.calls != 2 || c.calls != 1 {
		t.Fatal("durable/file-content cache missed", s.calls, c.calls)
	}
	n := &mediaNotifierFake{}
	if _, err := e.DeliverOne(ctx, n); err != nil {
		t.Fatal(err)
	}
	if n.replyID != 1 || !strings.Contains(n.text, "Высокий риск NSFW") {
		t.Fatal("no minimal media reply")
	}
	if n := number(t, e, `SELECT count(*) FROM ai_jobs`); n != 0 {
		t.Fatal("PM media created text review")
	}
	if n := number(t, e, `SELECT count(*) FROM visual_requests WHERE status='pending' OR payload<>'{}'::jsonb`); n != 0 {
		t.Fatal("completed metadata retained")
	}
}
func TestMediaGroupCasesAndTextMandatoryIndependent(t *testing.T) {
	for _, visualFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(visualFirst), func(t *testing.T) {
			e := mediaEngine(t)
			ctx := context.Background()
			s := &mediaSourceFake{}
			c := &mediaClassifierFake{p: .9}
			e.Config.Media.DailyRequestLimit = 200
			e.Config.AI.OptionalDailyRequestLimit = 0
			m := msg(1, 123, "Привет!")
			m.Media = pmMedia(1, "group").Media
			deliver(t, e, domain.Update{ID: 1, Message: &m})
			if n := number(t, e, `SELECT count(*) FROM ai_jobs WHERE priority='mandatory'`); n != 1 {
				t.Fatal("MUST review lost", n)
			}
			if visualFirst {
				drainMedia(t, e, s, c)
			}
			if _, err := e.ReviewOne(ctx, providerFake{}); err != nil {
				t.Fatal(err)
			}
			if !visualFirst {
				drainMedia(t, e, s, c)
			}
			var raw []byte
			if err := e.Store.Pool.QueryRow(ctx, `SELECT payload FROM incidents LIMIT 1`).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var card notify.Card
			if err := json.Unmarshal(raw, &card); err != nil {
				t.Fatal(err)
			}
			if card.Visual == nil || card.Visual.Verdict != "nsfw" || card.AI == nil {
				t.Fatal("reviews overwrote one another")
			}
			if n := number(t, e, `SELECT count(*) FROM moderation_events`); n != 0 {
				t.Fatal("visual result claimed sanctions")
			}
		})
	}
}
func TestMediaSafeResultUpdatesExistingCase(t *testing.T) {
	e := mediaEngine(t)
	m := msg(1, 123, "hello")
	m.Media = pmMedia(1, "safe").Media
	deliver(t, e, domain.Update{ID: 1, Message: &m})
	if _, err := e.ReviewOne(context.Background(), providerFake{}); err != nil {
		t.Fatal(err)
	}
	drainMedia(t, e, &mediaSourceFake{}, &mediaClassifierFake{p: .01})
	if n := number(t, e, `SELECT count(*) FROM incidents WHERE payload->'visual'->>'verdict'='safe'`); n != 1 {
		t.Fatal("safe result not attached", n)
	}
}
func TestMediaConsentRevocationEditsAndAdminIgnore(t *testing.T) {
	for _, scenario := range []string{"unknown", "revoked", "admin", "edited"} {
		t.Run(scenario, func(t *testing.T) {
			e := mediaEngine(t)
			m := msg(1, 123, "")
			m.Media = pmMedia(1, "stale").Media
			if scenario == "unknown" {
				m.ChatID = testChat - 100
			}
			deliver(t, e, domain.Update{ID: 1, Message: &m})
			switch scenario {
			case "revoked":
				if _, err := e.Store.Pool.Exec(context.Background(), `INSERT INTO managed_chats(chat_id,active,source) VALUES($1,false,'configured')`, testChat); err != nil {
					t.Fatal(err)
				}
			case "admin":
				e.Config.Moderation.IgnoreAdminMessages = true
			case "edited":
				m.Text = "media removed"
				m.Media = nil
				edited := time.Now().UTC().Add(time.Second)
				m.EditedAt = &edited
				deliver(t, e, domain.Update{ID: 2, Message: &m})
			}
			s := &mediaSourceFake{}
			c := &mediaClassifierFake{p: .9}
			drainMedia(t, e, s, c)
			if s.calls != 0 || c.calls != 0 {
				t.Fatal("checked unauthorized/stale media")
			}
			if n := number(t, e, `SELECT count(*) FROM visual_requests WHERE status='pending'`); n != 0 {
				t.Fatal("revoked request retained")
			}
		})
	}
	e := mediaEngine(t)
	m := pmMedia(1, "unauthorized")
	user := int64(222)
	m.ChatID = user
	m.UserID = &user
	deliver(t, e, domain.Update{ID: 1, Message: &m})
	if n := number(t, e, `SELECT count(*) FROM visual_requests`); n != 0 {
		t.Fatal("unallowlisted PM consumed paid credits")
	}
}
func TestMediaRetryBudgetUnknownAndLeaseFencing(t *testing.T) {
	t.Run("retry", func(t *testing.T) {
		e := mediaEngine(t)
		ctx := context.Background()
		m := pmMedia(1, "retry")
		deliver(t, e, domain.Update{ID: 1, Message: &m})
		s := &mediaSourceFake{}
		c := &mediaClassifierFake{err: &visual.Error{Code: "fal_http_429", RetryAfter: time.Minute}}
		drainMedia(t, e, s, c)
		if n := number(t, e, `SELECT count(*) FROM visual_checks WHERE status='pending' AND available_at>now()+interval '50 seconds'`); n != 1 {
			t.Fatal("429 not retried", n)
		}
		if n := number(t, e, `SELECT count(*) FROM visual_requests WHERE status='pending'`); n != 1 {
			t.Fatal("request dropped")
		}
		if _, err := e.Store.Pool.Exec(ctx, `UPDATE visual_checks SET available_at=now()`); err != nil {
			t.Fatal(err)
		}
		c.err = nil
		c.p = .01
		drainMedia(t, e, s, c)
		if n := number(t, e, `SELECT count(*) FROM visual_requests WHERE result->>'verdict'='safe'`); n != 1 {
			t.Fatal("retry not finished")
		}
	})
	t.Run("budget", func(t *testing.T) {
		e := mediaEngine(t)
		e.Config.Media.DailyRequestLimit = 0
		m := pmMedia(1, "budget")
		deliver(t, e, domain.Update{ID: 1, Message: &m})
		s := &mediaSourceFake{}
		c := &mediaClassifierFake{}
		drainMedia(t, e, s, c)
		if c.calls != 0 || number(t, e, `SELECT count(*) FROM visual_requests WHERE status='pending'`) != 1 {
			t.Fatal("budget bypass or silent drop")
		}
		if n := number(t, e, `SELECT count(*) FROM outbox WHERE idempotency_key LIKE 'media-deferred:%'`); n != 1 {
			t.Fatal("no private budget notice", n)
		}
		if err := e.deferVisualNotice(context.Background(), storage.VisualKey(m.Media[0])); err != nil {
			t.Fatal(err)
		}
		if n := number(t, e, `SELECT count(*) FROM outbox WHERE idempotency_key LIKE 'media-deferred:%'`); n != 1 {
			t.Fatal("duplicate notice")
		}
	})
	t.Run("unavailable", func(t *testing.T) {
		e := mediaEngine(t)
		m := pmMedia(1, "missing")
		deliver(t, e, domain.Update{ID: 1, Message: &m})
		drainMedia(t, e, &mediaSourceFake{err: &visual.Error{Code: "animated_preview_missing", Permanent: true}}, &mediaClassifierFake{})
		if n := number(t, e, `SELECT count(*) FROM visual_requests WHERE result->>'unavailable'='true' AND result->>'verdict'='review'`); n != 1 {
			t.Fatal("unknown interpreted as safe")
		}
		if n := number(t, e, `SELECT count(*) FROM visual_checks WHERE last_error='animated_preview_missing' AND result->>'error_code'='animated_preview_missing'`); n != 1 {
			t.Fatal("safe diagnostic code lost")
		}
	})
	t.Run("fence", func(t *testing.T) {
		e := mediaEngine(t)
		ctx := context.Background()
		m := pmMedia(1, "lease")
		deliver(t, e, domain.Update{ID: 1, Message: &m})
		old, err := e.Store.ClaimVisual(ctx)
		if err != nil || old == nil {
			t.Fatal(err)
		}
		if _, err = e.Store.Pool.Exec(ctx, `UPDATE visual_checks SET lease_until=now()-interval '1 minute'`); err != nil {
			t.Fatal(err)
		}
		newer, err := e.Store.ClaimVisual(ctx)
		if err != nil || newer == nil {
			t.Fatal(err)
		}
		if err = e.Store.FinishVisual(ctx, old, domain.MediaReview{}, 30); err == nil {
			t.Fatal("stale lease committed")
		}
		if err = e.Store.FinishVisual(ctx, newer, domain.MediaReview{Probability: .1}, 30); err != nil {
			t.Fatal(err)
		}
	})
}
func TestMediaConcurrentClaimsAndBudget(t *testing.T) {
	e := mediaEngine(t)
	ctx := context.Background()
	m := pmMedia(1, "claim")
	deliver(t, e, domain.Update{ID: 1, Message: &m})
	var wg sync.WaitGroup
	jobs := make(chan *storage.VisualJob, 10)
	errs := make(chan error, 20)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j, err := e.Store.ClaimVisual(ctx)
			if err != nil {
				errs <- err
			}
			if j != nil {
				jobs <- j
			}
		}()
	}
	wg.Wait()
	close(jobs)
	if len(jobs) != 1 {
		t.Fatal("duplicate shared job", len(jobs))
	}
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.Store.ReserveVisual(ctx, 3)
			if err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if n := number(t, e, `SELECT requests FROM visual_budget`); n != 3 {
		t.Fatal("daily budget race", n)
	}
}
func TestMediaRetentionAndPrivateRateLimit(t *testing.T) {
	e := mediaEngine(t)
	ctx := context.Background()
	m := pmMedia(1, "expire")
	deliver(t, e, domain.Update{ID: 1, Message: &m})
	if _, err := e.Store.Pool.Exec(ctx, `UPDATE visual_requests SET created_at=now()-interval '8 days'`); err != nil {
		t.Fatal(err)
	}
	if err := e.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if n := number(t, e, `SELECT count(*) FROM visual_requests`); n != 0 {
		t.Fatal("pending media exceeded retention")
	}
	if n := number(t, e, `SELECT count(*) FROM visual_checks`); n != 0 {
		t.Fatal("orphan file references retained")
	}
	for i := int64(2); i <= 35; i++ {
		m = pmMedia(i, fmt.Sprint(i))
		deliver(t, e, domain.Update{ID: i, Message: &m})
	}
	if n := number(t, e, `SELECT count(*) FROM visual_requests`); n != 30 {
		t.Fatal("private rate limit missing", n)
	}
	if n := number(t, e, `SELECT count(*) FROM outbox WHERE idempotency_key LIKE 'media-limit:%'`); n != 1 {
		t.Fatal("rate notices spammed", n)
	}
}
