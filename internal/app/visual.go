package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"sentinel/internal/domain"
	"sentinel/internal/notify"
	"sentinel/internal/storage"
	"sentinel/internal/visual"
)

const (
	mediaRequestsPerMinute = 30
	mediaPendingPerChat    = 200
	mediaMaxAttempts       = 8
	mediaEligibilityBatch  = 100
)

type MediaSource interface {
	Fetch(context.Context, domain.Media, int64) ([]byte, bool, int, error)
}

func (e *Engine) privateMedia(m domain.Message) bool {
	return e.Config.Media.Enabled && e.Config.Media.PrivateReplies && m.ChatType == "private" && m.UserID != nil && m.ChatID == *m.UserID && e.allowed(*m.UserID) && len(m.Media) > 0
}

func (e *Engine) enqueueVisual(ctx context.Context, tx pgx.Tx, m domain.Message) error {
	if !e.Config.Media.Enabled {
		return nil
	}
	if m.ContentHash == "" {
		m.ContentHash = fingerprint(m)
	}
	// An edit invalidates older work, including an edit which removes all media.
	if _, err := tx.Exec(ctx, `UPDATE visual_requests SET status='done',payload='{}' WHERE chat_id=$1 AND message_id=$2 AND edit_key<>$3 AND status='pending'`, m.ChatID, m.ID, m.ContentHash); err != nil {
		return err
	}
	if len(m.Media) == 0 {
		return nil
	}
	var recent, pending int
	if err := tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE created_at>now()-interval '1 minute'),count(*) FILTER(WHERE status='pending') FROM visual_requests WHERE chat_id=$1`, m.ChatID).Scan(&recent, &pending); err != nil {
		return err
	}
	if recent >= mediaRequestsPerMinute || pending >= mediaPendingPerChat {
		if m.ChatID > 0 {
			return e.text(ctx, tx, fmt.Sprintf("media-limit:%d:%s", m.ChatID, time.Now().UTC().Format("200601021504")), notify.Delivery{ChatID: m.ChatID, Private: true, OperatorOnly: true, ReplyToMessageID: m.ID, Text: "⏳ <b>Слишком много медиа</b>\nПовторите проверку через минуту."}, "media_result", "")
		}
		return storage.Metric(ctx, tx, "sentinel_media_rate_limited_total", 1)
	}
	if m.ContentHash == "" {
		m.ContentHash = fingerprint(m)
	}
	return storage.EnqueueVisual(ctx, tx, m, e.Config.Media.CacheDays)
}

func (e *Engine) visualAllowed(ctx context.Context, m domain.Message) (bool, error) {
	if !e.Config.Media.Enabled {
		return false, nil
	}
	if m.ChatID > 0 {
		return e.privateMedia(m), nil
	}
	active, err := e.observeMessage(ctx, e.Store.Pool, m)
	if err != nil || !active {
		return false, err
	}
	var current bool
	if err := e.Store.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM message_keys WHERE chat_id=$1 AND message_id=$2 AND edit_version=$3)`, m.ChatID, m.ID, m.Version).Scan(&current); err != nil || !current {
		return false, err
	}
	ignored, err := e.ignoreAdminMessage(ctx, m)
	return !ignored, err
}

