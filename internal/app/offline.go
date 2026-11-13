package app

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"sentinel/internal/domain"
	"sentinel/internal/notify"
	"sentinel/internal/privacy"
	"sentinel/internal/rules"
)

// Sample deliberately excludes Telegram IDs/usernames and all unneeded context. Text still requires privacy review.
type Sample struct {
	Text            string           `json:"text"`
	Caption         string           `json:"caption,omitempty"`
	QuoteText       string           `json:"quote_text,omitempty"`
	Mentions        []domain.Mention `json:"mentions,omitempty"`
	RecentSimilar   int              `json:"recent_similar,omitempty"`
	RecentLinks     int              `json:"recent_links,omitempty"`
	FeaturesVersion int              `json:"features_version,omitempty"`
	FeaturesPartial bool             `json:"features_partial,omitempty"`
	URLs            []domain.URL     `json:"urls"`
	URLsPartial     bool             `json:"urls_partial,omitempty"`
	First           bool             `json:"first"`
	Quoted          bool             `json:"quoted"`
	Forwarded       bool             `json:"forwarded"`
	Trust           int              `json:"trust"`
	Label           string           `json:"label"`
	Group           string           `json:"campaign_group"`
	Split           string           `json:"split"`
	Day             string           `json:"day"`
}

func (e *Engine) Export(ctx context.Context, w io.Writer) error {
	rows, err := e.Store.Pool.Query(ctx, `SELECT DISTINCT ON(f.incident_id) f.payload->'snapshot',f.label,f.created_at FROM feedback_events f JOIN incidents i ON i.id=f.incident_id WHERE f.label IN('benign','spam','false_positive') AND f.payload ? 'snapshot' AND i.status<>'needs_review' AND COALESCE(i.payload->>'conflict','false')<>'true' ORDER BY f.incident_id,f.created_at DESC,f.id DESC LIMIT 10000`)
	if err != nil {
		return err
	}
	defer rows.Close()
	samples := []Sample{}
	latest := map[string]time.Time{}
	cutoff := time.Now().UTC().AddDate(0, 0, -7)
	for rows.Next() {
		var raw []byte
		var label string
		var date time.Time
		if err = rows.Scan(&raw, &label, &date); err != nil {
			return err
		}
		var c notify.Card
		if err = json.Unmarshal(raw, &c); err != nil {
			return err
		}
		domains := []string{}
		urls := append([]domain.URL(nil), c.Assessment.URLs...)
		partialFeatures := c.Message.FeaturesPartial || c.Message.URLsPartial || len(c.Message.Mentions) > 0
		for i := range urls {
			partialFeatures = partialFeatures || urls[i].TelegramUsername != ""
			urls[i].TelegramUsername = ""
			domains = append(domains, urls[i].Domain)
			urls[i].Value = urls[i].Host
			urls[i].Source = "export"
		}
		sort.Strings(domains)
		group := strings.Join(domains, ",")
		if group == "" {
			group = strings.ToLower(c.Message.Content())
		}
		hash := sha256.Sum256([]byte(group))
		key := fmt.Sprintf("%x", hash[:8])
		if date.After(latest[key]) {
			latest[key] = date
		}
		first := false
		for _, s := range c.Assessment.Signals {
			first = first || s.Code == "FIRST_OBSERVED"
		}
		samples = append(samples, Sample{Text: privacy.Redact(c.Message.Text), Caption: privacy.Redact(c.Message.Caption), QuoteText: privacy.Redact(c.Message.QuoteText), RecentSimilar: c.Message.RecentSimilar, RecentLinks: c.Message.RecentLinks, FeaturesVersion: c.Message.FeaturesVersion, FeaturesPartial: partialFeatures, URLs: urls, URLsPartial: c.Message.URLsPartial, First: first, Quoted: c.Message.Quoted, Forwarded: c.Message.Forwarded, Trust: c.Assessment.Trust, Label: label, Group: key, Day: date.UTC().Format("2006-01-02")})
	}
	if err = rows.Err(); err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	for _, s := range samples {
		s.Split = "train"
		if latest[s.Group].After(cutoff) {
			s.Split = "holdout"
		}
		if err = enc.Encode(s); err != nil {
			return err
		}
	}
	return nil
}

type ReplayResult struct {
	Samples, TP, FP, TN, FN, AIRequired int
	Precision, Recall                   *float64
	IncompleteFeatures                  int
}

func Replay(r io.Reader, w io.Writer, c rules.Config) error {
	analyzer, err := rules.NewAnalyzer(c, rules.Options{})
	if err != nil {
		return fmt.Errorf("initialize replay analysis: invalid settings")
	}
	return ReplayWithAnalyzer(r, w, analyzer, c.AIThreshold)
}

