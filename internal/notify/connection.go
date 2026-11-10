package notify

import (
	"fmt"
	"html"

	"github.com/go-telegram/bot/models"
	"sentinel/internal/privacy"
)

func RenderConnection(id, revision, chat int64, title, status string) (string, *models.InlineKeyboardMarkup) {
	name := html.EscapeString(privacy.Limit(privacy.Redact(title), 100))
	header, icon, fallback := "Подключение чата", iconLink, "🔗"
	body := ""
	switch status {
	case "approved":
		header, icon, fallback = "Чат подключён", iconCheck, "✅"
		body = "Наблюдение включено для новых сообщений. Старая история не загружается.\n\n/status · состояние\n/model · модель AI"
	case "rejected":
		header, icon, fallback = "Подключение отклонено", iconClose, "❌"
		body = "Сообщения не собираются. Для новой заявки добавьте бота заново."
	case "cancelled":
		header, icon, fallback = "Заявка отменена", iconClose, "❌"
		body = "Бот покинул чат или обработана другая заявка. Доступные чаты: /chats"
	case "expired":
		header = "Заявка истекла"
		body = "Подключение не подтверждено за 24 часа. Для новой заявки добавьте бота заново."
	case "pending":
		body = "Сообщения пока не сохраняются и не отправляются AI.\n\nПосле подключения: проверка новых сообщений внешним AI и приватные карточки администраторам. Бот не банит и не удаляет сообщения.\n\nПеред подтверждением уведомите участников об обработке и внешнем AI. Кнопка «Подключить» подтверждает вашу готовность. Боту нужен статус администратора без прав банить/удалять.\n\n<i>Заявка на 24 часа. Права проверим при нажатии.</i>"
	default:
		header = "Заявка недоступна"
		body = "Проверьте состояние командой /chats."
	}
	text := heading(icon, fallback, header) + "\n" + name + fmt.Sprintf("\n<code>%d</code>\n\n", chat) + body
	if status != "pending" {
		return text, nil
	}
	kb := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{button("Подключить", fmt.Sprintf("conn:%d:%d:yes", id, revision), iconCheck, "success")},
		{button("Не подключать", fmt.Sprintf("conn:%d:%d:no", id, revision), iconClose, "")},
	}}
	return text, kb
}
