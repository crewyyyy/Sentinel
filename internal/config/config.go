// Package config loads strict YAML policies and environment-only secrets.
package config

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"os"
	"regexp"
	"sentinel/internal/review"
	"sentinel/internal/rules"
	"sentinel/internal/urlscan"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const ModePolling = "polling"
const ModeWebhook = "webhook"

var versionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,79}$`)

type Config struct {
	Telegram      Telegram      `yaml:"telegram"`
	Moderation    Moderation    `yaml:"moderation"`
	Trust         Trust         `yaml:"trust"`
	AI            AI            `yaml:"ai"`
	Retention     Retention     `yaml:"retention"`
	Links         Links         `yaml:"links"`
	Notifications Notifications `yaml:"notifications"`
	Analysis      rules.Options `yaml:"analysis"`
	DatabaseURL   string        `yaml:"-"`
	BotToken      string        `yaml:"-"`
	AIKey         string        `yaml:"-"`
	AIBaseURL     string        `yaml:"-"`
	WebhookSecret string        `yaml:"-"`
	HealthAddr    string        `yaml:"-"`
}
type Telegram struct {
	Mode              string  `yaml:"mode"`
	AutoDiscoverChats bool    `yaml:"auto_discover_chats"`
	ChatIDs           []int64 `yaml:"chat_ids"`
	AdminChatID       int64   `yaml:"admin_chat_id"`
	AllowedAdminIDs   []int64 `yaml:"allowed_admin_ids"`
}
type Moderation struct {
	EnforcementEnabled bool           `yaml:"enforcement_enabled"`
	RulesetVersion     string         `yaml:"ruleset_version"`
	Weights            map[string]int `yaml:"weights"`
	RiskAIThreshold    int            `yaml:"risk_ai_threshold"`
	AlertThreshold     int            `yaml:"alert_threshold"`
	ScoreThresholds    []int          `yaml:"score_thresholds"`
	ReportAliases      []string       `yaml:"report_aliases"`
	WatchMessageEdits  bool           `yaml:"watch_message_edits"`
}
type Trust struct {
	MaximumRiskDiscount int `yaml:"maximum_risk_discount"`
	AdminReplyBonusCap  int `yaml:"admin_reply_bonus_cap"`
	StatsWindowDays     int `yaml:"stats_window_days"`
}
type AI struct {
	Enabled                           bool    `yaml:"enabled"`
	Provider                          string  `yaml:"provider"`
	Model                             string  `yaml:"model"`
	TimeoutSeconds                    int     `yaml:"timeout_seconds"`
	MaxConcurrent                     int     `yaml:"max_concurrent"`
	OptionalDailyRequestLimit         int     `yaml:"optional_daily_request_limit"`
	MandatoryJobsBypassOptionalBudget bool    `yaml:"mandatory_jobs_bypass_optional_budget"`
	ReviewReportedMessages            bool    `yaml:"review_reported_messages"`
	InputPricePerMillion              float64 `yaml:"input_price_per_million"`
	OutputPricePerMillion             float64 `yaml:"output_price_per_million"`
}
type Retention struct {
	MaxRecentMessagesPerChat int `yaml:"max_recent_messages_per_chat"`
	MessagesDays             int `yaml:"messages_days"`
	IncidentDays             int `yaml:"incident_days"`
	AggregateDays            int `yaml:"aggregate_days"`
}
type Links struct {
	AllowDomains []string `yaml:"allow_domains"`
	WatchDomains []string `yaml:"watch_domains"`
	DenyDomains  []string `yaml:"deny_domains"`
	KnownBots    []string `yaml:"known_bots"`
}
type Notifications struct {
	BatchWindowSeconds       int  `yaml:"batch_window_seconds"`
	DuplicateCooldownSeconds int  `yaml:"duplicate_cooldown_seconds"`
	SendToPrivateAdmins      bool `yaml:"send_to_private_admins"`
}

func Defaults() *Config {
	return &Config{
		Telegram:      Telegram{Mode: ModePolling, AutoDiscoverChats: true},
		Moderation:    Moderation{RulesetVersion: "v1", RiskAIThreshold: 45, AlertThreshold: 45, ScoreThresholds: []int{25, 45, 70, 85}, ReportAliases: []string{"/report", "!репорт", "!report"}, WatchMessageEdits: true},
		Trust:         Trust{MaximumRiskDiscount: 20, AdminReplyBonusCap: 15, StatsWindowDays: 30},
		AI:            AI{Enabled: true, Provider: "openai_compatible", TimeoutSeconds: 15, MaxConcurrent: 3, OptionalDailyRequestLimit: 200, MandatoryJobsBypassOptionalBudget: true, ReviewReportedMessages: true},
		Retention:     Retention{MaxRecentMessagesPerChat: 1000, MessagesDays: 7, IncidentDays: 30, AggregateDays: 90},
		Links:         Links{AllowDomains: []string{"google.com", "youtube.com", "youtu.be", "tiktok.com", "github.com"}},
		Notifications: Notifications{BatchWindowSeconds: 20, DuplicateCooldownSeconds: 300}, HealthAddr: "127.0.0.1:8080",
	}
}
func loadFile(path string) (*Config, error) {
	c := Defaults()
	if path != "" {
		file, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open configuration: %w", err)
		}
		data, readErr := io.ReadAll(io.LimitReader(file, (64<<10)+1))
		closeErr := file.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read configuration: %w", readErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close configuration: %w", closeErr)
		}
		if len(data) > 64<<10 {
			return nil, fmt.Errorf("configuration exceeds 64 KiB")
		}
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)
		if err = dec.Decode(c); err != nil {
			// YAML errors may quote scalar values, including accidentally pasted secrets.
			return nil, fmt.Errorf("parse configuration: invalid YAML, field name or value type")
		}
		var extra any
		if err = dec.Decode(&extra); err != io.EOF {
			return nil, fmt.Errorf("configuration must contain exactly one YAML document")
		}
	}
	return c, nil
}

// LoadPolicy validates offline analysis settings without reading credentials or calling external services.
func LoadPolicy(path string) (*Config, error) {
	c, err := loadFile(path)
	if err != nil {
		return nil, err
	}
	validation := *c
	validation.DatabaseURL = "offline"
	validation.BotToken = "offline"
	validation.AI.Enabled = false
	if err := validation.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func Load(path string) (*Config, error) {
	c, err := loadFile(path)
	if err != nil {
		return nil, err
	}
	c.DatabaseURL = os.Getenv("DATABASE_URL")
	c.BotToken = os.Getenv("TELEGRAM_BOT_TOKEN")
	c.AIKey = os.Getenv("AI_API_KEY")
	c.AIBaseURL = os.Getenv("AI_BASE_URL")
	c.WebhookSecret = os.Getenv("WEBHOOK_SECRET")
	if v := os.Getenv("HEALTH_ADDR"); v != "" {
		c.HealthAddr = v
	}
	if v := os.Getenv("AI_MODEL"); v != "" {
		c.AI.Model = v
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}
func (c *Config) Validate() error {
	if c.Telegram.Mode != ModePolling {
		return fmt.Errorf("telegram.mode: only polling is implemented; webhook unavailable")
	}
	if c.DatabaseURL == "" {
		return fmt.Errorf("DATABASE_URL required")
	}
	if c.BotToken == "" {
		return fmt.Errorf("TELEGRAM_BOT_TOKEN required")
	}
	if (len(c.Telegram.ChatIDs) == 0 && !c.Telegram.AutoDiscoverChats) || c.Telegram.AdminChatID == 0 || len(c.Telegram.AllowedAdminIDs) == 0 {
		return fmt.Errorf("configured chats or auto discovery, admin destination and allowlisted admin IDs required")
	}
	seen := map[int64]bool{}
	for _, id := range c.Telegram.ChatIDs {
		if id >= 0 || seen[id] || id == c.Telegram.AdminChatID {
			return fmt.Errorf("managed chat IDs must be distinct groups, separate from admin destination")
		}
		seen[id] = true
	}
	for _, id := range c.Telegram.AllowedAdminIDs {
		if id <= 0 {
			return fmt.Errorf("admin user IDs must be positive")
		}
	}
	if c.Telegram.AdminChatID > 0 {
		allowed := false
		for _, id := range c.Telegram.AllowedAdminIDs {
			allowed = allowed || id == c.Telegram.AdminChatID
		}
		if !allowed {
			return fmt.Errorf("private admin_chat_id must be in allowed_admin_ids")
		}
	}
	m := c.Moderation
	if !m.WatchMessageEdits {
		return fmt.Errorf("watch_message_edits must stay enabled; mandatory signals may arrive in edits")
	}
	if m.EnforcementEnabled {
		return fmt.Errorf("enforcement_enabled must stay false: observe-only")
	}
	if !versionPattern.MatchString(m.RulesetVersion) {
		return fmt.Errorf("ruleset_version must be 1..80 ASCII letters/digits/dots/underscores/hyphens")
	}
	if m.RiskAIThreshold < 1 || m.RiskAIThreshold > 100 || m.AlertThreshold < 1 || m.AlertThreshold > 100 {
		return fmt.Errorf("risk/alert thresholds must be 1..100")
	}
	if len(m.ScoreThresholds) != 4 {
		return fmt.Errorf("score_thresholds requires four values")
	}
	for i, v := range m.ScoreThresholds {
		if v < 1 || v > 100 || (i > 0 && v <= m.ScoreThresholds[i-1]) {
			return fmt.Errorf("score_thresholds must ascend within 1..100")
		}
	}
	if len(m.ReportAliases) == 0 {
		return fmt.Errorf("at least one report alias required")
	}
	knownWeights := make(map[string]bool)
	for _, code := range rules.AllowedCodes() {
		knownWeights[code] = true
	}
	for code, v := range m.Weights {
		known := knownWeights[code]
		if !known || code == "" || v < 0 || v > 100 {
			return fmt.Errorf("invalid rule weight")
		}
	}
	if c.Trust.MaximumRiskDiscount < 0 || c.Trust.MaximumRiskDiscount > 20 || c.Trust.AdminReplyBonusCap < 0 || c.Trust.AdminReplyBonusCap > 15 || c.Trust.StatsWindowDays < 1 || c.Trust.StatsWindowDays > 90 {
		return fmt.Errorf("trust caps: discount <=20, admin bonus <=15, window 1..90")
	}
	a := c.AI
	if !a.MandatoryJobsBypassOptionalBudget {
		return fmt.Errorf("mandatory AI jobs must bypass optional budget")
	}
	if a.TimeoutSeconds < 1 || a.TimeoutSeconds > 120 || a.MaxConcurrent < 1 || a.MaxConcurrent > 16 || a.OptionalDailyRequestLimit < 0 {
		return fmt.Errorf("invalid AI timeout, concurrency or budget")
	}
	if a.Enabled {
		u, err := url.Parse(c.AIBaseURL)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
			return fmt.Errorf("AI_BASE_URL must be an HTTP(S) endpoint without credentials/query")
		}
		if u.Scheme == "http" && u.Hostname() != "localhost" && !net.ParseIP(u.Hostname()).IsLoopback() {
			return fmt.Errorf("plaintext AI transport permitted only on loopback")
		}
		if !review.ValidModelID(a.Model) || a.Provider != "openai_compatible" {
			return fmt.Errorf("AI model and openai_compatible provider required")
		}
	}
	if math.IsNaN(a.InputPricePerMillion) || math.IsInf(a.InputPricePerMillion, 0) || math.IsNaN(a.OutputPricePerMillion) || math.IsInf(a.OutputPricePerMillion, 0) || a.InputPricePerMillion < 0 || a.OutputPricePerMillion < 0 {
		return fmt.Errorf("AI prices cannot be negative")
	}
	r := c.Retention
	if r.MaxRecentMessagesPerChat < 1 || r.MaxRecentMessagesPerChat > 1000 || r.MessagesDays < 1 || r.MessagesDays > 7 || r.IncidentDays < 1 || r.IncidentDays > 365 || r.AggregateDays < 1 || r.AggregateDays > 365 {
		return fmt.Errorf("retention limits: recent 1..1000, messages 1..7d, incidents/aggregates 1..365d")
	}
	for _, list := range [][]string{c.Links.AllowDomains, c.Links.WatchDomains, c.Links.DenyDomains} {
		for _, host := range list {
			if host == "" || strings.ContainsAny(host, "/:* ") {
				return fmt.Errorf("domain lists require literal hostnames")
			}
		}
	}
	if len(c.Links.AllowDomains) == 0 {
		return fmt.Errorf("links.allow_domains required")
	}
	for _, name := range c.Links.KnownBots {
		if !urlscan.ValidBotUsername(name) {
			return fmt.Errorf("links.known_bots requires valid Telegram usernames without @")
		}
	}
	if c.Notifications.BatchWindowSeconds < 0 || c.Notifications.BatchWindowSeconds > 300 || c.Notifications.DuplicateCooldownSeconds < 0 {
		return fmt.Errorf("invalid notification timing")
	}
	if _, _, err := net.SplitHostPort(c.HealthAddr); err != nil {
		return fmt.Errorf("invalid HEALTH_ADDR")
	}
	if _, err := c.BuildAnalyzer(); err != nil {
		// Module errors may include untrusted dictionary values. Do not echo them into startup logs.
		return fmt.Errorf("analysis: invalid module, matcher, threshold, family cap or required-check settings")
	}
	return nil
}

func (c *Config) RuleConfig() rules.Config {
	r := rules.DefaultConfig()
	r.Version = c.Moderation.RulesetVersion
	r.AIThreshold = c.Moderation.RiskAIThreshold
	r.MaxTrustDiscount = c.Trust.MaximumRiskDiscount
	r.Thresholds = append([]int(nil), c.Moderation.ScoreThresholds...)
	for code, weight := range c.Moderation.Weights {
		r.Weights[code] = weight
	}
	return r
}

func (c *Config) BuildAnalyzer() (*rules.Analyzer, error) {
	return rules.NewAnalyzer(c.RuleConfig(), c.Analysis)
}

func (c *Config) AISubsystemTimeout() time.Duration {
	return time.Duration(c.AI.TimeoutSeconds) * time.Second
}
