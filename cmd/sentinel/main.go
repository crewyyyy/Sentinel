package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"sentinel/internal/app"
	"sentinel/internal/config"
	"sentinel/internal/domain"
	"sentinel/internal/privacy"
	"sentinel/internal/rules"
	"sentinel/internal/storage"
	"sentinel/internal/telegram"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		slog.Error("command failed", "error", privacy.Redact(err.Error()))
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string) error {
	cmd := "run"
	if len(args) > 0 {
		cmd = args[0]
		args = args[1:]
	}
	f := flag.NewFlagSet(cmd, flag.ContinueOnError)
	path := f.String("config", "configs/config.yaml", "YAML configuration")
	file := f.String("file", "", "input fixture or JSONL dataset")
	chat := f.Int64("chat", 0, "managed chat ID")
	user := f.Int64("user", 0, "user ID")
	if err := f.Parse(args); err != nil {
		return err
	}
	explicitConfig := false
	f.Visit(func(value *flag.Flag) {
		if value.Name == "config" {
			explicitConfig = true
		}
	})
	switch cmd {
	case "modules":
		return json.NewEncoder(os.Stdout).Encode(rules.Catalog())
	case "healthcheck":
		addr := os.Getenv("HEALTH_ADDR")
		if addr == "" {
			addr = "127.0.0.1:8080"
		}
		addr = strings.Replace(addr, "0.0.0.0:", "127.0.0.1:", 1)
		client := &http.Client{Timeout: 3 * time.Second}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/healthz", nil)
		if err != nil {
			return fmt.Errorf("invalid health address")
		}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("healthcheck unavailable")
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return fmt.Errorf("unhealthy status")
		}
		return nil
	case "migrate":
		s, err := storage.New(ctx, os.Getenv("DATABASE_URL"))
		if err != nil {
			return fmt.Errorf("database connection unavailable")
		}
		defer s.Close()
		return s.Migrate(ctx)
	case "inspect":
		data, err := readBounded(*file, 256<<10)
		if err != nil {
			return err
		}
		u, err := telegram.Decode(data)
		if err != nil {
			return err
		}
		if u.Message == nil {
			return json.NewEncoder(os.Stdout).Encode(u)
		}
		c := config.Defaults()
		if explicitConfig {
			c, err = config.LoadPolicy(*path)
			if err != nil {
				return err
			}
		}
		e, err := app.NewEngine(nil, c, nil, "offline", "")
		if err != nil {
			return err
		}
		m := privacy.Message(e.Scanner.Extract(*u.Message))
		a := e.Analyzer.Assess(m, m.URLs, true, 0)
		return json.NewEncoder(os.Stdout).Encode(struct {
			Message    domain.Message
			Assessment domain.Assessment
			AI         string
		}{m, a, "not called (offline inspect)"})
	}
	var c *config.Config
	var err error
	if cmd == "replay" {
		c, err = config.LoadPolicy(*path)
	} else {
		c, err = config.Load(*path)
	}
	if err != nil {
		return err
	}
	if cmd == "validate" {
		fmt.Fprintln(os.Stdout, "configuration valid; observe-only; no network calls")
		return nil
	}
	e, err := app.NewEngine(nil, c, nil, "offline", "")
	if err != nil {
		return err
	}
	if cmd == "replay" {
		r, err := os.Open(*file)
		if err != nil {
			return err
		}
		defer r.Close()
		return app.ReplayWithAnalyzer(r, os.Stdout, e.Analyzer, e.Rules.AIThreshold)
	}
	if cmd == "run" {
		return app.Run(ctx, c)
	}
	if cmd != "export" && cmd != "forget" {
		return fmt.Errorf("unknown command; use run, validate, migrate, modules, inspect, replay, export, forget, healthcheck")
	}
	s, err := storage.New(ctx, c.DatabaseURL)
	if err != nil {
		return fmt.Errorf("database connection unavailable")
	}
	defer s.Close()
	e.Store = s
	switch cmd {
	case "export":
		return e.Export(ctx, os.Stdout)
	case "forget":
		return e.Forget(ctx, *chat, *user)
	}
	return nil
}
func readBounded(path string, n int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, n+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > n {
		return nil, fmt.Errorf("input exceeds limit")
	}
	return data, nil
}
