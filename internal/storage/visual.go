package storage

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"sentinel/internal/domain"
	"sentinel/internal/visual"
)

func VisualKey(m domain.Media) string {
	id := m.UniqueID
	if m.EmojiID != "" {
		id = "emoji:" + m.EmojiID
	}
	if id == "" {
		id = "file:" + m.FileID
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(visual.Model+":samples-v1:"+id)))
}

func EnqueueVisual(ctx context.Context, tx pgx.Tx, m domain.Message, days int) error {
	keys := []string{}
	seen := map[string]bool{}
	for _, media := range m.Media {
		key := VisualKey(media)
		if seen[key] {
			continue
		}
		seen[key] = true
		keys = append(keys, key)
		if _, err := tx.Exec(ctx, `INSERT INTO visual_checks(cache_key,media,expires_at) VALUES($1,$2,now()+$3::interval) ON CONFLICT(cache_key) DO UPDATE SET media=EXCLUDED.media,status='pending',attempts=0,result=NULL,last_error='',available_at=now(),expires_at=EXCLUDED.expires_at WHERE visual_checks.status='done' AND visual_checks.expires_at<now()`, key, media, fmt.Sprintf("%d days", days)); err != nil {
			return err
		}
	}
	if len(keys) == 0 {
		return nil
	}
	// No private captions, text, usernames or reply contents are needed to classify media.
	if m.ChatID > 0 {
		m.Text = ""
		m.Caption = ""
		m.QuoteText = ""
		m.Reply = nil
		m.Entities = nil
		m.CaptionEntities = nil
		m.QuoteEntities = nil
		m.URLs = nil
		m.Mentions = nil
		m.Username = ""
	}
	_, err := tx.Exec(ctx, `INSERT INTO visual_requests(chat_id,message_id,edit_key,payload,check_keys) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, m.ChatID, m.ID, m.ContentHash, m, keys)
	return err
}

type VisualJob struct {
	Key      string
	Media    domain.Media
	Token    int64
	Attempts int
}

func (s *Store) ClaimVisual(ctx context.Context) (*VisualJob, error) {
	var result *VisualJob
	err := execTx(ctx, s.Pool, func(tx pgx.Tx) error {
		j := &VisualJob{}
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT cache_key,media,lease_token+1,attempts+1 FROM visual_checks WHERE (status='pending' AND available_at<=now()) OR (status='processing' AND lease_until<now()) ORDER BY available_at,cache_key LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&j.Key, &raw, &j.Token, &j.Attempts)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err = json.Unmarshal(raw, &j.Media); err != nil {
			return fmt.Errorf("invalid visual job")
		}
		if _, err = tx.Exec(ctx, `UPDATE visual_checks SET status='processing',attempts=$2,lease_token=$3,lease_until=now()+interval '3 minutes',updated_at=now() WHERE cache_key=$1`, j.Key, j.Attempts, j.Token); err != nil {
			return err
		}
		result = j
		return nil
	})
	return result, err
}

func (s *Store) FinishVisual(ctx context.Context, j *VisualJob, result domain.MediaReview, days int) error {
	ttl := fmt.Sprintf("%d days", days)
	if result.Unavailable {
		ttl = "15 minutes"
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE visual_checks SET status='done',media='{}',result=$3,expires_at=now()+$4::interval,lease_until=NULL,updated_at=now() WHERE cache_key=$1 AND lease_token=$2 AND status='processing'`, j.Key, j.Token, result, ttl)
	if err == nil && tag.RowsAffected() != 1 {
		return fmt.Errorf("visual lease lost")
	}
	return err
}

func (s *Store) RetryVisual(ctx context.Context, j *VisualJob, code string, delay time.Duration) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE visual_checks SET status='pending',lease_until=NULL,last_error=$3,available_at=now()+$4::interval,updated_at=now() WHERE cache_key=$1 AND lease_token=$2 AND status='processing'`, j.Key, j.Token, code, delay.String())
	if err == nil && tag.RowsAffected() != 1 {
		return fmt.Errorf("visual lease lost")
	}
	return err
}

func (s *Store) ReserveVisual(ctx context.Context, limit int) (bool, error) {
	tag, err := s.Pool.Exec(ctx, `INSERT INTO visual_budget(day,requests) SELECT (now() AT TIME ZONE 'UTC')::date,1 WHERE $1>0 ON CONFLICT(day) DO UPDATE SET requests=visual_budget.requests+1 WHERE visual_budget.requests<$1`, limit)
	return tag.RowsAffected() == 1, err
}
