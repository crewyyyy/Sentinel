// Package app connects the durable inbox, pure rules and asynchronous external boundaries.
package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"sentinel/internal/config"
	"sentinel/internal/domain"
	"sentinel/internal/notify"
	"sentinel/internal/privacy"
	"sentinel/internal/review"
	"sentinel/internal/rules"
	"sentinel/internal/storage"
	"sentinel/internal/trust"
	"sentinel/internal/urlscan"
	"sentinel/prompts"
)

type AdminChecker interface {
	IsAdmin(context.Context, int64, int64) (bool, error)
}
type Engine struct {
	Store           *storage.Store
	Config          *config.Config
	Admins          AdminChecker
	Catalog         review.ModelCatalog
	Scanner         *urlscan.Scanner
	Rules           rules.Config
	BotID, Username string
}

func NewEngine(s *storage.Store, c *config.Config, admins AdminChecker, botID, username string) *Engine {
	r := rules.DefaultConfig()
	r.Version = c.Moderation.RulesetVersion
	r.AIThreshold = c.Moderation.RiskAIThreshold
	r.MaxTrustDiscount = c.Trust.MaximumRiskDiscount
	r.Thresholds = append([]int(nil), c.Moderation.ScoreThresholds...)
	for k, v := range c.Moderation.Weights {
		r.Weights[k] = v
	}
	return &Engine{Store: s, Config: c, Admins: admins, Scanner: urlscan.New(c.Links.AllowDomains, c.Links.WatchDomains, c.Links.DenyDomains, c.Links.KnownBots), Rules: r, BotID: botID, Username: username}
}
func (e *Engine) RegisterRules(ctx context.Context) error {
	snapshot := struct {
		Rules rules.Config
		Links config.Links
		Trust config.Trust
	}{e.Rules, e.Config.Links, e.Config.Trust}
	tx, err := e.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO ruleset_versions(version,rules,model,prompt_version,source) VALUES($1,$2,$3,$4,'operator_configuration') ON CONFLICT DO NOTHING`, e.Rules.Version, snapshot, e.Config.AI.Model, prompts.CurrentVersion); err != nil {
		return err
	}
	var same bool
	if err = tx.QueryRow(ctx, `SELECT rules=$2::jsonb FROM ruleset_versions WHERE version=$1`, e.Rules.Version, snapshot).Scan(&same); err != nil {
		return err
	}
	if !same {
		return fmt.Errorf("ruleset_version already exists with different rules; use a new version")
	}
	return tx.Commit(ctx)
}

// ProcessOne makes all derived changes and inbox completion one atomic commit.
func (e *Engine) ProcessOne(ctx context.Context) (bool, error) {
	tx, err := e.Store.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var id int64
	var raw []byte
	var available time.Time
	err = tx.QueryRow(ctx, `SELECT id,payload,available_at FROM inbox WHERE bot_instance_id=$1 AND status<>'done' ORDER BY created_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`, e.BotID).Scan(&id, &raw, &available)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if available.After(time.Now()) {
		return false, nil
	}
	var u domain.Update
	if err = json.Unmarshal(raw, &u); err != nil {
		return false, fmt.Errorf("decode stored update: %w", err)
	}
	if err = e.process(ctx, tx, u); err != nil {
		if rollbackErr := tx.Rollback(ctx); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			return true, rollbackErr
		}
		_, retryErr := e.Store.Pool.Exec(ctx, `UPDATE inbox SET attempts=attempts+1,last_error='processing_failed',available_at=now()+interval '10 seconds' WHERE bot_instance_id=$1 AND id=$2`, e.BotID, id)
		if retryErr != nil {
			return true, retryErr
		}
		return true, err
	}
	if _, err = tx.Exec(ctx, `UPDATE inbox SET status='done',payload='{}',updated_at=now() WHERE bot_instance_id=$1 AND id=$2`, e.BotID, id); err != nil {
		return true, err
	}
	return true, tx.Commit(ctx)
}
func (e *Engine) process(ctx context.Context, tx pgx.Tx, u domain.Update) error {
	if u.Callback != nil {
		return e.callback(ctx, tx, u)
	}
	if u.Membership != nil {
		if u.Membership.Self {
			return e.selfMembership(ctx, tx, u)
		}
		active, err := e.managed(ctx, tx, u.Membership.ChatID)
		if err != nil {
			return err
		}
		if active {
			return e.membership(ctx, tx, u)
		}
	}
	if m := u.Message; m != nil {
		if e.isAdminDestination(m.ChatID) {
			return e.command(ctx, tx, u)
		}
		active, err := e.observeMessage(ctx, tx, *m)
		if err != nil {
			return err
		}
		if !active {
			return nil
		}
		if _, err := tx.Exec(ctx, `INSERT INTO chats(chat_id,chat_type,username) VALUES($1,$2,$3) ON CONFLICT(chat_id) DO UPDATE SET username=EXCLUDED.username,updated_at=now()`, m.ChatID, m.ChatType, m.ChatUsername); err != nil {
			return err
		}
		saved, a, changed, err := e.message(ctx, tx, *m, false)
		if err != nil {
			return err
		}
		if alias, reason := e.reportCommand(saved.Content()); alias != "" {
			return e.report(ctx, tx, saved, reason)
		}
		if changed && a.Score >= e.Config.Moderation.AlertThreshold {
			_, err = e.incident(ctx, tx, saved, a, nil, false)
			return err
		}
	}
	return nil
}
func fingerprint(m domain.Message) string {
	if m.ContentHash != "" {
		return m.ContentHash
	}
	// Detection flags/config and nil-vs-empty slices are not message edits. The hash is taken before truncation/redaction.
	entities := func(es []domain.Entity) []domain.Entity {
		if len(es) == 0 {
			return nil
		}
		return es
	}
	x := struct {
		Text, Caption, QuoteText, ForwardType    string
		Entities, CaptionEntities, QuoteEntities []domain.Entity
		Reply                                    int64
		Forwarded, Quoted                        bool
	}{m.Text, m.Caption, m.QuoteText, m.ForwardType, entities(m.Entities), entities(m.CaptionEntities), entities(m.QuoteEntities), m.ReplyID, m.Forwarded, m.Quoted}
	b, _ := json.Marshal(x)
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum)
}
func (e *Engine) message(ctx context.Context, tx pgx.Tx, m domain.Message, reported bool) (domain.Message, domain.Assessment, bool, error) {
	if m.Entities == nil {
		m.Entities = []domain.Entity{}
	}
	if m.CaptionEntities == nil {
		m.CaptionEntities = []domain.Entity{}
	}
	if m.URLs == nil {
		m.URLs = []domain.URL{}
	}
	var oldRaw []byte
	err := tx.QueryRow(ctx, `SELECT payload FROM message_journal WHERE chat_id=$1 AND message_id=$2 FOR UPDATE`, m.ChatID, m.ID).Scan(&oldRaw)
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return m, domain.Assessment{}, false, err
	}
	var old domain.Message
	if exists {
		if err = json.Unmarshal(oldRaw, &old); err != nil {
			return m, domain.Assessment{}, false, err
		}
		if reported || fingerprint(m) == fingerprint(old) || (old.EditedAt != nil && (m.EditedAt == nil || !m.EditedAt.After(*old.EditedAt))) {
			var a domain.Assessment
			var b []byte
			if err = tx.QueryRow(ctx, `SELECT payload FROM risk_assessments WHERE chat_id=$1 AND message_id=$2 ORDER BY id DESC LIMIT 1`, m.ChatID, m.ID).Scan(&b); err != nil {
				return old, a, false, err
			}
			err = json.Unmarshal(b, &a)
			return old, a, false, err
		}
		m.Version = old.Version + 1
	} else {
		m.Version = 1
	}
	var knownVersion int
	var knownHash string
	var knownEdited *time.Time
	keyErr := tx.QueryRow(ctx, `SELECT edit_version,fingerprint,last_edited_at FROM message_keys WHERE chat_id=$1 AND message_id=$2 FOR UPDATE`, m.ChatID, m.ID).Scan(&knownVersion, &knownHash, &knownEdited)
	known := keyErr == nil
	if keyErr != nil && !errors.Is(keyErr, pgx.ErrNoRows) {
		return m, domain.Assessment{}, false, keyErr
	}
	if !exists && known {
		m.Version = knownVersion
		if fingerprint(m) == knownHash || (knownEdited != nil && (m.EditedAt == nil || !m.EditedAt.After(*knownEdited))) {
			var raw []byte
			var a domain.Assessment
			err := tx.QueryRow(ctx, `SELECT payload FROM risk_assessments WHERE chat_id=$1 AND message_id=$2 ORDER BY id DESC LIMIT 1`, m.ChatID, m.ID).Scan(&raw)
			if errors.Is(err, pgx.ErrNoRows) {
				a = rules.Assess(m, m.URLs, false, 0, e.Rules)
				err = nil
			} else if err == nil {
				err = json.Unmarshal(raw, &a)
			}
			return m, a, false, err
		}
		m.Version = knownVersion + 1
	}
	first := false
	trustScore := 0
	if m.UserID != nil {
		if _, err = tx.Exec(ctx, `INSERT INTO users(user_id,username) VALUES($1,$2) ON CONFLICT(user_id) DO UPDATE SET username=EXCLUDED.username,updated_at=now()`, *m.UserID, m.Username); err != nil {
			return m, domain.Assessment{}, false, err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO chat_user_stats(chat_id,user_id,first_seen_at,last_seen_at,first_observed_message_id) VALUES($1,$2,now(),now(),$3) ON CONFLICT DO NOTHING`, m.ChatID, *m.UserID, m.ID)
		if err != nil {
			return m, domain.Assessment{}, false, err
		}
		first = tag.RowsAffected() > 0
		var days, history, replies, repliers int
		if err = tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE messages_count>0)::int,COALESCE(sum(admin_replies),0)::int,COALESCE((SELECT count(DISTINCT r) FROM daily_activity d,unnest(d.replier_ids) r WHERE d.chat_id=$1 AND d.user_id=$2 AND day>=(now() AT TIME ZONE 'UTC')::date-$3::int),0)::int FROM daily_activity WHERE chat_id=$1 AND user_id=$2 AND day>=(now() AT TIME ZONE 'UTC')::date-$3::int`, m.ChatID, *m.UserID, e.Config.Trust.StatsWindowDays).Scan(&days, &replies, &repliers); err != nil {
			return m, domain.Assessment{}, false, err
		}
		if err = tx.QueryRow(ctx, `SELECT GREATEST(0,LEAST(365,EXTRACT(day FROM now()-first_seen_at)::int)) FROM chat_user_stats WHERE chat_id=$1 AND user_id=$2`, m.ChatID, *m.UserID).Scan(&history); err != nil {
			return m, domain.Assessment{}, false, err
		}
		replies = min(replies, e.Config.Trust.AdminReplyBonusCap/3)
		trustScore = trust.Score(days, history, replies, repliers)
		if !known && !exists && !reported && m.EditedAt == nil {
			if _, err = tx.Exec(ctx, `INSERT INTO daily_activity(chat_id,user_id,day,messages_count) VALUES($1,$2,$3,1) ON CONFLICT(chat_id,user_id,day) DO UPDATE SET messages_count=daily_activity.messages_count+1`, m.ChatID, *m.UserID, m.Date.Format("2006-01-02")); err != nil {
				return m, domain.Assessment{}, false, err
			}
			if _, err = tx.Exec(ctx, `UPDATE chat_user_stats SET messages_count=messages_count+1,last_seen_at=GREATEST(last_seen_at,$3),trust=$4,trust_version=$5 WHERE chat_id=$1 AND user_id=$2`, m.ChatID, *m.UserID, m.Date, trustScore, e.Rules.Version); err != nil {
				return m, domain.Assessment{}, false, err
			}
			if err = storage.Metric(ctx, tx, "sentinel_messages_observed_total", 1); err != nil {
				return m, domain.Assessment{}, false, err
			}
			if err = e.replyStats(ctx, tx, m); err != nil {
				return m, domain.Assessment{}, false, err
			}
		}
	}
	if m.UserID != nil {
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM message_journal WHERE chat_id=$1 AND user_id=$2 AND message_id<>$3 AND occurred_at>=$4::timestamptz-interval '1 minute' AND occurred_at<=$4::timestamptz AND content=$5`, m.ChatID, *m.UserID, m.ID, m.Date, m.Content()).Scan(&m.RecentSimilar); err != nil {
			return m, domain.Assessment{}, false, err
		}
		domains := []string{}
		for _, u := range m.URLs {
			domains = append(domains, u.Domain)
		}
		if len(domains) > 0 {
			if err = tx.QueryRow(ctx, `SELECT count(DISTINCT j.message_id) FROM message_journal j JOIN message_urls u ON u.chat_id=j.chat_id AND u.message_id=j.message_id WHERE j.chat_id=$1 AND j.user_id=$2 AND j.message_id<>$3 AND j.occurred_at>=$4::timestamptz-interval '1 minute' AND j.occurred_at<=$4::timestamptz AND u.domain=ANY($5::text[])`, m.ChatID, *m.UserID, m.ID, m.Date, domains).Scan(&m.RecentLinks); err != nil {
				return m, domain.Assessment{}, false, err
			}
		}
		var confirmed int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM incidents WHERE chat_id=$1 AND user_id=$2 AND status<>'needs_review' AND payload->>'label'='spam' AND updated_at>now()-interval '30 days'`, m.ChatID, *m.UserID).Scan(&confirmed); err != nil {
			return m, domain.Assessment{}, false, err
		}
		trustScore = max(0, trustScore-min(30, 10*confirmed))
		if _, err = tx.Exec(ctx, `UPDATE chat_user_stats SET trust=$3,trust_version=$4 WHERE chat_id=$1 AND user_id=$2`, m.ChatID, *m.UserID, trustScore, e.Rules.Version); err != nil {
			return m, domain.Assessment{}, false, err
		}
		if err = e.syncAggregates(ctx, tx, m.ChatID, *m.UserID); err != nil {
			return m, domain.Assessment{}, false, err
		}
	}
	if m.UserID == nil && !known && !exists && !reported && m.EditedAt == nil {
		if err = storage.Metric(ctx, tx, "sentinel_messages_observed_total", 1); err != nil {
			return m, domain.Assessment{}, false, err
		}
	}
	a := rules.Assess(m, m.URLs, first, trustScore, e.Rules)
	if _, err = tx.Exec(ctx, `INSERT INTO message_keys(chat_id,message_id,edit_version,user_id,fingerprint,last_edited_at) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(chat_id,message_id) DO UPDATE SET edit_version=EXCLUDED.edit_version,fingerprint=EXCLUDED.fingerprint,last_edited_at=EXCLUDED.last_edited_at`, m.ChatID, m.ID, m.Version, m.UserID, fingerprint(m), m.EditedAt); err != nil {
		return m, a, false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO message_journal(chat_id,message_id,user_id,sender_chat_id,message_thread_id,reply_to,content,payload,entities,urls,edit_version,stats_counted,occurred_at,edited_at,retention_until) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,true,$12,$13,LEAST(now(),COALESCE($13::timestamptz,$12::timestamptz))+$14::interval) ON CONFLICT(chat_id,message_id) DO UPDATE SET payload=EXCLUDED.payload,content=EXCLUDED.content,entities=EXCLUDED.entities,urls=EXCLUDED.urls,edit_version=EXCLUDED.edit_version,edited_at=EXCLUDED.edited_at,updated_at=now()`, m.ChatID, m.ID, m.UserID, m.SenderChatID, m.ThreadID, m.ReplyID, m.Content(), m, m.Entities, m.URLs, m.Version, m.Date, m.EditedAt, fmt.Sprintf("%d days", e.Config.Retention.MessagesDays)); err != nil {
		return m, a, false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO message_revisions(chat_id,message_id,edit_version,payload,urls) VALUES($1,$2,$3,$4,$5)`, m.ChatID, m.ID, m.Version, m, m.URLs); err != nil {
		return m, a, false, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM message_urls WHERE chat_id=$1 AND message_id=$2`, m.ChatID, m.ID); err != nil {
		return m, a, false, err
	}
	for i, u := range m.URLs {
		if _, err = tx.Exec(ctx, `INSERT INTO message_urls(chat_id,message_id,ordinal,url,domain,payload) VALUES($1,$2,$3,$4,$5,$6)`, m.ChatID, m.ID, i, u.Value, u.Domain, u); err != nil {
			return m, a, false, err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO risk_assessments(chat_id,message_id,edit_version,ruleset_version,score,trust,mandatory,review_required,payload) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, m.ChatID, m.ID, m.Version, a.Version, a.Score, a.Trust, a.Mandatory, a.ReviewRequired, a); err != nil {
		return m, a, false, err
	}
	for _, s := range a.Signals {
		if err = storage.Metric(ctx, tx, `sentinel_triggers_total{type="`+s.Code+`"}`, 1); err != nil {
			return m, a, false, err
		}
	}
	if a.ReviewRequired {
		if err = e.enqueue(ctx, tx, m, a, a.Mandatory); err != nil {
			return m, a, false, err
		}
	}
	// Any existing incident must refresh on edits, even when the journal entry was pruned.
	if exists || known {
		var found bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM incident_messages WHERE chat_id=$1 AND message_id=$2)`, m.ChatID, m.ID).Scan(&found); err != nil {
			return m, a, false, err
		}
		if found {
			if _, err = e.incident(ctx, tx, m, a, nil, false); err != nil {
				return m, a, false, err
			}
		}
	}
	return m, a, true, nil
}
func (e *Engine) replyStats(ctx context.Context, tx pgx.Tx, m domain.Message) error {
	if m.ReplyID == 0 || m.UserID == nil {
		return nil
	}
	var target *int64
	err := tx.QueryRow(ctx, `SELECT user_id FROM message_journal WHERE chat_id=$1 AND message_id=$2`, m.ChatID, m.ReplyID).Scan(&target)
	if errors.Is(err, pgx.ErrNoRows) || target == nil {
		return nil
	}
	if err != nil {
		return err
	}
	if *target == *m.UserID {
		return nil
	}
	admin := false
	if e.allowed(*m.UserID) {
		admin, err = e.Admins.IsAdmin(ctx, m.ChatID, *m.UserID)
		if err != nil {
			return err
		}
	}
	bonus := 0
	if admin {
		bonus = 1
	}
	_, err = tx.Exec(ctx, `INSERT INTO daily_activity(chat_id,user_id,day,admin_replies,replier_ids,admin_ids) VALUES($1,$2,$3,$4,ARRAY[$5::bigint],CASE WHEN $4::int=1 THEN ARRAY[$5::bigint] ELSE '{}'::bigint[] END) ON CONFLICT(chat_id,user_id,day) DO UPDATE SET admin_replies=LEAST(5,daily_activity.admin_replies+CASE WHEN $4::int=1 AND NOT $5=ANY(daily_activity.admin_ids) THEN 1 ELSE 0 END),admin_ids=CASE WHEN $4::int=1 AND NOT $5=ANY(daily_activity.admin_ids) AND cardinality(daily_activity.admin_ids)<5 THEN array_append(daily_activity.admin_ids,$5) ELSE daily_activity.admin_ids END,replier_ids=CASE WHEN $5=ANY(daily_activity.replier_ids) OR cardinality(daily_activity.replier_ids)>=20 THEN daily_activity.replier_ids ELSE array_append(daily_activity.replier_ids,$5) END`, m.ChatID, *target, m.Date.Format("2006-01-02"), bonus, *m.UserID)
	if err != nil {
		return err
	}
	return e.syncAggregates(ctx, tx, m.ChatID, *target)
}
func (e *Engine) enqueue(ctx context.Context, tx pgx.Tx, m domain.Message, a domain.Assessment, mandatory bool) error {
	contextMessages := []domain.Message{}
	seen := map[int64]bool{m.ID: true}
	if m.Reply != nil {
		r := *m.Reply
		r.Reply = nil
		contextMessages = append(contextMessages, r)
		seen[r.ID] = true
	}
	// Up to three previous author messages and three nearby messages, same chat/topic only.
	for _, author := range []bool{true, false} {
		rows, err := tx.Query(ctx, `SELECT payload FROM message_journal WHERE chat_id=$1 AND message_id<>$2 AND occurred_at<=$3 AND message_thread_id=$4 AND ($5=false OR user_id=$6) ORDER BY occurred_at DESC LIMIT 3`, m.ChatID, m.ID, m.Date, m.ThreadID, author, m.UserID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var raw []byte
			if err = rows.Scan(&raw); err != nil {
				rows.Close()
				return err
			}
			var c domain.Message
			if err = json.Unmarshal(raw, &c); err != nil {
				rows.Close()
				return err
			}
			if !seen[c.ID] {
				c.Reply = nil
				contextMessages = append(contextMessages, c)
				seen[c.ID] = true
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	for i := range contextMessages {
		contextMessages[i] = jobMessage(contextMessages[i], 1000, 4)
		contextMessages[i].Reply = nil
	}
	jobAssessment := a
	jobAssessment.URLs = boundedURLs(a.URLs, 16)
	jobAssessment.Signals = append([]domain.Signal(nil), a.Signals...)
	for i := range jobAssessment.Signals {
		jobAssessment.Signals[i].Evidence = privacy.Limit(jobAssessment.Signals[i].Evidence, 512)
	}
	r := domain.ReviewRequest{Message: jobMessage(m, 6000, 16), Assessment: jobAssessment, Context: contextMessages, PromptVersion: prompts.CurrentVersion}
	priority := "optional"
	if mandatory {
		priority = "mandatory"
	}
	kind := fmt.Sprintf("review:%d", m.Version)
	tag, err := tx.Exec(ctx, `INSERT INTO ai_jobs(chat_id,message_id,kind,priority,payload) VALUES($1,$2,$3,$4,$5) ON CONFLICT(chat_id,message_id,kind) WHERE status IN ('pending','processing','done') DO NOTHING`, m.ChatID, m.ID, kind, priority, r)
	if err != nil {
		return err
	}
	if mandatory {
		if _, err = tx.Exec(ctx, `UPDATE ai_jobs SET priority='mandatory' WHERE chat_id=$1 AND message_id=$2 AND kind=$3`, m.ChatID, m.ID, kind); err != nil {
			return err
		}
	}
	if tag.RowsAffected() == 1 {
		return storage.Metric(ctx, tx, "sentinel_ai_jobs_required_total", 1)
	}
	return nil
}
func (e *Engine) incident(ctx context.Context, tx pgx.Tx, m domain.Message, a domain.Assessment, ai *domain.ReviewResponse, report bool) (int64, error) {
	var id int64
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT i.id,i.payload FROM incidents i JOIN incident_messages im ON im.incident_id=i.id WHERE im.chat_id=$1 AND im.message_id=$2 ORDER BY i.id LIMIT 1 FOR UPDATE OF i`, m.ChatID, m.ID).Scan(&id, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		parts := []string{}
		for _, u := range m.URLs {
			parts = append(parts, u.Domain)
		}
		sort.Strings(parts)
		fp := strings.Join(parts, ",")
		if fp == "" {
			fp = fingerprint(m)
		}
		if m.SenderChatID != nil {
			fp += strconv.FormatInt(*m.SenderChatID, 10)
		}
		seconds := max(60, e.Config.Notifications.DuplicateCooldownSeconds)
		window := time.Now().UTC().Truncate(time.Duration(seconds) * time.Second)
		err = tx.QueryRow(ctx, `INSERT INTO incidents(chat_id,user_id,fingerprint,window_start,retain_until) VALUES($1,$2,$3,$4,now()+$5::interval) ON CONFLICT(chat_id,(COALESCE(user_id,0)),fingerprint,window_start) DO UPDATE SET updated_at=now() RETURNING id,payload`, m.ChatID, m.UserID, fp, window, fmt.Sprintf("%d days", e.Config.Retention.IncidentDays)).Scan(&id, &raw)
	}
	if err != nil {
		return 0, err
	}
	var c notify.Card
	if err = json.Unmarshal(raw, &c); err != nil {
		return 0, err
	}
	c.ID = id
	if c.Message.ID == 0 || c.Message.ID == m.ID && m.Version >= c.Message.Version || m.ID > c.Message.ID {
		if c.Message.ID != m.ID || c.Message.Version != m.Version {
			c.AI = nil
		}
		c.Message = m
		c.Assessment = a
		c.AIState = "pending"
		if !a.ReviewRequired {
			c.AIState = "not_requested"
		}
	}
	if ai != nil && c.Message.ID == m.ID && c.Message.Version == m.Version {
		c.AI = ai
		c.AIState = "done"
	}
	if c.AI == nil {
		var reviewed []byte
		lookupErr := tx.QueryRow(ctx, `SELECT r.payload FROM ai_reviews r JOIN ai_jobs j ON j.id=r.job_id WHERE j.chat_id=$1 AND j.message_id=$2 AND (j.payload->'message'->>'edit_version')::int=$3 AND j.status='done' ORDER BY r.id DESC LIMIT 1`, c.Message.ChatID, c.Message.ID, c.Message.Version).Scan(&reviewed)
		if lookupErr == nil {
			var result domain.ReviewResponse
			if err = json.Unmarshal(reviewed, &result); err != nil {
				return 0, err
			}
			c.AI = &result
			c.AIState = "done"
		} else if !errors.Is(lookupErr, pgx.ErrNoRows) {
			return 0, lookupErr
		}
	}
	if c.AI != nil {
		c.AIState = "done"
	}
	if c.Status == "" {
		c.Status = "open"
	}
	if _, err = tx.Exec(ctx, `INSERT INTO incident_messages(incident_id,chat_id,message_id,evidence) VALUES($1,$2,$3,$4) ON CONFLICT(incident_id,chat_id,message_id) DO UPDATE SET evidence=EXCLUDED.evidence`, id, m.ChatID, m.ID, struct {
		Message    domain.Message
		Assessment domain.Assessment
	}{m, a}); err != nil {
		return 0, err
	}
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM user_reports r JOIN incident_messages im ON im.chat_id=r.chat_id AND im.message_id=r.target_message_id WHERE im.incident_id=$1`, id).Scan(&c.Reports); err != nil {
		return 0, err
	}
	if err = tx.QueryRow(ctx, `SELECT COALESCE(array_agg(reporter_id),'{}'::bigint[]) FROM (SELECT DISTINCT r.reporter_id FROM user_reports r JOIN incident_messages im ON im.chat_id=r.chat_id AND im.message_id=r.target_message_id WHERE im.incident_id=$1 ORDER BY r.reporter_id LIMIT 20) authors`, id).Scan(&c.Reporters); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `UPDATE incidents SET payload=$2,updated_at=now() WHERE id=$1`, id, c); err != nil {
		return 0, err
	}
	return id, e.queueCard(ctx, tx, id)
}
func (e *Engine) queueCard(ctx context.Context, tx pgx.Tx, id int64) error {
	dests := []int64{e.Config.Telegram.AdminChatID}
	if e.Config.Notifications.SendToPrivateAdmins {
		dests = append(dests, e.Config.Telegram.AllowedAdminIDs...)
	}
	for _, dest := range dests {
		d := notify.Delivery{IncidentID: id, ChatID: dest, Private: true}
		key := fmt.Sprintf("card:%d:%d", id, dest)
		_, err := tx.Exec(ctx, `INSERT INTO outbox(idempotency_key,payload,available_at) VALUES($1,$2,now()+$3::interval) ON CONFLICT(idempotency_key) DO UPDATE SET payload=EXCLUDED.payload,status=CASE WHEN outbox.status='processing' THEN 'processing' ELSE 'pending' END,updated_at=now()`, key, d, fmt.Sprintf("%d seconds", e.Config.Notifications.BatchWindowSeconds))
		if err != nil {
			return err
		}
	}
	return nil
}
