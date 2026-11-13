package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"sentinel/internal/config"
	"sentinel/internal/domain"
	"sentinel/internal/rules"
)

func referralOptions() rules.Options {
	enabled := true
	return rules.Options{Modules: map[string]rules.ModuleConfig{"referral_cues": {Enabled: &enabled}}}
}

func TestNewEngineRejectsInvalidModules(t *testing.T) {
	c := config.Defaults()
	disabled := false
	c.Analysis = rules.Options{Modules: map[string]rules.ModuleConfig{"links": {Enabled: &disabled}}}
	if engine, err := NewEngine(nil, c, nil, "fixture", ""); err == nil || engine != nil {
		t.Fatal("invalid module settings did not fail construction")
	}
}

func TestReferralModulesCreateOneDurableJobAcrossRestartAndEdit(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	e.Config.AI.Enabled = false
	e.Config.AI.OptionalDailyRequestLimit = 0
	m := msg(1, 123, "Привет")
	deliver(t, e, domain.Update{ID: 1, Message: &m})
	if err := e.RegisterRules(ctx); err != nil {
		t.Fatal(err)
	}
	updated := *e.Config
	updated.Moderation.RulesetVersion = "test-modular-ref-v2"
	updated.Analysis = referralOptions()
	e = mustEngine(t, e.Store, &updated, e.Admins, e.BotID, e.Username)
	if err := e.RegisterRules(ctx); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		text string
		job  bool
	}{
		{"https://github.com/docs?REF=test", true},
		{"😀 @Example_REF123_bot", true},
		{"https://github.com/docs?reference=docs", false},
		{"@reference_bot", false},
		{"https://github.com/docs?utm_source=ref", false},
		{"https://t.me/example_ref_bot?ref=test&start=ref123", true},
		{"https://github.com/docs?ref=%ZZ", true},
		{"https://github.com/docs?ref=abc;tag", true},
	}
	for i, tc := range cases {
		id := int64(i + 2)
		m = msg(id, 123, tc.text)
		u := domain.Update{ID: id, Message: &m}
		deliver(t, e, u)
		deliver(t, e, u)
		want := int64(0)
		if tc.job {
			want = 1
		}
		if got := number(t, e, `SELECT count(*) FROM ai_jobs WHERE message_id=$1 AND priority='mandatory' AND status='pending'`, id); got != want {
			t.Fatal("referral policy or job dedup failed", i, got, want)
		}
	}
	if got := number(t, e, `SELECT count(*) FROM ai_jobs`); got != 6 {
		t.Fatal("modules made duplicate paid jobs", got)
	}
	if worked, err := e.ReviewOne(ctx, providerFake{}); err != nil || worked {
		t.Fatal("AI disabled did not leave mandatory jobs pending", worked, err)
	}
	if got := number(t, e, `SELECT count(*) FROM ai_jobs WHERE payload->'assessment'->>'ruleset_version'='v1'`); got != 1 {
		t.Fatal("new policy silently rewrote queued v1 evidence", got)
	}
	e = mustEngine(t, e.Store, e.Config, e.Admins, e.BotID, e.Username)
	m = msg(2, 123, "https://github.com/docs?ref=edited")
	edit := time.Now().UTC().Add(time.Second)
	m.EditedAt = &edit
	deliver(t, e, domain.Update{ID: 100, Message: &m})
	if got := number(t, e, `SELECT count(*) FROM ai_jobs WHERE message_id=2`); got != 2 {
		t.Fatal("edited referral lost revision or pending original", got)
	}
	job, err := e.Store.ClaimAI(ctx, 0, time.Minute, e.Config.AI.Model)
	if err != nil || job == nil || job.Priority != "mandatory" {
		t.Fatal("mandatory jobs could not bypass optional budget", job, err)
	}
}

func TestIncompleteURLCoverageCreatesDurableMandatoryJobWithoutReferralModule(t *testing.T) {
	e := testEngine(t)
	e.Config.AI.Enabled = false
	e.Config.AI.OptionalDailyRequestLimit = 0
	first := msg(1, 123, "Привет")
	deliver(t, e, domain.Update{ID: 1, Message: &first})
	m := msg(2, 123, strings.Repeat("hxxps://github[.]com/docs ", 32)+"hxxps://unknown[.]example/")
	deliver(t, e, domain.Update{ID: 2, Message: &m})
	if got := number(t, e, `SELECT count(*) FROM ai_jobs WHERE message_id=2 AND priority='mandatory' AND status='pending' AND payload->'message'->>'urls_partial'='true'`); got != 1 {
		t.Fatal("incomplete URL extraction lost durable mandatory review", got)
	}
}

