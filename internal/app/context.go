package app

import (
	"sentinel/internal/domain"
	"sentinel/internal/privacy"
)

func boundedURLs(urls []domain.URL, n int) []domain.URL {
	out := append([]domain.URL(nil), urls...)
	if len(out) > n {
		out = out[:n]
	}
	for i := range out {
		out[i].Value = privacy.Limit(privacy.Redact(out[i].Value), 512)
	}
	return out
}
func jobMessage(m domain.Message, textLimit, urlLimit int) domain.Message {
	if len(m.Text) > textLimit || len(m.Caption) > textLimit || len(m.URLs) > urlLimit {
		m.Partial = true
	}
	m.Text = privacy.Limit(m.Text, textLimit)
	m.Caption = privacy.Limit(m.Caption, textLimit)
	m.Media = nil
	m.MediaPartial = false
	m.Entities = nil
	m.CaptionEntities = nil
	m.URLs = boundedURLs(m.URLs, urlLimit)
	if m.Reply != nil {
		r := *m.Reply
		r.Reply = nil
		r = jobMessage(r, 1000, 4)
		m.Reply = &r
	}
	return m
}
