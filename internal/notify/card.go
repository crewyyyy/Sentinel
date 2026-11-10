// Package notify renders advisory admin cards without claiming sanctions or AI completion without evidence.
package notify

import (
	"fmt"
	"html"
	"strings"

	"github.com/go-telegram/bot/models"
	"sentinel/internal/domain"
	"sentinel/internal/privacy"
	"sentinel/internal/telegram"
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
	state := "На проверку"
	if c.Status == "closed" {
		state = "Закрыт"
	} else if c.Conflict {
		state = "Конфликт меток · /resolve"
	} else if c.Label != "" {
		state = labelName(c.Label)
	}
	text := heading(iconSearch, "🔎", fmt.Sprintf("Кейс #%d · %s", c.ID, esc(state)))
	text += fmt.Sprintf("\nРиск %d/100 · доверие %d/100", c.Assessment.Score, c.Assessment.Trust)
	if c.Message.UserID != nil && *c.Message.UserID > 0 {
		text += fmt.Sprintf(` · <a href="tg://user?id=%d">Автор</a>`, *c.Message.UserID)
	} else {
		text += " · автор неизвестен или от имени чата"
	}
	text += "\n\n"
	content := c.Message.Content()
	if content == "" {
		content = "Текст сообщения недоступен"
	}
	text += "<blockquote>" + esc(privacy.Limit(content, 240)) + "</blockquote>\n"
	if c.Message.Partial {
		text += "<i>Исходное сообщение вне журнала; доступен только фрагмент ответа.</i>\n"
	}

	if c.AI == nil {
		if c.Assessment.ReviewRequired || c.AIState == "pending" {
			text += "\n<b>AI: ожидает проверки</b>\n"
		} else {
			text += "\nAI не запрашивался.\n"
		}
	} else {
		text += "\n<b>AI: " + esc(categoryName(c.AI.Result.Category)) + "</b>\n"
		if explanation := strings.TrimSpace(c.AI.Result.ExplanationRU); explanation != "" {
			text += esc(privacy.Limit(explanation, 220)) + "\n"
		}
		text += "Совет AI: " + esc(recommendationName(c.AI.Result.Recommendation)) + ".\n"
	}
	if len(c.Assessment.Signals) > 0 {
		reasons := []string{}
		seen := map[string]bool{}
		for _, s := range c.Assessment.Signals {
			name := signalName(s.Code)
			if !seen[name] && len(reasons) < 3 {
				reasons = append(reasons, name)
				seen[name] = true
			}
		}
		text += "Причины: " + strings.Join(reasons, "; ") + ".\n"
	}
	if c.Reports > 0 {
		text += fmt.Sprintf("Жалоб: %d", c.Reports)
		for i, reporter := range c.Reporters {
			if i >= 20 {
				break
			}
			text += fmt.Sprintf(` · <a href="tg://user?id=%d">%d</a>`, reporter, i+1)
		}
		text += ".\n"
	}
	for _, event := range c.Events {
		source := event.Source
		switch event.Source {
		case "platform_chat_member":
			source = "Telegram"
		case "trusted_bot_webhook":
			source = "Доверенная интеграция"
		case "mtproto_admin_log":
			source = "Журнал Telegram"
		}
		if event.EvidenceLevel == "attested" {
			text += "Со слов администратора: " + esc(actionName(event.Action)) + ". Telegram не подтвердил.\n"
		} else if event.EvidenceLevel == "verified" {
			text += esc(source) + ": подтверждено действие «" + esc(actionName(event.Action)) + "».\n"
		} else {
			text += "Неподтверждённое действие: " + esc(actionName(event.Action)) + ".\n"
		}
		if event.RelatedMessageID == nil {
			text += "Связь с сообщением не установлена.\n"
		} else {
			text += "Связь указана администратором, не Telegram.\n"
		}
		if event.SendingRestricted && event.EvidenceLevel == "verified" {
			text += "Ограничена отправка сообщений.\n"
		}
	}
	kb := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{}}
	if link, err := telegram.MessageLink(c.Message.ChatID, c.Message.ID, c.Message.ThreadID); err == nil {
		kb.InlineKeyboard = append(kb.InlineKeyboard, []models.InlineKeyboardButton{{Text: "К сообщению", URL: link, IconCustomEmojiID: iconLink}})
	} else {
		text += fmt.Sprintf("Ссылка недоступна. Чат: <code>%d</code>, сообщение: <code>%d</code>.\n", c.Message.ChatID, c.Message.ID)
	}
	if c.Status != "closed" {
		kb.InlineKeyboard = append(kb.InlineKeyboard,
			[]models.InlineKeyboardButton{
				button("Не нарушение", fmt.Sprintf("act:benign:%d", c.ID), iconCheck, "success"),
				button("Спам", fmt.Sprintf("act:spam:%d", c.ID), iconClose, "danger"),
			},
			[]models.InlineKeyboardButton{
				button("Проверить", fmt.Sprintf("act:needs_review:%d", c.ID), iconSearch, "primary"),
				button("Закрыть", fmt.Sprintf("act:close:%d", c.ID), "", ""),
			},
		)
	}
	return strings.TrimSpace(text), kb
}