func TestResolvedModuleSettingsArePartOfRulesetSnapshot(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	if err := e.RegisterRules(ctx); err != nil {
		t.Fatal(err)
	}
	updated := *e.Config
	updated.Analysis = referralOptions()
	changed := mustEngine(t, e.Store, &updated, e.Admins, e.BotID, e.Username)
	if err := changed.RegisterRules(ctx); err == nil {
		t.Fatal("changed module policy reused registered version")
	}
}

func TestQueuedUnknownFeatureVersionRequiresReviewWithoutReparsing(t *testing.T) {
	for _, version := range []int{0, domain.CurrentFeaturesVersion + 1} {
		t.Run(fmt.Sprintf("version_%d", version), func(t *testing.T) {
			e := testEngine(t)
			ctx := context.Background()
			first := msg(1, 123, "Привет")
			deliver(t, e, domain.Update{ID: 1, Message: &first})

			prefix := "😀 password=synthetic_fixture_value "
			m := msg(2, 123, prefix+"@example_ref_bot")
			m.Entities = []domain.Entity{{Type: "mention", Offset: len(utf16.Encode([]rune(prefix))), Length: len("@example_ref_bot")}}
			u := prepared(t, e, domain.Update{ID: 2, Message: &m})
			if u.Message.Text == m.Text {
				t.Fatal("fixture did not change entity offsets through redaction")
			}
			// Model an already-durable payload, not a fresh update for Prepare.
			u.Message.FeaturesVersion = version
			u.Message.FeaturesPartial = false
			u.Message.Mentions = nil
			if err := e.Store.Ingest(ctx, e.BotID, u); err != nil {
				t.Fatal(err)
			}
			updated := *e.Config
			updated.Moderation.RulesetVersion = "test-queued-feature-upgrade"
			updated.Analysis = referralOptions()
			e = mustEngine(t, e.Store, &updated, e.Admins, e.BotID, e.Username)
			if worked, err := e.ProcessOne(ctx); err != nil || !worked {
				t.Fatal("queued evidence did not survive engine upgrade", worked, err)
			}
			var raw []byte
			if err := e.Store.Pool.QueryRow(ctx, `SELECT payload FROM risk_assessments WHERE message_id=2`).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var assessment domain.Assessment
			if err := json.Unmarshal(raw, &assessment); err != nil {
				t.Fatal(err)
			}
			if len(assessment.Signals) != 1 || assessment.Signals[0].Code != "REFERRAL_REVIEW" || !strings.Contains(assessment.Signals[0].Evidence, "coverage partial") || !assessment.Mandatory {
				t.Fatal("unknown features were treated as complete or as an established referral")
			}
			if got := number(t, e, `SELECT count(*) FROM ai_jobs WHERE message_id=2 AND priority='mandatory' AND status='pending' AND payload->'message'->>'features_partial'='true' AND NOT (payload->'message' ? 'mentions')`); got != 1 {
				t.Fatal("upgrade dropped review or reparsed redacted evidence", got)
			}
		})
	}
}

func TestFingerprintIgnoresDerivedFeatureMetadata(t *testing.T) {
	m := msg(1, 123, "Пример @example_ref_bot")
	before := fingerprint(m)
	m.Mentions = []domain.Mention{{Username: "example_ref_bot", BotStatus: "heuristic"}}
	m.FeaturesVersion = domain.CurrentFeaturesVersion
	m.FeaturesPartial = true
	m.URLs = []domain.URL{{QueryKeys: []string{"ref"}}}
	if fingerprint(m) != before {
		t.Fatal("extractor metadata became a message edit")
	}
}

func TestReplaySharesModulePolicyAndReportsIncompleteFeatures(t *testing.T) {
	c := rules.DefaultConfig()
	analyzer, err := rules.NewAnalyzer(c, referralOptions())
	if err != nil {
		t.Fatal(err)
	}
	samples := []Sample{
		{Text: "test", URLs: []domain.URL{{QueryKeys: []string{"ref"}}}, Label: "benign", FeaturesVersion: domain.CurrentFeaturesVersion},
		{Text: "legacy example", Label: "benign"},
		{Text: "partial", FeaturesVersion: domain.CurrentFeaturesVersion, FeaturesPartial: true},
		{Text: "future format", FeaturesVersion: domain.CurrentFeaturesVersion + 1},
	}
	var input, output strings.Builder
	for _, sample := range samples {
		if err := json.NewEncoder(&input).Encode(sample); err != nil {
			t.Fatal(err)
		}
	}
	if err := ReplayWithAnalyzer(strings.NewReader(input.String()), &output, analyzer, c.AIThreshold); err != nil {
		t.Fatal(err)
	}
	var result ReplayResult
	if err := json.Unmarshal([]byte(output.String()), &result); err != nil {
		t.Fatal(err)
	}
	if result.AIRequired != 4 || result.IncompleteFeatures != 3 {
		t.Fatal("replay ignored configured modules or invented complete coverage", result)
	}
}
