package privacy

import (
	"encoding/json"
	"strings"
	"testing"

	"sentinel/internal/domain"
)

func TestExtractedFactsAreRedactedAndCopied(t *testing.T) {
	input := domain.Message{Mentions: []domain.Mention{{Username: "api_key=fixturevalue", BotStatus: "unknown"}}, URLs: []domain.URL{{QueryKeys: []string{"token=fixturevalue"}, TelegramUsername: "api_key=fixturevalue"}}}
	out := Message(input)
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "fixturevalue") || out.Mentions[0].BotStatus != "unverified" {
		t.Fatal("unsafe extracted facts", string(encoded))
	}
	out.URLs[0].QueryKeys[0] = "changed"
	out.Mentions[0].Username = "changed"
	if input.URLs[0].QueryKeys[0] == "changed" || input.Mentions[0].Username == "changed" {
		t.Fatal("privacy projection mutated original evidence")
	}
}

func TestBoundedExtractedFactsKeepPartialCoverage(t *testing.T) {
	input := domain.Message{Mentions: make([]domain.Mention, 40), URLs: []domain.URL{{QueryKeys: make([]string, 40)}}}
	out := Message(input)
	if len(out.Mentions) != 32 || len(out.URLs[0].QueryKeys) != 32 || !out.FeaturesPartial || !out.URLs[0].QueryKeysPartial {
		t.Fatal("facts truncated without preserving uncertainty")
	}
}