func categoryName(code string) string {
	names := map[string]string{
		"legitimate": "без признаков нарушения", "benign_criticism": "добросовестная критика",
		"uncertain": "недостаточно данных", "unsolicited_promotion": "возможная реклама",
		"referral_spam": "возможная реферальная реклама", "impersonation": "возможная подмена личности",
		"phishing": "возможный фишинг", "credential_theft": "возможный сбор ключей или паролей", "other_abuse": "возможное нарушение",
	}
	if name, ok := names[code]; ok {
		return name
	}
	return "нужна проверка"
}

func recommendationName(code string) string {
	names := map[string]string{"ignore": "не вмешиваться", "monitor": "наблюдать", "review": "проверить", "warn": "рассмотреть предупреждение", "mute": "рассмотреть ограничение", "ban": "рассмотреть блокировку"}
	if name, ok := names[code]; ok {
		return name
	}
	return "проверить"
}

func labelName(code string) string {
	names := map[string]string{"benign": "Не нарушение", "spam": "Метка: спам", "false_positive": "Ложное срабатывание", "needs_review": "На проверку"}
	if name, ok := names[code]; ok {
		return name
	}
	return "На проверку"
}

func actionName(code string) string {
	names := map[string]string{"ban": "блокировка", "kick": "исключение", "restrict": "ограничение", "unrestrict": "снятие ограничений", "warn": "предупреждение", "delete": "удаление"}
	if name, ok := names[code]; ok {
		return name
	}
	return "неизвестно"
}

func signalName(code string) string {
	names := map[string]string{
		"FIRST_OBSERVED": "первое наблюдаемое сообщение", "UNKNOWN_EXTERNAL_DOMAIN": "неизвестный домен",
		"TELEGRAM_BOT_LINK": "ссылка на бота", "TELEGRAM_LINK_UNVERIFIED": "назначение Telegram-ссылки неизвестно",
		"COMPETITOR_PRICE_OR_ACCUSATION": "критика или сравнение цен", "REFERRAL_LINK": "реферальная ссылка",
		"EXTERNAL_PURCHASE_CTA": "предложение покупки", "REFERRAL_REVIEW": "проверка ref-признаков",
	}
	if name, ok := names[code]; ok {
		return name
	}
	return "дополнительные признаки"
}

type Delivery struct {
	ConnectionID int64  `json:"connection_id,omitempty"`
	ModelPanelID int64  `json:"model_panel_id,omitempty"`
	OperatorOnly bool   `json:"operator_only,omitempty"`
	Private      bool   `json:"private"`
	OriginChatID int64  `json:"origin_chat_id,omitempty"`
	IncidentID   int64  `json:"incident_id"`
	ChatID       int64  `json:"chat_id"`
	Text         string `json:"text,omitempty"`
	Fallback     bool   `json:"fallback,omitempty"`
}
