package notify

import (
	"fmt"
	"html"
	"strings"

	"github.com/go-telegram/bot/models"
	"sentinel/internal/privacy"
	"sentinel/internal/urlscan"
)

const HomeChatsPerPage = 5

type HomePanel struct {
	ID, Revision int64
	View         string
	Page         int
}

type HomeData struct {
	ChatTitles                       []string
	PendingRequests                  int
	PendingReviews, CompletedReviews int64
	AIEnabled, AutoDiscover          bool
	MediaEnabled                     bool
	PendingMedia                     int64
	Incomplete                       bool
	Model, BotUsername               string
}

// RenderHome keeps instructions out of the default screen; navigation replaces
// this message rather than growing a transcript of help and status replies.
func RenderHome(p HomePanel, d HomeData) (string, *models.InlineKeyboardMarkup) {
	esc := func(s string) string { return html.EscapeString(privacy.Redact(s)) }
	kb := &models.InlineKeyboardMarkup{}
	b := func(label, action, icon, style string, value int) models.InlineKeyboardButton {
		return button(label, fmt.Sprintf("nav:%d:%d:%s:%d", p.ID, p.Revision, action, value), icon, style)
	}
	row := func(buttons ...models.InlineKeyboardButton) { kb.InlineKeyboard = append(kb.InlineKeyboard, buttons) }
	back := func() { row(b("Главная", "home", iconBack, "", 0)) }
	ai := "включён"
	if !d.AIEnabled {
		ai = "выключен"
	}
	var text string
	switch p.View {
	case "chats":
		text = heading(iconLink, "🔗", "Чаты")
		if len(d.ChatTitles) == 0 {
			text += "\nНет доступных чатов."
		} else {
			pages := max(1, (len(d.ChatTitles)+HomeChatsPerPage-1)/HomeChatsPerPage)
			page := min(max(0, p.Page), pages-1)
			for _, title := range d.ChatTitles[page*HomeChatsPerPage : min(len(d.ChatTitles), (page+1)*HomeChatsPerPage)] {
				text += "\n• " + esc(privacy.Limit(title, 80))
			}
			if pages > 1 {
				text += fmt.Sprintf("\n\n%d/%d", page+1, pages)
				navigation := []models.InlineKeyboardButton{}
				if page > 0 {
					navigation = append(navigation, b("Назад", "page", iconBack, "", page-1))
				}
				if page+1 < pages {
					navigation = append(navigation, b("Далее", "page", iconNext, "", page+1))
				}
				row(navigation...)
			}
			row(b("Настроить модули", "modules", iconShield, "primary", 0))
		}
		row(b("Подключить чат", "connect", iconLink, "success", 0))
		back()
	case "status":
		text = heading(iconStats, "📊", "Состояние") + "\nAI: <b>" + ai + "</b>"
		if d.Model != "" {
			text += "\nМодель: <code>" + esc(privacy.Limit(d.Model, 80)) + "</code>"
		}
		text += fmt.Sprintf("\n\nПроверки: <b>%d</b> в очереди · %d завершено", d.PendingReviews, d.CompletedReviews)
		if d.MediaEnabled {
			text += fmt.Sprintf("\nМедиа NSFW: включено · <b>%d</b> в очереди", d.PendingMedia)
		}
		row(b("Обновить", "status", iconStats, "", 0), b("Модель AI", "model", iconAI, "", 0))
		back()
	case "connect":
		text = heading(iconLink, "🔗", "Подключить чат")
		if d.AutoDiscover {
			text += "\nДобавьте бота администратором <b>без прав банить и удалять</b>.\n\nУведомите участников об обработке текста и медиа внешним AI. Затем подтвердите заявку в ЛС бота."
			if urlscan.ValidBotUsername(d.BotUsername) {
				row(models.InlineKeyboardButton{Text: "Добавить в группу", URL: "https://t.me/" + d.BotUsername + "?startgroup=connect", IconCustomEmojiID: iconLink, Style: "success"})
			}
		} else {
			text += "\nАвтоподключение выключено. Чаты добавляет оператор в конфигурации."
		}
		if d.PendingRequests > 0 {
			row(b(fmt.Sprintf("Заявки · %d", d.PendingRequests), "requests", iconCheck, "primary", 0))
		}
		back()
	case "requests":
		text = heading(iconCheck, "✅", "Заявки")
		if d.PendingRequests == 0 {
			text += "\nНовых заявок нет."
		} else {
			text += "\nПодтвердите подключение в отдельной карточке заявки в ЛС."
		}
		row(b("Обновить", "requests", iconStats, "", 0))
		back()
	case "help":
		text = heading(iconSearch, "🔎", "Помощь") + "\n/report — жалоба ответом на сообщение.\n/modules — правила чата.\n/model — выбор модели AI.\n\nВ карточке кейса выберите решение кнопкой."
		row(b("Подключить чат", "connect", iconLink, "", 0))
		back()
	default:
		text = HelpHeading() + fmt.Sprintf("\nЧаты: <b>%d</b> · AI <b>%s</b>", len(d.ChatTitles), ai)
		if d.PendingRequests > 0 {
			row(b(fmt.Sprintf("Заявки · %d", d.PendingRequests), "requests", iconCheck, "primary", 0))
		}
		row(b("Чаты", "chats", iconLink, "", 0), b("Модули", "modules", iconShield, "primary", 0))
		row(b("Модель AI", "model", iconAI, "", 0), b("Статус", "status", iconStats, "", 0))
		row(b("Подключить чат", "connect", iconLink, "success", 0), b("Помощь", "help", iconSearch, "", 0))
	}
	if d.Incomplete && strings.Contains("|home|chats|status|", "|"+p.View+"|") {
		text += "\n<i>Данные части чатов недоступны.</i>"
	}
	return text, kb
}
