package urlscan

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"

	"sentinel/internal/domain"
)

func TestExtractMentionsBeforeRedactionWithUTF16Provenance(t *testing.T) {
	s := New(nil, nil, nil, []string{"example_ref_bot"})
	handle := "@Example_REF_bot"
	m := domain.Message{
		Text: "😀 " + handle + " @unverified_name", Caption: "@Other_REF12_bot", QuoteText: "@Reference_bot",
		Entities: []domain.Entity{{Type: "mention", Offset: 3, Length: len(utf16.Encode([]rune(handle)))}},
	}
	got := s.Extract(m)
	if len(got.Mentions) != 4 || got.FeaturesVersion != domain.CurrentFeaturesVersion || got.FeaturesPartial {
		t.Fatal(got.Mentions, got.FeaturesVersion, got.FeaturesPartial)
	}
	first := got.Mentions[0]
	if first.Username != "example_ref_bot" || first.BotStatus != "confirmed" || first.Offset != 3 || first.Source != "text" {
		t.Fatal(first)
	}
	if got.Mentions[1].BotStatus != "unverified" || got.Mentions[2].BotStatus != "heuristic" || got.Mentions[2].Source != "caption" || got.Mentions[3].Source != "quote" {
		t.Fatal(got.Mentions)
	}
	if m.Text != got.Text || m.Mentions != nil {
		t.Fatal("extractor mutated input or original content")
	}
}

func TestMentionExtractionRejectsEmailsAndPartialNames(t *testing.T) {
	s := New(nil, nil, nil, nil)
	m := s.Extract(domain.Message{Text: "person@reference_bot.example @@example_ref_bot @" + strings.Repeat("a", 33) + "_ref_bot"})
	if len(m.Mentions) != 0 {
		t.Fatal(m.Mentions)
	}
	invalid := s.Extract(domain.Message{Text: "😀@example_ref_bot", Entities: []domain.Entity{{Type: "mention", Offset: 1, Length: 15}}})
	if len(invalid.Mentions) != 1 || invalid.Mentions[0].Offset != 2 {
		t.Fatal("bad entity offset corrupted fallback extraction", invalid.Mentions)
	}
}

func TestCanonicalQueryKeysDoNotRetainValuesOrInventBotIdentity(t *testing.T) {
	s := New([]string{"github.com"}, nil, nil, nil)
	m := s.Extract(domain.Message{Text: "https://github.com/docs?REF=privatevalue&ref=other&reference=docs&utm_source=test&ref%5B%5D=x https://t.me/Example_REF_bot https://t.me/addemoji/pack https://t.me/reference"})
	if len(m.URLs) != 4 {
		t.Fatal(m.URLs)
	}
	if !reflect.DeepEqual(m.URLs[0].QueryKeys, []string{"ref", "ref[]", "reference", "utm_source"}) {
		t.Fatal(m.URLs[0].QueryKeys)
	}
	if strings.Contains(strings.Join(m.URLs[0].QueryKeys, ","), "privatevalue") {
		t.Fatal("query values leaked into structural facts")
	}
	if m.URLs[1].TelegramUsername != "example_ref_bot" || m.URLs[1].Bot || !m.URLs[1].BotHeuristic || m.URLs[2].TelegramUsername != "" || m.URLs[3].Bot || m.URLs[3].BotHeuristic {
		t.Fatal(m.URLs)
	}
}

func TestFeatureTruncationIsExplicit(t *testing.T) {
	s := New([]string{"github.com"}, nil, nil, nil)
	keys := []string{}
	for i := 0; i < 40; i++ {
		keys = append(keys, fmt.Sprintf("a%02d=x", i))
	}
	m := s.Extract(domain.Message{Text: "https://github.com/docs?" + strings.Join(keys, "&") + "&ref=x"})
	if len(m.URLs) != 1 || len(m.URLs[0].QueryKeys) != maxQueryKeys || !m.URLs[0].QueryKeysPartial || !m.FeaturesPartial {
		t.Fatal(m)
	}
	mentions := []string{}
	for i := 0; i < 40; i++ {
		mentions = append(mentions, fmt.Sprintf("@example_%d_bot", i))
	}
	m = s.Extract(domain.Message{Text: strings.Join(mentions, " ")})
	if len(m.Mentions) != maxMentions || !m.FeaturesPartial {
		t.Fatal("mention coverage silently truncated", len(m.Mentions), m.FeaturesPartial)
	}
}
