package urlscan

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"sentinel/internal/domain"
)

const maxMentions = 32

var plainMentions = regexp.MustCompile(`@[A-Za-z][A-Za-z0-9_]{4,31}`)

// Extract resolves entities against original text before redaction changes UTF-16 offsets.
// Its facts are independent of detector dictionaries, so changing policy never reinterprets offsets.
func (s *Scanner) Extract(m domain.Message) domain.Message {
	m.URLs, m.URLsPartial = s.scan(m)
	m.Mentions, m.FeaturesPartial = s.mentions(m)
	m.FeaturesPartial = m.FeaturesPartial || m.URLsPartial
	for _, u := range m.URLs {
		m.FeaturesPartial = m.FeaturesPartial || u.QueryKeysPartial
	}
	m.FeaturesVersion = domain.CurrentFeaturesVersion
	return m
}

func (s *Scanner) mentions(m domain.Message) ([]domain.Mention, bool) {
	out := []domain.Mention{}
	seen := map[string]bool{}
	partial := false
	add := func(raw, source string, offset, length int) {
		if !strings.HasPrefix(raw, "@") {
			return
		}
		name := strings.ToLower(strings.TrimPrefix(raw, "@"))
		if !username.MatchString(name) {
			return
		}
		key := fmt.Sprintf("%s:%d:%s", source, offset, name)
		if seen[key] {
			return
		}
		seen[key] = true
		if len(out) >= maxMentions {
			partial = true
			return
		}
		status := "unverified"
		if s.bots[name] {
			status = "confirmed"
		} else if strings.HasSuffix(name, "bot") {
			status = "heuristic"
		}
		out = append(out, domain.Mention{Username: name, Source: source, BotStatus: status, Offset: offset, Length: length})
	}
	scan := func(text, source string, entities []domain.Entity) {
		if len(entities) > maxMentions {
			partial = true
			entities = entities[:maxMentions]
		}
		for _, e := range entities {
			if e.Type == "mention" {
				if raw, ok := UTF16Slice(text, e.Offset, e.Length); ok {
					add(raw, source, e.Offset, e.Length)
				}
			}
		}
		positions := plainMentions.FindAllStringIndex(text, maxMentions+1)
		if len(positions) > maxMentions {
			partial = true
		}
		for _, position := range positions {
			start, end := position[0], position[1]
			if start > 0 {
				previous, _ := utf8.DecodeLastRuneInString(text[:start])
				if unicode.IsLetter(previous) || unicode.IsDigit(previous) || previous == '_' || previous == '@' {
					continue // Not a standalone mention, e.g. an email address.
				}
			}
			if end < len(text) {
				next, _ := utf8.DecodeRuneInString(text[end:])
				if unicode.IsLetter(next) || unicode.IsDigit(next) || next == '_' {
					continue // Do not turn an oversized username into a shorter valid one.
				}
			}
			add(text[start:end], source, len(utf16.Encode([]rune(text[:start]))), len(utf16.Encode([]rune(text[start:end]))))
		}
	}
	scan(m.Text, "text", m.Entities)
	scan(m.Caption, "caption", m.CaptionEntities)
	scan(m.QuoteText, "quote", m.QuoteEntities)
	return out, partial
}
