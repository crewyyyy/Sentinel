package notify

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"sentinel/internal/rules"
)

func TestModulePanelViewsAreShortEscapedAndBounded(t *testing.T) {
	analyzer, err := rules.NewAnalyzer(rules.DefaultConfig(), rules.Options{})
	if err != nil {
		t.Fatal(err)
	}
	policy := analyzer.Resolved()
	selected := 0
	for i, spec := range policy.Catalog {
		if spec.ID == "sales" {
			selected = i
		}
	}
	for _, view := range []string{"chats", "list", "module", "dictionary", "parameters", "confirm"} {
		p := ModulePanel{Dictionary: "intent", Revision: 12, View: view, Selected: selected, ChatTitle: "Fixture <chat>", Dirty: true, Chats: []ModuleChat{{ID: -123, Title: "Fixture"}}}
		text, kb := RenderModules(1, p, policy)
		if utf8.RuneCountInString(text) > 4096 || strings.Contains(text, "Fixture <chat>") || kb == nil || len(kb.InlineKeyboard) == 0 {
			t.Fatal("invalid panel rendering", view)
		}
		for _, row := range kb.InlineKeyboard {
			for _, b := range row {
				if len(b.CallbackData) > 64 || b.Text == "" {
					t.Fatal("invalid module button")
				}
			}
		}
		if view == "module" {
			if utf8.RuneCountInString(text) > 350 {
				t.Fatal("module description is too long")
			}
			for _, noise := range []string{"версия", "YAML", "Пороги", "/modulewords", "/modulelimit", "Запрет задаёт", "не доказывает", "требует проверки"} {
				if strings.Contains(text, noise) {
					t.Fatal("noise in module description", noise)
				}
			}
		}
		if view == "dictionary" && !strings.Contains(text, "/modulewords") {
			t.Fatal("dictionary editor is not discoverable")
		}
		if view == "parameters" && !strings.Contains(text, "/modulelimit") {
			t.Fatal("parameter editor is not discoverable")
		}
		if view == "confirm" && !strings.Contains(text, "Сохранить настройки?") {
			t.Fatal("confirmation missing")
		}
		if _, err := json.Marshal(p); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRequiredModulesShowActualReadOnlyParameters(t *testing.T) {
	config := rules.DefaultConfig()
	config.Weights["FIRST_OBSERVED"] = 19
	analyzer, err := rules.NewAnalyzer(config, rules.Options{})
	if err != nil {
		t.Fatal(err)
	}
	policy := analyzer.Resolved()
	for i, spec := range policy.Catalog {
		if !spec.Required {
			continue
		}
		text, kb := RenderModules(1, ModulePanel{Revision: 1, View: "parameters", Selected: i, Dirty: true}, policy)
		if !strings.Contains(text, "Сигналы") || strings.Contains(text, "Не сохранено") {
			t.Fatal("required parameters missing or inherited another draft")
		}
		if spec.ID == "observation" && !strings.Contains(text, "+19") {
			t.Fatal("parameters ignore the configured weight")
		}
		for _, row := range kb.InlineKeyboard {
			for _, button := range row {
				for _, action := range []string{":mode:", ":cap:", ":save:", ":cancel:", ":window:", ":reset:"} {
					if strings.Contains(button.CallbackData, action) {
						t.Fatal("read-only parameters expose mutation", action)
					}
				}
			}
		}
	}
	text, _ := RenderModules(1, ModulePanel{Revision: 1, View: "module", Selected: 0}, policy)
	if !strings.Contains(text, "+19") || !strings.Contains(text, "неполный анализ") {
		t.Fatal("observation still looks empty")
	}
	text, kb := RenderModules(1, ModulePanel{Revision: 1, View: "list", Selected: -1, Dirty: true}, policy)
	if strings.Contains(text, "Не сохранено") {
		t.Fatal("list renders a stale draft")
	}
	for _, row := range kb.InlineKeyboard {
		for _, button := range row {
			if strings.Contains(button.CallbackData, ":save:") || strings.Contains(button.CallbackData, ":cancel:") {
				t.Fatal("save/cancel leaked to list")
			}
		}
	}
}

func TestRequiredModulePanelHasNoMutationButtons(t *testing.T) {
	analyzer, _ := rules.NewAnalyzer(rules.DefaultConfig(), rules.Options{})
	text, kb := RenderModules(1, ModulePanel{Revision: 1, View: "module", Selected: 0}, analyzer.Resolved())
	if !strings.Contains(text, "Обязательный") {
		t.Fatal("required module state missing")
	}
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			if strings.Contains(b.CallbackData, ":mode:") || strings.Contains(b.CallbackData, ":cap:") {
				t.Fatal("required module exposes mutation control")
			}
		}
	}
}
