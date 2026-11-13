package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sentinel/internal/domain"
)

func TestAnalysisYAMLBuildsSharedAnalyzer(t *testing.T) {
	testEnv(t)
	c, err := loadTest(t, validYAML+`moderation:
  ruleset_version: test-modular-v2
analysis:
  modules:
    referral_cues:
      enabled: true
      matchers:
        query_keys:
          mode: exact
          terms: [ref, partner]
  family_caps:
    links: 0
`)
	if err != nil {
		t.Fatal(err)
	}
	analyzer, err := c.BuildAnalyzer()
	if err != nil {
		t.Fatal(err)
	}
	a := analyzer.Assess(domain.Message{}, []domain.URL{{QueryKeys: []string{"PARTNER"}}}, false, 100)
	if !a.Mandatory || !a.ReviewRequired || a.Score != 0 || a.Version != "test-modular-v2" {
		t.Fatal(a)
	}
}

func TestAnalysisYAMLRejectsUnsafeOrUnknownSettingsWithoutEchoingValues(t *testing.T) {
	testEnv(t)
	for _, extra := range []string{
		"analysis:\n  modules:\n    fixture-sensitive-value: {enabled: true}\n",
		"analysis:\n  modules:\n    links: {enabled: false}\n",
		"analysis:\n  modules:\n    observation: {enabled: false}\n",
		"analysis:\n  modules:\n    commerce: {enabled: false}\n",
		"analysis:\n  modules:\n    commerce:\n      matchers:\n        criticism: {terms: []}\n",
		"analysis:\n  modules:\n    commerce:\n      matchers:\n        criticism: {mode: fixture-sensitive-value}\n",
		"analysis:\n  modules:\n    links:\n      thresholds: {url_scan_limit: 33}\n",
		"analysis:\n  family_caps: {links: -1}\n",
		"analysis:\n  modules:\n    referral_cues:\n      unsupported: fixture-sensitive-value\n",
	} {
		if _, err := loadTest(t, validYAML+extra); err == nil {
			t.Fatal("accepted unsafe module settings")
		} else if strings.Contains(err.Error(), "fixture-sensitive-value") {
			t.Fatal("settings error leaked scalar value", err)
		}
	}
}

func TestCatalogSignalsAreValidWeightKeys(t *testing.T) {
	testEnv(t)
	if _, err := loadTest(t, validYAML+"moderation:\n  weights: {REFERRAL_REVIEW: 0, URL_SCAN_LIMIT: 0}\n"); err != nil {
		t.Fatal(err)
	}
}

func TestOfflinePolicyIgnoresRuntimeCredentials(t *testing.T) {
	testEnv(t)
	t.Setenv("DATABASE_URL", "fixture-private-db")
	t.Setenv("TELEGRAM_BOT_TOKEN", "fixture-private-token")
	path := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(path, []byte(validYAML), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.DatabaseURL != "" || c.BotToken != "" || c.AI.Model != "" || !c.AI.Enabled {
		t.Fatal("offline policy read credentials or mutated AI policy")
	}
}
