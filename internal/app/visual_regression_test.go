package app

import (
	"context"
	"testing"
	"time"

	"sentinel/internal/domain"
	"sentinel/internal/storage"
)

func TestMediaPartialKnownRiskStillCreatesCase(t *testing.T) {
	e := mediaEngine(t)
	ctx := context.Background()
	m := msg(1, 123, "")
	m.Media = pmMedia(1, "known").Media
	m.Media = append(m.Media, domain.Media{Kind: "sticker", UniqueID: "missing", FileID: "missing"})
	deliver(t, e, domain.Update{ID: 1, Message: &m})
	if _, err := e.Store.Pool.Exec(ctx, `UPDATE visual_checks SET status='done',media='{}',result='{"unavailable":true,"model":"fal-ai/imageutils/nsfw"}' WHERE cache_key=$1`, storage.VisualKey(m.Media[1])); err != nil {
		t.Fatal(err)
	}
	drainMedia(t, e, &mediaSourceFake{}, &mediaClassifierFake{p: .4})
	if n := number(t, e, `SELECT count(*) FROM incidents WHERE payload->'visual'->>'verdict'='review' AND payload->'visual'->>'partial'='true'`); n != 1 {
		t.Fatal("known risk suppressed by unavailable second image")
	}
}
func TestMediaExpiredCachesAndStableVerdict(t *testing.T) {
	e := mediaEngine(t)
	ctx := context.Background()
	m := msg(1, 123, "")
	m.Media = pmMedia(1, "cached").Media
	deliver(t, e, domain.Update{ID: 1, Message: &m})
	s := &mediaSourceFake{}
	c := &mediaClassifierFake{p: .9}
	drainMedia(t, e, s, c)
	e.Config.Media.NSFWThreshold = .99
	if _, err := e.ReviewOne(ctx, providerFake{}); err != nil {
		t.Fatal(err)
	}
	if n := number(t, e, `SELECT count(*) FROM incidents WHERE payload->'visual'->>'verdict'='nsfw'`); n != 1 {
		t.Fatal("historical verdict reinterpreted")
	}
	if _, err := e.Store.Pool.Exec(ctx, `UPDATE visual_checks SET expires_at=now()-interval '1 hour';UPDATE visual_frame_cache SET expires_at=now()-interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	m = pmMedia(2, "cached")
	deliver(t, e, domain.Update{ID: 2, Message: &m})
	drainMedia(t, e, s, c)
	if s.calls != 2 || c.calls != 2 {
		t.Fatal("expired cache used indefinitely")
	}
}
func TestMediaStalePrivateResultIsNotDelivered(t *testing.T) {
	for _, mode := range []string{"edit", "disabled", "revoked"} {
		t.Run(mode, func(t *testing.T) {
			e := mediaEngine(t)
			ctx := context.Background()
			m := pmMedia(1, "old")
			deliver(t, e, domain.Update{ID: 1, Message: &m})
			drainMedia(t, e, &mediaSourceFake{}, &mediaClassifierFake{p: .9})
			switch mode {
			case "edit":
				m.Media = pmMedia(1, "new").Media
				edited := time.Now().UTC().Add(time.Second)
				m.EditedAt = &edited
				deliver(t, e, domain.Update{ID: 2, Message: &m})
			case "disabled":
				e.Config.Media.Enabled = false
			case "revoked":
				e.Config.Telegram.AllowedAdminIDs = nil
			}
			n := &mediaNotifierFake{}
			if _, err := e.DeliverOne(ctx, n); err != nil {
				t.Fatal(err)
			}
			if n.sends != 0 {
				t.Fatal("stale private result delivered")
			}
		})
	}
}
func TestMediaForgetRemovesCompletedRequestAssociation(t *testing.T) {
	e := mediaEngine(t)
	ctx := context.Background()
	m := msg(1, 123, "")
	m.Media = pmMedia(1, "forget").Media
	deliver(t, e, domain.Update{ID: 1, Message: &m})
	drainMedia(t, e, &mediaSourceFake{}, &mediaClassifierFake{p: .01})
	if _, err := e.Store.Pool.Exec(ctx, `UPDATE ai_jobs SET status='done'`); err != nil {
		t.Fatal(err)
	}
	if err := e.Forget(ctx, testChat, 123); err != nil {
		t.Fatal(err)
	}
	if n := number(t, e, `SELECT count(*) FROM visual_requests`); n != 0 {
		t.Fatal("completed media link not forgotten")
	}
	if n := number(t, e, `SELECT count(*) FROM visual_checks WHERE media<>'{}'::jsonb`); n != 0 {
		t.Fatal("file IDs retained")
	}
}
func TestMediaBudgetDoesNotGateFourMandatoryTriggers(t *testing.T) {
	for _, tc := range []struct{ text, code string }{
		{"Привет", "FIRST_OBSERVED"},
		{"https://fixture.external.example", "UNKNOWN_EXTERNAL_DOMAIN"},
		{"https://t.me/sample_bot?start=fixture", "TELEGRAM_BOT_LINK"},
		{"У вас скам, деньги не вернули", "COMPETITOR_PRICE_OR_ACCUSATION"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			e := mediaEngine(t)
			e.Config.Media.DailyRequestLimit = 0
			e.Config.AI.OptionalDailyRequestLimit = 0
			m := msg(1, 123, tc.text)
			m.Media = pmMedia(1, "trigger").Media
			deliver(t, e, domain.Update{ID: 1, Message: &m})
			if n := number(t, e, `SELECT count(*) FROM ai_jobs WHERE priority='mandatory' AND status='pending' AND payload::text LIKE $1`, "%"+tc.code+"%"); n != 1 {
				t.Fatal("mandatory text trigger disabled by media budget")
			}
		})
	}
}
