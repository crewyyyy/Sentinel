package notify

import (
	"sentinel/internal/domain"
	"strings"
	"testing"
)

func TestMediaResultPresentation(t *testing.T) {
	for _, tc := range []struct {
		r    domain.MediaReview
		want string
	}{
		{domain.MediaReview{Verdict: "safe", Probability: .012}, "✅ <b>Низкий риск NSFW</b>\nNSFW: <code>1,2%</code>"},
		{domain.MediaReview{Verdict: "review", Probability: .4}, "🟡 <b>Нужна проверка</b>"},
		{domain.MediaReview{Verdict: "nsfw", Probability: .9}, "🔞 <b>Высокий риск NSFW</b>"},
		{domain.MediaReview{Unavailable: true}, "⚪ <b>Не удалось проверить медиа</b>"},
		{domain.MediaReview{Verdict: "nsfw", Probability: .9, Frames: 1, Unavailable: true, Partial: true}, "Часть медиа не проверена"},
		{domain.MediaReview{Verdict: "safe", Preview: true}, "Проверено превью"},
		{domain.MediaReview{Verdict: "safe", Frames: 6}, "Проверено кадров: 6"},
		{domain.MediaReview{}, "Нужна проверка"},
	} {
		s := RenderMedia(tc.r)
		if !strings.Contains(s, tc.want) || len(s) > 500 {
			t.Fatal(s)
		}
	}
}