// MediaOne is a separate bounded worker: it never delays Telegram ingestion or text MUST jobs.
func (e *Engine) MediaOne(ctx context.Context, source MediaSource, renderer visual.Renderer, classifier visual.Classifier) (bool, error) {
	if !e.Config.Media.Enabled {
		return false, nil
	}
	if worked, err := e.finishVisualRequest(ctx); worked || err != nil {
		return worked, err
	}
	job, err := e.Store.ClaimVisual(ctx)
	if err != nil || job == nil {
		return false, err
	}
	fail := func(cause error) (bool, error) {
		code := "media_processing_failed"
		delay := min(30*time.Minute, time.Second*time.Duration(1<<min(10, job.Attempts)))
		var failure *visual.Error
		if errors.As(cause, &failure) {
			code = failure.Code
			delay = max(delay, failure.RetryAfter)
		}
		if failure != nil && failure.Permanent || job.Attempts >= mediaMaxAttempts {
			return true, e.Store.FinishVisual(ctx, job, domain.MediaReview{Unavailable: true, Model: visual.Model}, e.Config.Media.CacheDays)
		}
		return true, e.Store.RetryVisual(ctx, job, code, delay)
	}
	// Consent/roles may have changed after enqueue. Never fetch a file merely because it once had a waiter.
	rows, err := e.Store.Pool.Query(ctx, `SELECT id,payload FROM visual_requests WHERE status='pending' AND $1=ANY(check_keys) ORDER BY id LIMIT $2`, job.Key, mediaEligibilityBatch)
	if err != nil {
		return fail(err)
	}
	type waiter struct {
		id int64
		m  domain.Message
	}
	waiters := []waiter{}
	for rows.Next() {
		var w waiter
		var raw []byte
		if err = rows.Scan(&w.id, &raw); err != nil {
			rows.Close()
			return fail(err)
		}
		if json.Unmarshal(raw, &w.m) != nil {
			rows.Close()
			return fail(fmt.Errorf("invalid media waiter"))
		}
		waiters = append(waiters, w)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return fail(err)
	}
	eligible := false
	for _, w := range waiters {
		allowed, err := e.visualAllowed(ctx, w.m)
		if err != nil {
			return fail(err)
		}
		if allowed {
			eligible = true
			break
		}
		if _, err = e.Store.Pool.Exec(ctx, `UPDATE visual_requests SET status='done',payload='{}' WHERE id=$1`, w.id); err != nil {
			return fail(err)
		}
	}
	if !eligible {
		if len(waiters) == mediaEligibilityBatch {
			return true, e.Store.RetryVisual(ctx, job, "waiting_for_eligible_request", time.Second)
		}
		_, err = e.Store.Pool.Exec(ctx, `DELETE FROM visual_checks WHERE cache_key=$1 AND lease_token=$2 AND NOT EXISTS(SELECT 1 FROM visual_requests WHERE status='pending' AND $1=ANY(check_keys))`, job.Key, job.Token)
		return true, err
	}
	data, preview, duration, err := source.Fetch(ctx, job.Media, e.Config.Media.MaxFileBytes)
	if err != nil {
		return fail(err)
	}
	frames, err := renderer.Frames(ctx, data, duration)
	if err != nil {
		return fail(err)
	}
	if len(frames) == 0 || len(frames) > visual.MaxFrames {
		return fail(&visual.Error{Code: "media_render_invalid", Permanent: true})
	}
	result := domain.MediaReview{Model: visual.Model, Frames: len(frames), Preview: preview}
	for _, frame := range frames {
		digest := fmt.Sprintf("%x", sha256.Sum256(append([]byte(visual.Model+":png-v1:"), frame...)))
		var probability float64
		err = e.Store.Pool.QueryRow(ctx, `SELECT probability FROM visual_frame_cache WHERE digest=$1 AND expires_at>now()`, digest).Scan(&probability)
		if errors.Is(err, pgx.ErrNoRows) {
			reserved, reserveErr := e.Store.ReserveVisual(ctx, e.Config.Media.DailyRequestLimit)
			if reserveErr != nil {
				return fail(reserveErr)
			}
			if !reserved {
				if err := e.deferVisualNotice(ctx, job.Key); err != nil {
					return fail(err)
				}
				delay := time.Until(time.Now().UTC().Truncate(24 * time.Hour).Add(24 * time.Hour))
				return true, e.Store.RetryVisual(ctx, job, "media_daily_budget", delay)
			}
			call, cancel := context.WithTimeout(ctx, time.Duration(e.Config.Media.TimeoutSeconds)*time.Second)
			probability, err = classifier.Classify(call, frame)
			cancel()
			if err != nil {
				return fail(err)
			}
			if math.IsNaN(probability) || math.IsInf(probability, 0) || probability < 0 || probability > 1 {
				return fail(&visual.Error{Code: "fal_invalid_result"})
			}
			_, err = e.Store.Pool.Exec(ctx, `INSERT INTO visual_frame_cache(digest,probability,expires_at) VALUES($1,$2,now()+$3::interval) ON CONFLICT(digest) DO UPDATE SET probability=EXCLUDED.probability,expires_at=EXCLUDED.expires_at`, digest, probability, fmt.Sprintf("%d days", e.Config.Media.CacheDays))
		}
		if err != nil {
			return fail(err)
		}
		result.Probability = max(result.Probability, probability)
	}
	return true, e.Store.FinishVisual(ctx, job, result, e.Config.Media.CacheDays)
}

