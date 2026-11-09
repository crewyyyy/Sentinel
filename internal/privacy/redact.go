// Package privacy removes credentials before persistence, logging, or external review.
package privacy

import (
	"regexp"
	"sentinel/internal/domain"
	"strings"
	"unicode/utf8"
)

var credentials = regexp.MustCompile(`(?i)(\b(?:api[_ -]?key|token|password|passwd|secret|authorization|access_token|api_token)\b[\s"':=]+)([^\s"'&,;}]+)`)
var keys = regexp.MustCompile(`(?i)\b(?:sk-[A-Za-z0-9_-]{8,}|[0-9]{6,12}:[A-Za-z0-9_-]{20,}|Bearer\s+[A-Za-z0-9_.-]{8,})`)
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
	m.Text = Limit(Redact(m.Text), 16384)
	m.Caption = Limit(Redact(m.Caption), 8192)
	m.QuoteText = Limit(Redact(m.QuoteText), 2048)
	m.URLs = append([]domain.URL(nil), m.URLs...)
	for i := range m.URLs {
		m.URLs[i].Value = Limit(Redact(m.URLs[i].Value), 2048)
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
