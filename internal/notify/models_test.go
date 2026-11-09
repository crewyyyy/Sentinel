package notify

import (
	"fmt"
	"strings"
	"testing"
)

func TestModelPagesConfirmationAndCallbackBounds(t *testing.T) {
	ids := []string{}
	for i := 0; i < 14; i++ {
		ids = append(ids, fmt.Sprintf("fixture/model-%02d", i))
	}
	p := ModelPanel{Models: ids, Selected: -1, Revision: 9}
	text, kb := RenderModels(123, p, ids[0])
	if !strings.Contains(text, "Страница 1/3") || len(kb.InlineKeyboard) != 7 || !strings.HasPrefix(kb.InlineKeyboard[0][0].Text, "✓ ") {
		t.Fatal(text, kb)
	}
	for _, page := range []int{0, 1, 2} {
		p.Page = page
		_, kb = RenderModels(123, p, ids[0])
		for _, row := range kb.InlineKeyboard {
			for _, button := range row {
				if len(button.CallbackData) > 64 || strings.Contains(button.CallbackData, "fixture/") {
					t.Fatal("unbounded/model-bearing callback", button)
				}
			}
		}
	}
	p.Selected = 7
	text, kb = RenderModels(123, p, ids[0])
	if !strings.Contains(text, ids[7]) || len(kb.InlineKeyboard) != 2 || !strings.Contains(kb.InlineKeyboard[0][0].CallbackData, ":a:7") {
		t.Fatal(text, kb)
	}
	p.Selected = -1
	p.Query = "<script>"
	text, _ = RenderModels(123, p, ids[0])
	if strings.Contains(text, "<script>") || !strings.Contains(text, "&lt;script&gt;") {
		t.Fatal("unescaped search query")
	}
}
