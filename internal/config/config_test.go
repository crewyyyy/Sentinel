package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadTest(t *testing.T, s string) (*Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(s), 0600); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}
func testEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("TELEGRAM_BOT_TOKEN", "123:fake")
	t.Setenv("AI_BASE_URL", "http://127.0.0.1:9/v1")
	t.Setenv("AI_MODEL", "test")
}

const validYAML = `telegram:
  mode: polling
  chat_ids: [-1001111111111]
  admin_chat_id: -1002222222222
  allowed_admin_ids: [111]
`

func TestLoad(t *testing.T) {
	testEnv(t)
	c, err := loadTest(t, validYAML)
	if err != nil {
		t.Fatal(err)
	}
	if c.Retention.MaxRecentMessagesPerChat != 1000 || c.AI.Model != "test" {
		t.Fatal(c)
	}
}
func TestRejectUnsafe(t *testing.T) {
	testEnv(t)
	for _, s := range []string{
		strings.Replace(validYAML, "mode: polling", "mode: webhook", 1),
		validYAML + "moderation:\n  enforcement_enabled: true\n",
		validYAML + "trust:\n  maximum_risk_discount: 21\n",
		validYAML + "ai:\n  mandatory_jobs_bypass_optional_budget: false\n",
		validYAML + "retention:\n  max_recent_messages_per_chat: 1001\n",
		validYAML + "typo: true\n",
	} {
		if _, err := loadTest(t, s); err == nil {
			t.Fatalf("accepted unsafe config %s", s)
		}
	}
}
func TestBoundedSingleDocumentAndSecretSafeErrors(t *testing.T) {
	testEnv(t)
	for _, input := range []string{
		validYAML + "ai:\n  max_concurrent: fixture-sensitive-value\n",
		validYAML + "---\ntelegram: {}\n",
		strings.Repeat("x", (64<<10)+1),
	} {
		_, err := loadTest(t, input)
		if err == nil {
			t.Fatal("accepted invalid, multiple-document or oversized configuration")
		}
		if strings.Contains(err.Error(), "fixture-sensitive-value") {
			t.Fatal("configuration error exposed a scalar value")
		}
	}
}

func TestAutoDiscoveryAllowsEmptyChatIDsOnlyWhenEnabled(t *testing.T) {
	testEnv(t)
	input := strings.Replace(validYAML, "chat_ids: [-1001111111111]", "chat_ids: []", 1)
	c, err := loadTest(t, input)
	if err != nil || len(c.Telegram.ChatIDs) != 0 || !c.Telegram.AutoDiscoverChats {
		t.Fatal(c, err)
	}
	input = strings.Replace(input, "mode: polling", "mode: polling\n  auto_discover_chats: false", 1)
	if _, err := loadTest(t, input); err == nil {
		t.Fatal("empty managed chats without discovery accepted")
	}
}

func TestMissingSecrets(t *testing.T) {
	testEnv(t)
	t.Setenv("DATABASE_URL", "")
	if _, err := loadTest(t, validYAML); err == nil {
		t.Fatal("accepted missing db")
	}
}
