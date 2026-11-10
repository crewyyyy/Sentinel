package rules_test

import (
	"testing"

	"sentinel/internal/domain"
	"sentinel/internal/rules"
)

func TestReferralCuesBoundariesAndEvidence(t *testing.T) {
	a := compile(t, rules.Options{Modules: map[string]rules.ModuleConfig{"referral_cues": {Enabled: enabled(true)}}})
	cases := []struct {
		name       string
		m          domain.Message
		urls       []domain.URL
		want       bool
		confidence float64
	}{
		{name: "exact ref", urls: []domain.URL{{QueryKeys: []string{"ref"}}}, want: true, confidence: 1},
		{name: "casefold referral", urls: []domain.URL{{QueryKeys: []string{"ReFeRRaL"}}}, want: true, confidence: 1},
		{name: "affiliate", urls: []domain.URL{{QueryKeys: []string{"affiliate"}}}, want: true, confidence: 1},
		{name: "reference is not ref", urls: []domain.URL{{QueryKeys: []string{"reference"}}}},
		{name: "ref suffix is not exact key", urls: []domain.URL{{QueryKeys: []string{"ref_id", "myref", "utm_ref"}}}},
		{name: "start alone is not referral cue", urls: []domain.URL{{QueryKeys: []string{"start"}, Telegram: true, Bot: true}}},
		{name: "legacy referral flag alone is not new cue", urls: []domain.URL{{Referral: true, QueryKeys: []string{"utm_source"}}}},
		{name: "unrelated plain text excluded", m: domain.Message{Text: "@my_ref_bot?ref=123"}},
		{name: "bot URL digits", urls: []domain.URL{{BotHeuristic: true, TelegramUsername: "my_REF123_bot"}}, want: true, confidence: .8},
		{name: "confirmed bot URL", urls: []domain.URL{{Bot: true, TelegramUsername: "my_referral_bot"}}, want: true, confidence: 1},
		{name: "reference bot excluded", urls: []domain.URL{{BotHeuristic: true, TelegramUsername: "reference_bot"}}},
		{name: "prefix bot excluded", urls: []domain.URL{{BotHeuristic: true, TelegramUsername: "prefix_bot"}}},
		{name: "embedded ref excluded", urls: []domain.URL{{BotHeuristic: true, TelegramUsername: "myrefbot"}}},
		{name: "unverified bot name excluded", urls: []domain.URL{{Telegram: true, TelegramUsername: "my_ref_bot"}}},
		{name: "confirmed mention", m: domain.Message{Mentions: []domain.Mention{{Username: "ref123_bot", BotStatus: "confirmed", Source: "text"}}}, want: true, confidence: 1},
		{name: "heuristic caption mention", m: domain.Message{Mentions: []domain.Mention{{Username: "my_REF_bot", BotStatus: "heuristic", Source: "caption"}}}, want: true, confidence: .8},
		{name: "heuristic quote mention", m: domain.Message{Mentions: []domain.Mention{{Username: "my_referral_bot", BotStatus: "heuristic", Source: "quote"}}}, want: true, confidence: .8},
		{name: "unknown mention excluded", m: domain.Message{Mentions: []domain.Mention{{Username: "my_ref_bot", BotStatus: "unverified"}}}},
		{name: "no status mention excluded", m: domain.Message{Mentions: []domain.Mention{{Username: "my_ref_bot"}}}},
		{name: "reference mention excluded", m: domain.Message{Mentions: []domain.Mention{{Username: "reference_bot", BotStatus: "confirmed"}}}},
		{name: "partial query coverage", urls: []domain.URL{{QueryKeys: []string{"reference"}, QueryKeysPartial: true}}, want: true, confidence: .5},
		{name: "partial mention coverage", m: domain.Message{FeaturesPartial: true}, want: true, confidence: .5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := a.Assess(tc.m, tc.urls, false, 100)
			signal, found := signalByCode(got, "REFERRAL_REVIEW")
			if found != tc.want {
				t.Fatalf("referral cue=%v, want %v: %+v", found, tc.want, got)
			}
			if found && (signal.Weight != 0 || !signal.MandatoryAI || signal.Confidence != tc.confidence || !got.Mandatory || !got.ReviewRequired) {
				t.Fatalf("new policy is not review-only: %+v", got)
			}
			legacy := compile(t, rules.Options{}).Assess(tc.m, tc.urls, false, 100)
			assertCode(t, legacy, "REFERRAL_REVIEW", false)
		})
	}
}

func TestReferralDeclarativeOverridesAndDedup(t *testing.T) {
	a := compile(t, rules.Options{Modules: map[string]rules.ModuleConfig{"referral_cues": {
		Enabled: enabled(true), Matchers: map[string]rules.MatcherConfig{
			"query_keys": {Terms: []string{"partner"}}, "bot_components": {Mode: "exact", Terms: []string{"deal"}},
		},
	}}})
	for _, tc := range []struct {
		m    domain.Message
		urls []domain.URL
		want bool
	}{
		{urls: []domain.URL{{QueryKeys: []string{"PARTNER"}}}, want: true},
		{urls: []domain.URL{{QueryKeys: []string{"ref", "referral"}}}},
		{urls: []domain.URL{{BotHeuristic: true, TelegramUsername: "deal123_bot"}}, want: true},
		{urls: []domain.URL{{BotHeuristic: true, TelegramUsername: "dealing_bot"}}},
		{m: domain.Message{Mentions: []domain.Mention{{Username: "deal_bot", BotStatus: "heuristic"}}}, want: true},
	} {
		assertCode(t, a.Assess(tc.m, tc.urls, false, 0), "REFERRAL_REVIEW", tc.want)
	}

	a = compile(t, rules.Options{Modules: map[string]rules.ModuleConfig{"referral_cues": {Enabled: enabled(true)}}})
	urls := []domain.URL{{Referral: true, QueryKeys: []string{"ref", "referral"}, Value: "https://example.test/?ref=x"}, {QueryKeys: []string{"ref"}}}
	got := a.Assess(domain.Message{Mentions: []domain.Mention{{Username: "ref_bot", BotStatus: "confirmed"}}}, urls, false, 0)
	count := 0
	for _, signal := range got.Signals {
		if signal.Code == "REFERRAL_REVIEW" {
			count++
			if signal.Evidence != "referral query key: ref" {
				t.Fatal(signal)
			}
		}
	}
	if count != 1 {
		t.Fatalf("duplicate referral review signal: %+v", got.Signals)
	}
	assertCode(t, got, "REFERRAL_LINK", true)
	if got.Score != 22 {
		t.Fatalf("review cue changed legacy referral weight: %+v", got)
	}
}
