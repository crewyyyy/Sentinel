package urlscan

import (
	"sentinel/internal/domain"
	"testing"
)

func TestEntitiesUTF16AndCaption(t *testing.T) {
	s := New([]string{"google.com"}, nil, nil, []string{"knownbot"})
	for _, m := range []domain.Message{
		{Text: "😀 ссылка", Entities: []domain.Entity{{Type: "text_link", Offset: 3, Length: 6, URL: "https://hidden.example/x"}}},
		{Caption: "😀 https://hidden.example/x", CaptionEntities: []domain.Entity{{Type: "url", Offset: 3, Length: 24}}},
		{Text: "bot", Entities: []domain.Entity{{Type: "text_mention", Bot: true}}},
		{Text: "@knownbot", Entities: []domain.Entity{{Type: "mention", Offset: 0, Length: 9}}},
	} {
		us := s.Scan(m)
		if len(us) != 1 {
			t.Fatalf("expected one URL: %+v", us)
		}
		if !(us[0].Unknown || us[0].Bot) {
			t.Fatalf("missing trigger: %+v", us)
		}
	}
}
func TestUTF16Bounds(t *testing.T) {
	if v, ok := UTF16Slice("😀abc", 2, 3); !ok || v != "abc" {
		t.Fatal(v, ok)
	}
	for _, p := range [][2]int{{1, 2}, {-1, 4}, {0, 100}, {0, 1}} {
		if _, ok := UTF16Slice("😀abc", p[0], p[1]); ok {
			t.Fatal("invalid UTF16 slice accepted", p)
		}
	}
}
func TestNormalization(t *testing.T) {
	s := New([]string{"google.com"}, []string{"watch.example"}, []string{"bad.example"}, nil)
	for _, tc := range []struct {
		input, domain     string
		unknown, ref, bot bool
	}{
		{"https://DOCS.google.com./a", "google.com", false, false, false},
		{"https://пример.рф/a", "xn--e1afmkfd.xn--p1ai", true, false, false},
		{"https://google.com.evil.example/a", "evil.example", true, false, false},
		{"https://google.com?utm_medium=a", "google.com", false, false, false},
		{"https://google.com?referral=x", "google.com", false, true, false},
		{"https://t.me/random", "t.me", false, false, false},
		{"https://t.me/thingbot?startapp=abc", "t.me", false, true, true},
		{"tg://resolve?domain=thingbot", "resolve", false, false, true},
	} {
		us := s.Scan(domain.Message{Text: tc.input})
		if len(us) != 1 {
			t.Fatalf("%s: %+v", tc.input, us)
		}
		u := us[0]
		if u.Domain != tc.domain || u.Unknown != tc.unknown || u.Referral != tc.ref || u.Bot || u.BotHeuristic != tc.bot {
			t.Fatalf("%s => %+v", tc.input, u)
		}
	}
}
