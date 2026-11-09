package notify

import (
	"strings"
	"testing"
)

func TestConnectionCardEscapesAndRequiresExplicitConsent(t *testing.T) {
	text, kb := RenderConnection(1, 2, -1001111111111, "<b>Fixture</b> api_key=fixturevalue", "pending")
	if strings.Contains(text, "<b>Fixture</b>") || strings.Contains(text, "fixturevalue") || !strings.Contains(text, "Сообщения пока не сохраняются") || !strings.Contains(text, "уведомите участников") {
		t.Fatal("unsafe consent card", text)
	}
	if len(kb.InlineKeyboard) != 2 {
		t.Fatal(kb)
	}
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			if len(b.CallbackData) > 64 {
				t.Fatal("callback too long")
			}
		}
	}
	for _, status := range []string{"approved", "rejected", "cancelled"} {
		_, kb := RenderConnection(1, 2, -1001111111111, "Fixture", status)
		if kb != nil {
			t.Fatal("closed request kept approval buttons", status)
		}
	}
}
