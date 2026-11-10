package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"sentinel/internal/app"
	"sentinel/internal/domain"
	"sentinel/internal/rules"
)

func runOutput(t *testing.T, args ...string) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdout")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = file
	defer func() { os.Stdout = old }()
	err = run(context.Background(), args)
	if closeErr := file.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writeFixture(t *testing.T, name, data string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOfflineCommandsUseConfiguredModulesWithoutCredentials(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("AI_API_KEY", "")
	policy := writeFixture(t, "policy.yaml", `telegram:
  chat_ids: [-1001111111111]
  admin_chat_id: 111
  allowed_admin_ids: [111]
moderation:
  ruleset_version: test-cli-v2
analysis:
  modules:
    referral_cues:
      enabled: true
      matchers:
        query_keys:
          mode: exact
          terms: [partner]
`)
	fixture := writeFixture(t, "update.json", `{"update_id":1,"message":{"message_id":1,"date":1,"from":{"id":123},"chat":{"id":-1001111111111,"type":"supergroup"},"text":"https://github.com/docs?PARTNER=fixture"}}`)
	data := runOutput(t, "inspect", "--config", policy, "--file", fixture)
	var result struct {
		Message    domain.Message
		Assessment domain.Assessment
		AI         string
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, signal := range result.Assessment.Signals {
		found = found || signal.Code == "REFERRAL_REVIEW"
	}
	if !found || result.Assessment.Version != "test-cli-v2" || result.Message.FeaturesVersion != domain.CurrentFeaturesVersion || result.AI != "not called (offline inspect)" {
		t.Fatal(result)
	}
	dataset := writeFixture(t, "samples.jsonl", `{"text":"fixture","urls":[{"query_keys":["partner"]}],"features_version":2,"label":"benign"}`+"\n")
	data = runOutput(t, "replay", "--config", policy, "--file", dataset)
	var replay app.ReplayResult
	if err := json.Unmarshal(data, &replay); err != nil {
		t.Fatal(err)
	}
	if replay.AIRequired != 1 || replay.IncompleteFeatures != 0 {
		t.Fatal(replay)
	}
}

func TestModulesCommandListsSingleCatalogWithoutRuntimeSetup(t *testing.T) {
	data := runOutput(t, "modules")
	var specs []rules.Spec
	if err := json.Unmarshal(data, &specs); err != nil {
		t.Fatal(err)
	}
	if len(specs) != len(rules.Catalog()) {
		t.Fatal("CLI catalog drift")
	}
	for _, spec := range specs {
		if spec.ID == "referral_cues" {
			return
		}
	}
	t.Fatal("new module missing from registry command")
}
