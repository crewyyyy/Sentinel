package notify

import (
	"strings"
	"testing"

	"sentinel/internal/domain"
)

func TestMediaResultPresentation(t *testing.T) {
	for _, tc := range []struct {
		r          domain.MediaReview
		want, icon string
	}{
		{domain.MediaReview{Verdict: "safe", Probability: .012}, "<b>Низкий риск NSFW</b>\nNSFW: <code>1,2%</code>", iconCheck},
		{domain.MediaReview{Verdict: "review", Probability: .4}, "<b>Нужна проверка</b>", iconSearch},
		{domain.MediaReview{Verdict: "nsfw", Probability: .9}, "<b>Высокий риск NSFW</b>", iconClose},
		{domain.MediaReview{Unavailable: true}, "<b>Не удалось проверить медиа</b>", iconClose},
		{domain.MediaReview{Verdict: "nsfw", Probability: .9, Frames: 1, Unavailable: true, Partial: true}, "Часть медиа не проверена", iconClose},
		{domain.MediaReview{Verdict: "safe", Preview: true}, "Проверено превью", iconCheck},
		{domain.MediaReview{Verdict: "safe", Frames: 6}, "Проверено кадров: 6", iconCheck},
		{domain.MediaReview{}, "Нужна проверка", iconSearch},
	} {
		s := RenderMedia(tc.r)
		if !strings.Contains(s, tc.want) || len(s) > 500 {
			t.Fatal(s)
		}
		if !strings.HasPrefix(s, `<tg-emoji emoji-id="`+tc.icon+`">`) || strings.Count(s, "<tg-emoji") != 1 {
			t.Fatal("media verdict must use its Premium icon", s)
		}
	}
}

func TestMediaQueueNoticesUsePremiumEmoji(t *testing.T) {
	for _, text := range []string{MediaPending(), MediaRateLimited()} {
		if !strings.HasPrefix(text, `<tg-emoji emoji-id="`+iconAI+`">`) || len(text) > 300 {
			t.Fatal("media notice lost Premium formatting", text)
		}
	}
}
