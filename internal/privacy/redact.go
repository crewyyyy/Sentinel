// Package privacy removes credentials before persistence, logging, or external review.
package privacy

import (
	"regexp"
	"sentinel/internal/domain"
	"strings"
	"unicode/utf8"
)

var credentials = regexp.MustCompile(`(?i)(\b(?:api[_ -]?key|token|password|passwd|secret|authorization|access_token|api_token)\b[\s"':=]+)([^\s"'&,;}]+)`)
var keys = regexp.MustCompile(`(?i)\b(?:fal_sk_[A-Za-z0-9_-]+:[A-Za-z0-9_-]+|[A-Za-z0-9_-]{24,}:[A-Za-z0-9_-]{24,}|sk-[A-Za-z0-9_-]{8,}|[0-9]{6,12}:[A-Za-z0-9_-]{20,}|Bearer\s+[A-Za-z0-9_.-]{8,})`)
var contact = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}|\+\d[\d ()-]{8,}\d`)

func Redact(s string) string {
	s = strings.ToValidUTF8(s, "")
	s = keys.ReplaceAllString(s, "[REDACTED]")
	s = credentials.ReplaceAllString(s, "${1}[REDACTED]")
	return contact.ReplaceAllString(s, "[PRIVATE]")
}
func Limit(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
func Message(m domain.Message) domain.Message {
	original := m
	m.Text = Limit(Redact(m.Text), 16384)
	m.Caption = Limit(Redact(m.Caption), 8192)
	m.QuoteText = Limit(Redact(m.QuoteText), 2048)
	if m.Text != original.Text || m.Caption != original.Caption || m.QuoteText != original.QuoteText {
		m.FeaturesPartial = true
		// Detached facts lose credential context (e.g. password=@name). Do not
		// recover them with offsets into redacted text; preserve coverage as unknown.
		if len(m.URLs) > 0 {
			m.URLs = nil
			m.URLsPartial = true
			m.FeaturesPartial = true
		}
		if len(m.Mentions) > 0 {
			m.Mentions = nil
			m.FeaturesPartial = true
		}
	}
	m.URLs = append([]domain.URL(nil), m.URLs...)
	for i := range m.URLs {
		m.URLs[i].Value = Limit(Redact(m.URLs[i].Value), 2048)
		m.URLs[i].TelegramUsername = Limit(Redact(m.URLs[i].TelegramUsername), 32)
		m.URLs[i].QueryKeys = append([]string(nil), m.URLs[i].QueryKeys...)
		if len(m.URLs[i].QueryKeys) > 32 {
			m.URLs[i].QueryKeys = m.URLs[i].QueryKeys[:32]
			m.URLs[i].QueryKeysPartial = true
		}
		for j, key := range m.URLs[i].QueryKeys {
			if len(key) > 64 {
				m.URLs[i].QueryKeysPartial = true
			}
			m.URLs[i].QueryKeys[j] = Limit(Redact(key), 64)
		}
		m.FeaturesPartial = m.FeaturesPartial || m.URLs[i].QueryKeysPartial
	}
	m.Mentions = append([]domain.Mention(nil), m.Mentions...)
	if len(m.Mentions) > 32 {
		m.Mentions = m.Mentions[:32]
		m.FeaturesPartial = true
	}
	for i := range m.Mentions {
		m.Mentions[i].Username = Limit(Redact(m.Mentions[i].Username), 32)
		if m.Mentions[i].BotStatus != "confirmed" && m.Mentions[i].BotStatus != "heuristic" {
			m.Mentions[i].BotStatus = "unverified"
		}
	}
	// Resolve URL entities before redaction. Offsets into changed text are no longer reliable.
	m.Entities = redactEntities(m.Entities)
	m.CaptionEntities = redactEntities(m.CaptionEntities)
	m.QuoteEntities = redactEntities(m.QuoteEntities)
	if m.Reply != nil {
		r := *m.Reply
		r.Reply = nil
		r = Message(r)
		m.Reply = &r
	}
	return m
}
func redactEntities(es []domain.Entity) []domain.Entity {
	out := append([]domain.Entity(nil), es...)
	if len(out) > 32 {
		out = out[:32]
	}
	for i := range out {
		out[i].URL = Limit(Redact(out[i].URL), 512)
	}
	return out
}
