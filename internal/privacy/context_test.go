package privacy

import (
	"encoding/json"
	"strings"
	"testing"

	"sentinel/internal/domain"
	"sentinel/internal/rules"
	"sentinel/internal/urlscan"
)

func TestDetachedFactsDoNotRecoverContextuallyRedactedCredentials(t *testing.T) {
	const name = "synthetic_ref_bot"
	scanner := urlscan.New(nil, nil, nil, []string{name})
	enabled := true
	analyzer, err := rules.NewAnalyzer(rules.DefaultConfig(), rules.Options{Modules: map[string]rules.ModuleConfig{"referral_cues": {Enabled: &enabled}}})
	if err != nil {
		t.Fatal(err)
	}
	entity := domain.Entity{Type: "mention", Offset: len("password="), Length: len("@" + name)}
	for _, input := range []domain.Message{
		{Text: "password=@" + name, Entities: []domain.Entity{entity}},
		{Caption: "password=@" + name, CaptionEntities: []domain.Entity{entity}},
		{QuoteText: "password=@" + name, QuoteEntities: []domain.Entity{entity}},
		{Text: "password=https://t.me/" + name},
		{Text: "password=https://github.com/?" + name + "=x"},
	} {
		extracted := scanner.Extract(input)
		safe := Message(extracted)
		assessment := analyzer.Assess(safe, safe.URLs, false, 100)
		encoded, err := json.Marshal(struct {
			Message    domain.Message
			Assessment domain.Assessment
		}{safe, assessment})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), name) || !safe.FeaturesPartial || !assessment.Mandatory {
			t.Fatal("detached facts recovered a credential or lost review")
		}
		if len(safe.Mentions) != 0 || len(safe.URLs) != 0 {
			t.Fatal("redacted source retained detached identity facts")
		}
		if input.Text != extracted.Text || input.Caption != extracted.Caption || input.QuoteText != extracted.QuoteText {
			t.Fatal("privacy mutated original source")
		}
	}
}
