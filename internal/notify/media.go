package notify

import (
	"fmt"
	"strings"

	"sentinel/internal/domain"
)

func RenderMedia(r domain.MediaReview) string {
	if r.Unavailable && r.Frames == 0 {
		return "⚪ <b>Не удалось проверить медиа</b>"
	}
	heading := "✅ <b>Низкий риск NSFW</b>"
	if r.Verdict == "nsfw" {
		heading = "🔞 <b>Высокий риск NSFW</b>"
	} else if r.Verdict != "safe" || r.Partial {
		heading = "🟡 <b>Нужна проверка</b>"
	}
	score := strings.ReplaceAll(fmt.Sprintf("%.1f", r.Probability*100), ".", ",")
	text := heading + "\nNSFW: <code>" + score + "%</code>"
	if r.Preview {
		text += "\n<i>Проверено превью</i>"
	} else if r.Frames > 1 {
		text += fmt.Sprintf("\n<i>Проверено кадров: %d</i>", r.Frames)
	}
	if r.Partial {
		text += "\n<i>Часть медиа не проверена</i>"
	}
	return text
}
