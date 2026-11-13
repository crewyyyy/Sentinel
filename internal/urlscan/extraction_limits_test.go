package urlscan_test

import (
	"strings"
	"testing"

	"sentinel/internal/domain"
	"sentinel/internal/privacy"
	"sentinel/internal/rules"
	"sentinel/internal/urlscan"
)

func TestDiscardedLinkCandidatesAlwaysRequireReview(t *testing.T) {
	scanner := urlscan.New([]string{"github.com"}, nil, nil, nil)
	for _, last := range []string{"hxxps://github[.]com/docs?ref=x", "hxxps://unknown[.]example/", "t[.]me/example_ref_bot"} {
		m := scanner.Extract(domain.Message{Text: strings.Repeat("hxxps://github[.]com/docs ", 32) + last})
		if len(m.URLs) != 1 || !m.URLsPartial || !m.FeaturesPartial {
			t.Fatal("dedup concealed discarded candidates")
		}
		for _, enabled := range []bool{false, true} {
			analyzer, err := rules.NewAnalyzer(rules.DefaultConfig(), rules.Options{Modules: map[string]rules.ModuleConfig{"referral_cues": {Enabled: &enabled}}})
			if err != nil {
				t.Fatal(err)
			}
			m = privacy.Message(m)
			assessment := analyzer.Assess(m, m.URLs, false, 100)
			if !assessment.Mandatory || !assessment.ReviewRequired || !hasSignal(assessment, "URL_SCAN_LIMIT") {
				t.Fatal("incomplete destination coverage lost mandatory review", enabled)
			}
		}
	}
}

func TestOtherDiscardedLinkEvidenceMarksCoveragePartial(t *testing.T) {
	scanner := urlscan.New([]string{"github.com"}, nil, nil, nil)
	entities := make([]domain.Entity, 33)
	for i := range entities {
		entities[i] = domain.Entity{Type: "text_link", URL: "https://github.com/docs"}
	}
	for _, input := range []domain.Message{
		{Text: "documentation", Entities: entities},
		{Text: "https://github.com/docs?x=" + strings.Repeat("x", 4096)},
		{Text: "https://github.com/%ZZ"},
		{Text: "url", Entities: []domain.Entity{{Type: "url", Offset: 100, Length: 10}}},
	} {
		m := scanner.Extract(input)
		if !m.URLsPartial || !m.FeaturesPartial {
			t.Fatal("discarded link evidence treated as complete")
		}
	}
}

func TestMalformedQueriesRequireReferralReview(t *testing.T) {
	scanner := urlscan.New([]string{"github.com"}, nil, nil, nil)
	enabled := true
	analyzer, err := rules.NewAnalyzer(rules.DefaultConfig(), rules.Options{Modules: map[string]rules.ModuleConfig{"referral_cues": {Enabled: &enabled}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"ref=abc;tag", "ref=%ZZ"} {
		m := privacy.Message(scanner.Extract(domain.Message{Text: "https://github.com/docs?" + query}))
		if len(m.URLs) != 1 || !m.URLs[0].QueryKeysPartial || !m.FeaturesPartial {
			t.Fatal("query parse failure lost coverage uncertainty")
		}
		assessment := analyzer.Assess(m, m.URLs, false, 100)
		if !assessment.Mandatory || !hasSignal(assessment, "REFERRAL_REVIEW") {
			t.Fatal("malformed query bypassed review")
		}
		for _, signal := range assessment.Signals {
			if signal.Code == "REFERRAL_REVIEW" && !strings.Contains(signal.Evidence, "coverage partial") {
				t.Fatal("malformed query was presented as an established referral")
			}
		}
	}
}

func hasSignal(assessment domain.Assessment, code string) bool {
	for _, signal := range assessment.Signals {
		if signal.Code == code {
			return true
		}
	}
	return false
}