func (e *Engine) finishVisualRequest(ctx context.Context) (bool, error) {
	tx, err := e.Store.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var id int64
	var raw []byte
	var keys []string
	err = tx.QueryRow(ctx, `SELECT id,payload,check_keys FROM visual_requests r WHERE status='pending' AND NOT EXISTS(SELECT 1 FROM unnest(r.check_keys) k LEFT JOIN visual_checks c ON c.cache_key=k WHERE c.status IS DISTINCT FROM 'done') ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&id, &raw, &keys)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var m domain.Message
	if json.Unmarshal(raw, &m) != nil {
		return true, fmt.Errorf("invalid media request")
	}
	allowed, err := e.visualAllowed(ctx, m)
	if err != nil {
		return true, err
	}
	if allowed {
		result := domain.MediaReview{Model: visual.Model, Partial: m.MediaPartial}
		for _, key := range keys {
			var b []byte
			var r domain.MediaReview
			if err = tx.QueryRow(ctx, `SELECT result FROM visual_checks WHERE cache_key=$1`, key).Scan(&b); err != nil {
				return true, err
			}
			if json.Unmarshal(b, &r) != nil {
				return true, fmt.Errorf("invalid visual result")
			}
			result.Probability = max(result.Probability, r.Probability)
			result.Frames += r.Frames
			result.Preview = result.Preview || r.Preview
			result.Unavailable = result.Unavailable || r.Unavailable
		}
		result.Partial = result.Partial || result.Unavailable
		result.Verdict = "safe"
		if result.Probability >= e.Config.Media.NSFWThreshold {
			result.Verdict = "nsfw"
		} else if result.Probability >= e.Config.Media.ReviewThreshold || result.Partial {
			result.Verdict = "review"
		}
		if _, err = tx.Exec(ctx, `UPDATE visual_requests SET result=$2 WHERE id=$1`, id, result); err != nil {
			return true, err
		}
		if m.ChatID > 0 {
			err = e.text(ctx, tx, fmt.Sprintf("media-result:%d", id), notify.Delivery{ChatID: m.ChatID, Private: true, OperatorOnly: true, ReplyToMessageID: m.ID, MediaRequestID: id, Text: notify.RenderMedia(result)}, "media_result", "")
		} else if result.Probability >= e.Config.Media.ReviewThreshold || result.Partial && !result.Unavailable {
			var current int
			err = tx.QueryRow(ctx, `SELECT edit_version FROM message_keys WHERE chat_id=$1 AND message_id=$2`, m.ChatID, m.ID).Scan(&current)
			if errors.Is(err, pgx.ErrNoRows) {
				err = nil
			} else if err == nil && current == m.Version {
				var assessment domain.Assessment
				var b []byte
				err = tx.QueryRow(ctx, `SELECT payload FROM risk_assessments WHERE chat_id=$1 AND message_id=$2 AND edit_version=$3 ORDER BY id DESC LIMIT 1`, m.ChatID, m.ID, m.Version).Scan(&b)
				if err == nil {
					err = json.Unmarshal(b, &assessment)
				}
				if err == nil {
					var caseID int64
					caseID, err = e.incident(ctx, tx, m, assessment, nil, false)
					if err == nil {
						err = e.queueCard(ctx, tx, caseID)
					}
				}
			}
		} else {
			var caseID int64
			err = tx.QueryRow(ctx, `UPDATE incidents SET payload=jsonb_set(payload,'{visual}',$4::jsonb) WHERE chat_id=$1 AND payload->'message'->>'message_id'=($2::bigint)::text AND payload->'message'->>'edit_version'=($3::int)::text RETURNING id`, m.ChatID, m.ID, m.Version, result).Scan(&caseID)
			if errors.Is(err, pgx.ErrNoRows) {
				err = nil
			} else if err == nil {
				err = e.queueCard(ctx, tx, caseID)
			}
		}
		if err != nil {
			return true, err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE visual_requests SET status='done',payload='{}' WHERE id=$1`, id); err != nil {
		return true, err
	}
	return true, tx.Commit(ctx)
}
