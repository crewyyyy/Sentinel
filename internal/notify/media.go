package notify

import (
	"fmt"
	"strings"

	"sentinel/internal/domain"
)

func RenderMedia(r domain.MediaReview) string {
	if r.Unavailable && r.Frames == 0 {
		return heading(iconClose, "⚪", "Не удалось проверить медиа")
	}
	title := heading(iconCheck, "✅", "Низкий риск NSFW")
	if r.Verdict == "nsfw" {
		title = heading(iconClose, "🔞", "Высокий риск NSFW")
	} else if r.Verdict != "safe" || r.Partial {
		title = heading(iconSearch, "🟡", "Нужна проверка")
	}
	score := strings.ReplaceAll(fmt.Sprintf("%.1f", r.Probability*100), ".", ",")
	text := title + "\nNSFW: <code>" + score + "%</code>"
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

func MediaPending() string {
	return heading(iconAI, "⏳", "Проверка в очереди") + "\nДневной лимит исчерпан. Продолжу после 00:00 UTC."
}

func MediaRateLimited() string {
	return heading(iconAI, "⏳", "Слишком много медиа") + "\nПовторите проверку через минуту."
}
