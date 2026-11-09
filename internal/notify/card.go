// Package notify renders advisory admin cards without claiming sanctions or AI completion without evidence.
package notify

import (
	"fmt"
	"github.com/go-telegram/bot/models"
	"html"
	"sentinel/internal/domain"
	"sentinel/internal/privacy"
	"sentinel/internal/telegram"
	"strings"
)

type Card struct {
	ID         int64                    `json:"id"`
	Message    domain.Message           `json:"message"`
	Assessment domain.Assessment        `json:"assessment"`
	AI         *domain.ReviewResponse   `json:"ai,omitempty"`
	AIState    string                   `json:"ai_state"`
	Reports    int                      `json:"reports"`
	Reporters  []int64                  `json:"reporter_ids,omitempty"`
	Label      string                   `json:"label"`
	Conflict   bool                     `json:"conflict"`
	Status     string                   `json:"status"`
	Events     []domain.ModerationEvent `json:"events,omitempty"`
}

func Render(c Card) (string, *models.InlineKeyboardMarkup) {
	esc := func(s string) string { return html.EscapeString(privacy.Redact(s)) }
	a := c.Assessment
	title := fmt.Sprintf("🔎 Кейс #%d · риск %d/100 · trust %d/100\nЧат: %d · сообщение: %d\n", c.ID, a.Score, a.Trust, c.Message.ChatID, c.Message.ID)
	if c.Message.UserID != nil {
		title += fmt.Sprintf("Автор ID: %d\n", *c.Message.UserID)
	} else {
		title += "Автор: sender_chat/неизвестен\n"
	}
	if a.Band != "" {
		title += "Уровень рассмотрения: " + esc(a.Band) + "\n"
	}
	if c.Message.Partial {
		title += "Evidence partial: исходное сообщение вне журнала, использованы доступные поля reply.\n"
	}
	title += "История: только наблюдаемая, partial. Удаление: unknown.\n\n<blockquote>" + esc(privacy.Limit(c.Message.Content(), 650)) + "</blockquote>\n\n"
	for i, s := range a.Signals {
		if i >= 8 {
			break
		}
		must := ""
		if s.MandatoryAI {
			must = "; AI обязателен"
		}
		title += fmt.Sprintf("• %s (+%d%s)\n", esc(s.Code), s.Weight, must)
	}
	if c.AI == nil {
		if a.ReviewRequired || c.AIState == "pending" {
			title += "AI pending — проверка не завершена.\n"
		} else {
			title += "AI не запрашивался.\n"
		}
		title += "Рекомендация: проверить, не обвинение.\n"
	} else {
		title += "AI (мнение, не факт): " + esc(c.AI.Result.Category) + "\n" + esc(privacy.Limit(c.AI.Result.ExplanationRU, 550)) + "\nРекомендация AI: " + esc(c.AI.Result.Recommendation) + " (только решение администратора).\n"
	}
	if c.Reports > 0 {
		title += fmt.Sprintf("Жалоб: %d (не доказательство нарушения).\n", c.Reports)
	}
	if len(c.Reporters) > 0 {
		title += fmt.Sprintf("Авторы жалоб (только админам): %v\n", c.Reporters)
	}
	if c.Label != "" {
		title += "Метка: " + esc(c.Label) + " — не факт наказания.\n"
	}
	if c.Status != "" {
		title += "Состояние: " + esc(c.Status) + "\n"
	}
	for _, e := range c.Events {
		relation := "Действие над пользователем; связь с сообщением не установлена."
		if e.RelatedMessageID != nil {
			relation = "Связь с сообщением указана администратором, не Telegram."
		}
		if e.EvidenceLevel == "attested" {
			relation = "Со слов администратора; Telegram не подтвердил. " + relation
		}
		title += fmt.Sprintf("Событие: %s · %s · %s. %s\n", esc(e.Action), esc(e.Source), esc(e.EvidenceLevel), relation)
		if e.SendingRestricted {
			title += "Подтверждено ограничение отправки сообщений.\n"
		}
	}
	title += "Бот не применяет санкций."
	kb := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{}}
	if link, err := telegram.MessageLink(c.Message.ChatID, c.Message.ID, c.Message.ThreadID); err == nil {
		kb.InlineKeyboard = append(kb.InlineKeyboard, []models.InlineKeyboardButton{{Text: "Открыть сообщение", URL: link}})
	} else {
		title += "\nСсылка недоступна; используйте chat/message IDs."
	}
	row := []models.InlineKeyboardButton{}
	for _, pair := range [][2]string{{"Не нарушение", "benign"}, {"Спам", "spam"}, {"Проверить", "needs_review"}, {"Закрыть", "close"}} {
		row = append(row, models.InlineKeyboardButton{Text: pair[0], CallbackData: fmt.Sprintf("act:%s:%d", pair[1], c.ID)})
	}
	kb.InlineKeyboard = append(kb.InlineKeyboard, row)
	return strings.TrimSpace(title), kb
}

type Delivery struct {
	ModelPanelID int64  `json:"model_panel_id,omitempty"`
	Private      bool   `json:"private"`
	OriginChatID int64  `json:"origin_chat_id,omitempty"`
	IncidentID   int64  `json:"incident_id"`
	ChatID       int64  `json:"chat_id"`
	Text         string `json:"text,omitempty"`
	Fallback     bool   `json:"fallback,omitempty"`
}
