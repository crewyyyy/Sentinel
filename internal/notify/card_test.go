package notify

import (
	"sentinel/internal/domain"
	"strings"
	"testing"
)

func TestCardSafety(t *testing.T) {
	id := int64(123)
	c := Card{ID: 1, Message: domain.Message{ChatID: -1001111111111, ID: 2, UserID: &id, Text: "<script> api_key=secretvalue", Partial: true}, Assessment: domain.Assessment{ReviewRequired: true}, Status: "open", Reporters: []int64{200}, Events: []domain.ModerationEvent{{Source: "admin_attested", EvidenceLevel: "attested", Action: "warn"}}}
	text, kb := Render(c)
	for _, required := range []string{"AI pending", "&lt;script&gt;", "unknown", "Со слов администратора", "Evidence partial", "200"} {
		if !strings.Contains(text, required) {
			t.Fatal("missing", required, text)
		}
	}
	if strings.Contains(text, "secretvalue") || strings.Contains(text, "<script>") {
		t.Fatal("unsafe preview", text)
	}
	if len(kb.InlineKeyboard) != 2 {
		t.Fatal(kb)
	}
}
