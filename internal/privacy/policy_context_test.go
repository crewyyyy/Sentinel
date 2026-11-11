package privacy

import (
	"strings"
	"testing"

	"sentinel/internal/domain"
	"sentinel/internal/rules"
	"sentinel/internal/urlscan"
)

func TestChangedTextCannotClaimCompletePolicyContext(t *testing.T) {
	enabled := true
	analyzer, err := rules.NewAnalyzer(rules.DefaultConfig(), rules.Options{Modules: map[string]rules.ModuleConfig{"sales": {Enabled: &enabled}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"password=synthetic_value", strings.Repeat("спокойный текст ", 1200) + " Продам доступ к API"} {
		m := Message(urlscan.New(nil, nil, nil, nil).Extract(domain.Message{Text: text}))
		if !m.FeaturesPartial {
			t.Fatal("modified text retained complete coverage")
		}
		a := analyzer.Assess(m, m.URLs, false, 100)
		if !a.Mandatory || !a.ReviewRequired {
			t.Fatal("truncation or redaction silently lost policy coverage")
		}
	}
}
