package notify

import (
	"fmt"
	"html"
	"strings"

	"github.com/go-telegram/bot/models"
	"sentinel/internal/privacy"
)

const ModelsPerPage = 6

type ModelPanel struct {
	Models         []string `json:"models"`
	Query          string   `json:"query"`
	Page           int      `json:"page"`
	Selected       int      `json:"selected"`
	Revision       int64    `json:"revision"`
	SettingVersion int64    `json:"setting_version"`
	Notice         string   `json:"notice,omitempty"`
}

func RenderModels(id int64, p ModelPanel, current string) (string, *models.InlineKeyboardMarkup) {
	esc := func(s string) string { return html.EscapeString(privacy.Redact(s)) }
	callback := func(action string, value int) string {
		return fmt.Sprintf("mdl:%d:%d:%s:%d", id, p.Revision, action, value)
	}
	kb := &models.InlineKeyboardMarkup{}
	text := heading(iconAI, "🤖", "Модель AI") + "\nТекущая: <code>" + esc(current) + "</code>\n"
	if p.Notice != "" {
		text += "\n" + esc(p.Notice) + "\n"
	}
	if p.Selected >= 0 && p.Selected < len(p.Models) {
		text += "\nВыбрана: <code>" + esc(p.Models[p.Selected]) + "</code>\n\nПрименить модель?"
		kb.InlineKeyboard = [][]models.InlineKeyboardButton{
			{button("Применить модель", callback("a", p.Selected), iconCheck, "success")},
			{button("К списку", callback("p", p.Page), iconBack, "")},
		}
		return text, kb
	}
	pages := max(1, (len(p.Models)+ModelsPerPage-1)/ModelsPerPage)
	page := min(max(0, p.Page), pages-1)
	if p.Query != "" {
		text += "Поиск: <code>" + esc(p.Query) + "</code>\n"
	}
	text += fmt.Sprintf("\nСтраница %d/%d · моделей %d\n", page+1, pages, len(p.Models))
	for i := page * ModelsPerPage; i < min(len(p.Models), (page+1)*ModelsPerPage); i++ {
		label := []rune(privacy.Redact(p.Models[i]))
		if len(label) > 46 {
			label = append(append(label[:22:22], '…'), label[len(label)-22:]...)
		}
		b := button(string(label), callback("s", i), "", "")
		if p.Models[i] == current {
			b.IconCustomEmojiID = iconCheck
			b.Style = "primary"
			b.Text = "Текущая · " + b.Text
		}
		kb.InlineKeyboard = append(kb.InlineKeyboard, []models.InlineKeyboardButton{b})
	}
	if len(p.Models) == 0 {
		text += "Ничего не найдено. Измените запрос: /model название\n"
	}
	row := []models.InlineKeyboardButton{}
	if page > 0 {
		row = append(row, button("Назад", callback("p", page-1), iconBack, ""))
	}
	row = append(row, button(fmt.Sprintf("%d / %d", page+1, pages), callback("p", page), "", ""))
	if page+1 < pages {
		row = append(row, button("Далее", callback("p", page+1), iconNext, ""))
	}
	kb.InlineKeyboard = append(kb.InlineKeyboard, row)
	text += "\nПоиск: /model название"
	return strings.TrimSpace(text), kb
}
