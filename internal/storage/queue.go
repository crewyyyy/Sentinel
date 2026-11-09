package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"sentinel/internal/domain"
	"sentinel/prompts"
	"time"
)

type AIJob struct {
	ID              int64
	Token, Attempts int
	Priority        string
	Payload         domain.ReviewRequest
}

// ClaimAI atomically fences an attempt and reserves optional request budget. Mandatory bypass is unconditional.
func (s *Store) ClaimAI(ctx context.Context, optionalLimit int, lease time.Duration, defaultModel ...string) (job *AIJob, err error) {
	err = execTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var paused bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ai_provider_state WHERE retry_at>now())`).Scan(&paused); err != nil {
			return err
		}
		if paused {
			return nil
		}
		j := &AIJob{}
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT id,payload,priority,attempts FROM ai_jobs WHERE ((status='pending' AND available_at<=now()) OR (status='processing' AND lease_until<now())) ORDER BY (priority='mandatory') DESC,available_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&j.ID, &raw, &j.Priority, &j.Attempts)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if j.Priority == "optional" {
			tag, err := tx.Exec(ctx, `INSERT INTO ai_budget(day,requests) SELECT (now() AT TIME ZONE 'UTC')::date,1 WHERE $1>0 ON CONFLICT(day) DO UPDATE SET requests=ai_budget.requests+1 WHERE ai_budget.requests<$1`, optionalLimit)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return nil
			}
		}
		if err = json.Unmarshal(raw, &j.Payload); err != nil {
			return fmt.Errorf("stored AI payload invalid: %w", err)
		}
		j.Attempts++
		j.Token = j.Attempts
		j.Payload.JobID = j.ID
		fallback := ""
		if len(defaultModel) > 0 {
			fallback = defaultModel[0]
		}
		setting, err := CurrentModel(ctx, tx, fallback)
		if err != nil {
			return err
		}
		j.Payload.Model = setting.Model
		j.Payload.PromptVersion = prompts.CurrentVersion
		_, err = tx.Exec(ctx, `UPDATE ai_jobs SET status='processing',attempts=$2::int,lease_token=$2::bigint,lease_until=now()+$3::interval,updated_at=now() WHERE id=$1`, j.ID, j.Attempts, lease.String())
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE ai_attempts SET status='outcome_unknown',finished_at=now() WHERE job_id=$1 AND status='started'`, j.ID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO ai_attempts(job_id,attempt,model,prompt_version) VALUES($1,$2,$3,$4)`, j.ID, j.Attempts, j.Payload.Model, j.Payload.PromptVersion); err != nil {
			return err
		}
		job = j
		return Metric(ctx, tx, "sentinel_ai_attempts_total", 1)
	})
	return job, err
}

// FinishAI must run in the same transaction as incident/outbox updates.
func FinishAI(ctx context.Context, tx pgx.Tx, j *AIJob, r domain.ReviewResponse) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE ai_jobs SET status='done',result=$3,lease_until=NULL,updated_at=now() WHERE id=$1 AND lease_token=$2 AND status='processing'`, j.ID, j.Token, raw)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("AI lease lost")
	}
	if _, err = tx.Exec(ctx, `UPDATE ai_attempts SET status='succeeded',finished_at=now() WHERE job_id=$1 AND attempt=$2`, j.ID, j.Attempts); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO ai_provider_state(id,failures,retry_at) VALUES(true,0,now()) ON CONFLICT(id) DO UPDATE SET failures=0,retry_at=now()`); err != nil {
		return err
	}
	if err = Metric(ctx, tx, "sentinel_ai_jobs_succeeded_total", 1); err != nil {
		return err
	}
	if r.Usage.InputTokens != nil && r.Usage.OutputTokens != nil {
		if err = Metric(ctx, tx, "sentinel_ai_tokens_in_total", *r.Usage.InputTokens); err != nil {
			return err
		}
		if err = Metric(ctx, tx, "sentinel_ai_tokens_out_total", *r.Usage.OutputTokens); err != nil {
			return err
		}
	} else {
		if err = Metric(ctx, tx, "sentinel_ai_usage_unknown_total", 1); err != nil {
			return err
		}
	}
	if r.Usage.EstimatedCost != nil {
		if err = Metric(ctx, tx, "sentinel_ai_cost_estimate_micro_total", int64(*r.Usage.EstimatedCost*1e6)); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO ai_reviews(job_id,chat_id,message_id,payload,provider,model,input_tokens,output_tokens,estimated_cost) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(job_id) DO NOTHING`, j.ID, j.Payload.Message.ChatID, j.Payload.Message.ID, raw, r.Provider, r.Model, r.Usage.InputTokens, r.Usage.OutputTokens, r.Usage.EstimatedCost)
	return err
}
func (s *Store) RetryAI(ctx context.Context, j *AIJob, delay time.Duration, code string) error {
	return execTx(ctx, s.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE ai_jobs SET status='pending',lease_until=NULL,available_at=now()+$3::interval,last_error=$4,updated_at=now() WHERE id=$1 AND lease_token=$2 AND status='processing'`, j.ID, j.Token, delay.String(), code)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE ai_attempts SET status='retry',error_code=$3,finished_at=now() WHERE job_id=$1 AND attempt=$2`, j.ID, j.Attempts, code); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO ai_provider_state(id,failures,retry_at) VALUES(true,1,CASE WHEN $2 THEN now()+$1::interval ELSE now() END) ON CONFLICT(id) DO UPDATE SET failures=ai_provider_state.failures+1,retry_at=CASE WHEN ai_provider_state.failures>=2 OR $2 THEN now()+$1::interval ELSE ai_provider_state.retry_at END`, max(30*time.Second, delay).String(), code == "provider_http_429")
		return err
	})
}
