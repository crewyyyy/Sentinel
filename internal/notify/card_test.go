package notify

import (
	"strings"
	"testing"
	"unicode/utf8"

	"sentinel/internal/domain"
)

func TestCardSafety(t *testing.T) {
	id := int64(123)
	c := Card{ID: 1, Message: domain.Message{ChatID: -1001111111111, ID: 2, UserID: &id, Text: "<script> api_key=secretvalue", Partial: true}, Assessment: domain.Assessment{ReviewRequired: true}, Status: "open", Reports: 1, Reporters: []int64{200}, Events: []domain.ModerationEvent{{Source: "admin_attested", EvidenceLevel: "attested", Action: "warn"}}}
	text, kb := Render(c)
	for _, required := range []string{"AI: ожидает проверки", "&lt;script&gt;", "Со слов администратора", "вне журнала", "tg://user?id=200"} {
		if !strings.Contains(text, required) {
			t.Fatal("missing", required, text)
		}
	}
	if strings.Contains(text, "secretvalue") || strings.Contains(text, "<script>") || strings.Contains(text, "Бот не применяет санкций") {
		t.Fatal("unsafe or redundant preview", text)
	}
	if len(kb.InlineKeyboard) != 3 || len(kb.InlineKeyboard[1]) != 2 || len(kb.InlineKeyboard[2]) != 2 {
		t.Fatal(kb)
	}
}

func TestCardIsCompactAndUsesRussianLabels(t *testing.T) {
	c := Card{ID: 1, Message: domain.Message{ChatID: -1001111111111, ID: 2, Text: strings.Repeat("Пример ", 100)}, Assessment: domain.Assessment{Score: 12, Signals: []domain.Signal{{Code: "FIRST_OBSERVED", MandatoryAI: true}}}, AI: &domain.ReviewResponse{Result: domain.ReviewResult{Category: "referral_spam", Recommendation: "review", ExplanationRU: strings.Repeat("Возможно продвижение. ", 100)}}}
	text, kb := Render(c)
	for _, unwanted := range []string{"FIRST_OBSERVED", "referral_spam", "Состояние: open", "Удаление: unknown", "Уровень рассмотрения", "Бот не применяет санкций"} {
		if strings.Contains(text, unwanted) {
			t.Fatal("internal jargon in compact card", unwanted)
		}
	}
	if utf8.RuneCountInString(text) > 950 || !utf8.ValidString(text) {
		t.Fatal("card is oversized or invalid UTF-8", len(text))
	}
	if !strings.Contains(text, "возможная реферальная реклама") || !strings.Contains(text, "Совет AI: проверить") {
		t.Fatal(text)
	}
	if kb.InlineKeyboard[1][0].Style != "success" || kb.InlineKeyboard[1][1].Style != "danger" {
		t.Fatal("feedback styles missing", kb)
	}
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			if len(b.CallbackData) > 64 || strings.Contains(b.Text, "<tg-emoji") {
				t.Fatal("invalid button", b)
			}
		}
	}
}

func TestClosedCardOnlyKeepsOriginalLink(t *testing.T) {
	text, kb := Render(Card{ID: 1, Status: "closed", Message: domain.Message{ChatID: -1001111111111, ID: 2}})
	if !strings.Contains(text, "Закрыт") || len(kb.InlineKeyboard) != 1 || kb.InlineKeyboard[0][0].URL == "" {
		t.Fatal(text, kb)
	}
}