// ReplayWithAnalyzer uses the same immutable policy as live ingestion; it never calls AI.
// IncompleteFeatures prevents older or anonymized samples being mistaken for full extractor coverage.
func ReplayWithAnalyzer(r io.Reader, w io.Writer, analyzer *rules.Analyzer, threshold int) error {
	scan := bufio.NewScanner(r)
	scan.Buffer(make([]byte, 65536), 256<<10)
	result := ReplayResult{}
	for scan.Scan() {
		var s Sample
		if err := json.Unmarshal(scan.Bytes(), &s); err != nil {
			return fmt.Errorf("decode dataset line %d: %w", result.Samples+1, err)
		}
		id := int64(1)
		m := domain.Message{Text: s.Text, Caption: s.Caption, QuoteText: s.QuoteText, Quoted: s.Quoted, Forwarded: s.Forwarded, UserID: &id, Mentions: s.Mentions, URLsPartial: s.URLsPartial, RecentSimilar: s.RecentSimilar, RecentLinks: s.RecentLinks, FeaturesVersion: s.FeaturesVersion, FeaturesPartial: s.FeaturesPartial || s.URLsPartial || s.FeaturesVersion != domain.CurrentFeaturesVersion}
		a := analyzer.Assess(m, s.URLs, s.First, s.Trust)
		result.Samples++
		if m.FeaturesPartial {
			result.IncompleteFeatures++
		}
		if a.ReviewRequired {
			result.AIRequired++
		}
		predicted := a.Score >= threshold
		positive := s.Label == "spam"
		switch {
		case positive && predicted:
			result.TP++
		case positive:
			result.FN++
		case predicted:
			result.FP++
		default:
			result.TN++
		}
	}
	if err := scan.Err(); err != nil {
		return err
	}
	if result.TP+result.FP > 0 {
		v := float64(result.TP) / float64(result.TP+result.FP)
		result.Precision = &v
	}
	if result.TP+result.FN > 0 {
		v := float64(result.TP) / float64(result.TP+result.FN)
		result.Recall = &v
	}
	return json.NewEncoder(w).Encode(result)
}
func (e *Engine) Forget(ctx context.Context, chat, user int64) error {
	if chat >= 0 || user <= 0 {
		return fmt.Errorf("managed chat and positive user ID required")
	}
	var known bool
	if err := e.Store.Pool.QueryRow(ctx, `SELECT $2::boolean OR EXISTS(SELECT 1 FROM managed_chats WHERE chat_id=$1 AND approved_at IS NOT NULL)`, chat, e.configuredChat(chat)).Scan(&known); err != nil {
		return err
	}
	if !known {
		return fmt.Errorf("configured or previously approved chat required")
	}
	tx, err := e.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var held bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM incidents WHERE chat_id=$1 AND user_id=$2 AND retain_until>now()) OR EXISTS(SELECT 1 FROM ai_jobs WHERE chat_id=$1 AND (payload->'message'->>'user_id')::bigint=$2 AND status IN('pending','processing')) OR EXISTS(SELECT 1 FROM inbox WHERE status<>'done' AND payload->'message'->>'chat_id'=$1::text AND payload->'message'->>'user_id'=$2::text)`, chat, user).Scan(&held); err != nil {
		return err
	}
	if held {
		return fmt.Errorf("retained case or pending job exists: operator review required, mandatory evidence was not deleted")
	}
	queries := []string{
		`DELETE FROM ai_reviews WHERE job_id IN(SELECT id FROM ai_jobs WHERE chat_id=$1 AND (payload->'message'->>'user_id')::bigint=$2)`,
		`DELETE FROM ai_jobs WHERE chat_id=$1 AND (payload->'message'->>'user_id')::bigint=$2`,
		`DELETE FROM risk_assessments WHERE chat_id=$1 AND message_id IN(SELECT message_id FROM message_keys WHERE chat_id=$1 AND user_id=$2)`,
		`DELETE FROM message_keys WHERE chat_id=$1 AND user_id=$2`,
		`DELETE FROM message_journal WHERE chat_id=$1 AND user_id=$2`,
		`DELETE FROM user_reports WHERE chat_id=$1 AND (reporter_id=$2 OR reported_user_id=$2)`,
		`DELETE FROM daily_activity WHERE chat_id=$1 AND user_id=$2`,
		`DELETE FROM chat_user_stats WHERE chat_id=$1 AND user_id=$2`,
	}
	for _, q := range queries {
		if _, err = tx.Exec(ctx, q, chat, user); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE users SET username='' WHERE user_id=$1 AND NOT EXISTS(SELECT 1 FROM chat_user_stats WHERE user_id=$1)`, user); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
