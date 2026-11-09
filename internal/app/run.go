package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"sentinel/internal/config"
	"sentinel/internal/domain"
	"sentinel/internal/privacy"
	"sentinel/internal/review"
	"sentinel/internal/storage"
	"sentinel/internal/telegram"
)

func (e *Engine) Prepare(u domain.Update) domain.Update {
	if m := u.Message; m != nil {
		if !e.Managed(m.ChatID) && !e.isAdminDestination(m.ChatID) {
			u.Message = nil
			return u
		}
		if !e.Managed(m.ChatID) && e.controlName(m.Content()) == "" {
			u.Message = nil
			return u
		}
		m.ContentHash = ""
		m.ContentHash = fingerprint(*m)
		m.URLs = e.Scanner.Scan(*m)
		if m.Reply != nil {
			m.Reply.ContentHash = ""
			m.Reply.ContentHash = fingerprint(*m.Reply)
			m.Reply.URLs = e.Scanner.Scan(*m.Reply)
		}
		safe := privacy.Message(*m)
		u.Message = &safe
	}
	if u.Membership != nil && !e.Managed(u.Membership.ChatID) {
		u.Membership = nil
	}
	return u
}
func (e *Engine) Poll(ctx context.Context, p *telegram.Poller) error {
	last, err := e.Store.Offset(ctx, e.BotID)
	if err != nil {
		return err
	}
	for ctx.Err() == nil {
		last, err = e.Store.Offset(ctx, e.BotID)
		if err != nil {
			slog.Error("poll cursor unavailable; acknowledgement paused")
			if !wait(ctx, 5*time.Second) {
				break
			}
			continue
		}
		updates, err := p.Fetch(ctx, last+1)
		if err != nil {
			delay := 5 * time.Second
			var api *telegram.APIError
			if errors.As(err, &api) {
				delay = max(delay, api.RetryAfter)
			}
			slog.Warn("poll retry", "provider", "telegram", "retry_seconds", delay.Seconds())
			if !wait(ctx, delay) {
				break
			}
			continue
		}
		for _, raw := range updates {
			u, err := telegram.Decode(raw)
			if err != nil {
				slog.Error("update decode blocked; no acknowledgement advanced")
				if !wait(ctx, 5*time.Second) {
					return ctx.Err()
				}
				break
			}
			safe := e.Prepare(u)
			if err = e.Store.Ingest(ctx, e.BotID, safe); err != nil {
				slog.Error("durable ingest failed; no acknowledgement advanced", "update_id", u.ID)
				if !wait(ctx, 5*time.Second) {
					return ctx.Err()
				}
				break
			}
			last = u.ID
		}
	}
	return ctx.Err()
}
func wait(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
func worker(ctx context.Context, name string, fn func(context.Context) (bool, error), wg *sync.WaitGroup) {
	defer wg.Done()
	for ctx.Err() == nil {
		start := time.Now()
		jobCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		worked, err := fn(jobCtx)
		cancel()
		if err != nil {
			slog.Error("worker operation failed", "worker", name, "elapsed_ms", time.Since(start).Milliseconds())
			if !wait(ctx, 2*time.Second) {
				return
			}
		} else if !worked {
			if !wait(ctx, 500*time.Millisecond) {
				return
			}
		}
	}
}
func Run(ctx context.Context, c *config.Config) error {
	s, err := storage.New(ctx, c.DatabaseURL)
	if err != nil {
		return fmt.Errorf("database connection failed (check credentials and availability)")
	}
	defer s.Close()
	if err = s.Migrate(ctx); err != nil {
		return fmt.Errorf("database migration failed: %w", err)
	}
	sender, err := telegram.NewSender(c.BotToken)
	if err != nil {
		return err
	}
	id, username, err := sender.GetMe(ctx)
	if err != nil {
		return fmt.Errorf("Telegram getMe failed; check token and network")
	}
	// A dedicated connection holds this lock for the process lifetime, preventing competing poll offsets.
	owner, err := s.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer owner.Release()
	var locked bool
	if err = owner.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, id).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return fmt.Errorf("another sentinel process already owns this bot")
	}
	defer owner.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, id)
	webhook, err := sender.WebhookURL(ctx)
	if err != nil {
		return fmt.Errorf("cannot inspect Telegram webhook state")
	}
	if webhook != "" {
		return fmt.Errorf("polling refused: webhook configured; remove it explicitly without drop_pending_updates")
	}
	private, err := sender.PrivateDestination(ctx, c.Telegram.AdminChatID)
	if err != nil {
		return fmt.Errorf("cannot inspect configured admin destination; check chat access")
	}
	if !private {
		return fmt.Errorf("admin destination must be private, not a public group/channel")
	}
	e := NewEngine(s, c, sender, strconv.FormatInt(id, 10), username)
	if err = e.RegisterRules(ctx); err != nil {
		return err
	}
	for _, chat := range c.Telegram.ChatIDs {
		admin, err := sender.IsAdmin(ctx, chat, id)
		if err != nil {
			return fmt.Errorf("cannot verify bot membership in configured chat")
		}
		if !admin {
			slog.Warn("chat_member unavailable: bot is not administrator", "chat_id", chat)
		}
		features := map[string]any{"bot_admin": admin, "chat_member": "unavailable"}
		if admin {
			features["chat_member"] = "subscribed_requires_real_update"
		}
		if _, err = s.Pool.Exec(ctx, `INSERT INTO chats(chat_id,features) VALUES($1,$2) ON CONFLICT(chat_id) DO UPDATE SET features=EXCLUDED.features`, chat, features); err != nil {
			return err
		}
	}
	ln, err := net.Listen("tcp", c.HealthAddr)
	if err != nil {
		return fmt.Errorf("listen health endpoint: %w", err)
	}
	srv := &http.Server{Handler: e.HealthHandler(), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if serveErr := srv.Serve(ln); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			slog.Error("health server stopped")
			cancel()
		}
	}()
	provider := &review.Compatible{Client: &http.Client{Timeout: c.AISubsystemTimeout(), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, BaseURL: c.AIBaseURL, Key: c.AIKey, Model: c.AI.Model, InputPrice: c.AI.InputPricePerMillion, OutputPrice: c.AI.OutputPricePerMillion}
	e.Catalog = provider
	wg.Add(1)
	go worker(child, "inbox", e.ProcessOne, &wg)
	for i := 0; i < c.AI.MaxConcurrent; i++ {
		wg.Add(1)
		go worker(child, "ai", func(ctx context.Context) (bool, error) { return e.ReviewOne(ctx, provider) }, &wg)
	}
	wg.Add(1)
	go worker(child, "outbox", func(ctx context.Context) (bool, error) { return e.DeliverOne(ctx, sender) }, &wg)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for child.Err() == nil {
			cleanupCtx, stop := context.WithTimeout(child, 30*time.Second)
			err := e.Cleanup(cleanupCtx)
			stop()
			if err != nil {
				slog.Error("retention failed")
			}
			if !wait(child, time.Minute) {
				return
			}
		}
	}()
	slog.Info("sentinel started", "mode", "observe_only", "rule_version", e.Rules.Version)
	pollErr := e.Poll(child, telegram.NewPoller(c.BotToken))
	cancel()
	shutdownCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	if err = srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("health shutdown failed")
	}
	wg.Wait()
	if errors.Is(pollErr, context.Canceled) {
		return nil
	}
	return pollErr
}
func (e *Engine) HealthHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := e.Store.Pool.Ping(ctx); err != nil {
			http.Error(w, "database unavailable", 503)
			return
		}
		w.WriteHeader(200)
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		var messages, pending, done, reports, events, alerts, inbox int64
		err := e.Store.Pool.QueryRow(ctx, `SELECT COALESCE((SELECT value FROM runtime_metrics WHERE name='sentinel_messages_observed_total'),0),(SELECT count(*) FROM ai_jobs WHERE status IN ('pending','processing')),(SELECT count(*) FROM ai_jobs WHERE status='done'),(SELECT count(*) FROM user_reports),(SELECT count(*) FROM moderation_events WHERE evidence_level='verified'),(SELECT count(*) FROM alerts WHERE status='sent'),(SELECT count(*) FROM inbox WHERE status<>'done')`).Scan(&messages, &pending, &done, &reports, &events, &alerts, &inbox)
		if err != nil {
			http.Error(w, "metrics unavailable", 503)
			return
		}
		for _, p := range []struct {
			name string
			n    int64
		}{{"sentinel_messages_total", messages}, {"sentinel_ai_jobs_pending", pending}, {"sentinel_ai_jobs_succeeded_retained", done}, {"sentinel_reports_retained", reports}, {"sentinel_confirmed_actions_retained", events}, {"sentinel_alerts_retained", alerts}, {"sentinel_inbox_pending", inbox}} {
			fmt.Fprintf(w, "%s %d\n", p.name, p.n)
		}
		rows, err := e.Store.Pool.Query(ctx, `SELECT name,value FROM runtime_metrics ORDER BY name`)
		if err != nil {
			return
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			var value int64
			if err = rows.Scan(&name, &value); err != nil {
				return
			}
			fmt.Fprintf(w, "%s %d\n", name, value)
		}
		var lag float64
		if err = e.Store.Pool.QueryRow(ctx, `SELECT COALESCE(EXTRACT(epoch FROM now()-min(created_at)),0)::float8 FROM ai_jobs WHERE status IN('pending','processing')`).Scan(&lag); err == nil {
			fmt.Fprintf(w, "sentinel_ai_queue_lag_seconds %f\n", lag)
		}
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "use authenticated Telegram /status", 404) })
	return mux
}
