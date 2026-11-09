package notify

import (
	"fmt"
	"html"

	"github.com/go-telegram/bot/models"
	"sentinel/internal/privacy"
)

func RenderConnection(id, revision, chat int64, title, status string) (string, *models.InlineKeyboardMarkup) {
	text := "🔗 <b>Подключение чата</b>\n" + html.EscapeString(privacy.Redact(title)) + fmt.Sprintf("\nChat ID: <code>%d</code>\n", chat)
	if status != "pending" {
		states := map[string]string{"approved": "Подключён. Наблюдение начинается с доступных новых сообщений, старая история не загружается.", "rejected": "Отклонён. Наблюдение не включено.", "cancelled": "Запрос отменён; бот покинул чат или другая заявка уже обработана."}
		text += states[status]
		return text, nil
	}
	text += "\nСообщения пока не сохраняются и не отправляются AI.\n\nПосле подтверждения: наблюдение, ограниченный журнал, обязательные внешние AI-проверки и приватные уведомления. Бот не банит и не удаляет сообщения.\n\nПеред подтверждением уведомите участников об обработке и внешнем AI-провайдере. Подтверждая подключение, вы подтверждаете эту готовность. Бот должен быть администратором, без прав банить/удалять.\n\nЗапрос действует 24 часа; права повторно проверяются при нажатии."
	kb := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "✅ Подключить", CallbackData: fmt.Sprintf("conn:%d:%d:yes", id, revision)}}, {{Text: "Не подключать", CallbackData: fmt.Sprintf("conn:%d:%d:no", id, revision)}}}}
	return text, kb
}
