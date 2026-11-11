package notify

import (
	"fmt"
	"html"
	"regexp"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"
)

func TestHomeIsCompactAndInstructionsAreOnDemand(t *testing.T) {
	data := HomeData{ChatTitles: []string{"Fixture group"}, AIEnabled: true, AutoDiscover: true, BotUsername: "fixture_bot"}
	text, kb := RenderHome(HomePanel{ID: 1, Revision: 1, View: "home"}, data)
	if utf8.RuneCountInString(text) > 180 || len(kb.InlineKeyboard) != 3 {
		t.Fatal("home is not a compact menu")
	}
	for _, noise := range []string{"Подключить группу", "Карточки придут", "/status", "Журнал ограничен", "не банит"} {
		if strings.Contains(text, noise) {
			t.Fatal("home repeats secondary instructions", noise)
		}
	}
	connect, connectKB := RenderHome(HomePanel{ID: 1, Revision: 2, View: "connect"}, data)
	if !strings.Contains(connect, "внешнем AI") || !strings.Contains(connect, "подтвердите заявку") || !strings.Contains(connectKB.InlineKeyboard[0][0].URL, "?startgroup=connect") {
		t.Fatal("connection guide lost consent or usable group link")
	}
	data.PendingRequests = 2
	_, pendingKB := RenderHome(HomePanel{ID: 1, Revision: 2, View: "home"}, data)
	if len(pendingKB.InlineKeyboard) != 4 || !strings.Contains(pendingKB.InlineKeyboard[0][0].Text, "2") {
		t.Fatal("pending requests are not actionable")
	}
}

func TestHomeViewsAreEscapedAndButtonsBounded(t *testing.T) {
	data := HomeData{ChatTitles: []string{"Fixture <group> & name"}, AIEnabled: true, AutoDiscover: true, Model: "fixture/<model>", BotUsername: "bad?inject=true"}
	for _, view := range []string{"home", "chats", "connect", "help", "status", "requests"} {
		text, kb := RenderHome(HomePanel{ID: 9223372036854775807, Revision: 9223372036854775807, View: view}, data)
		if utf8.RuneCountInString(text) > 4096 || strings.Contains(text, "<group>") || strings.Contains(text, "<model>") {
			t.Fatal("unsafe home rendering", view)
		}
		for _, row := range kb.InlineKeyboard {
			for _, button := range row {
				if len(button.CallbackData) > 64 || button.Text == "" || button.URL != "" {
					t.Fatal("unsafe callback or invalid username link", view)
				}
			}
		}
	}
	data.AutoDiscover = false
	text, _ := RenderHome(HomePanel{ID: 1, Revision: 1, View: "connect"}, data)
	if !strings.Contains(text, "выключено") {
		t.Fatal("disabled discovery still promises onboarding")
	}
}

func TestHomeViewsFitPhotoCaption(t *testing.T) {
	data := HomeData{PendingRequests: 20, PendingReviews: 9223372036854775807, CompletedReviews: 9223372036854775807, AIEnabled: true, AutoDiscover: true, Incomplete: true, Model: strings.Repeat("m", 160), BotUsername: "fixture_bot"}
	for i := 0; i < 20; i++ {
		data.ChatTitles = append(data.ChatTitles, strings.Repeat("🛡<&", 100))
	}
	tags := regexp.MustCompile(`<[^>]*>`)
	for _, view := range []string{"home", "chats", "connect", "help", "status", "requests"} {
		text, _ := RenderHome(HomePanel{ID: 1, Revision: 1, View: view}, data)
		plain := html.UnescapeString(tags.ReplaceAllString(text, ""))
		if units := len(utf16.Encode([]rune(plain))); units > 1024 {
			t.Fatalf("%s caption exceeds Telegram limit: %d UTF-16 units", view, units)
		}
	}
}

func TestHomeChatPaginationAndUnavailableData(t *testing.T) {
	data := HomeData{Incomplete: true}
	for i := 0; i < 12; i++ {
		data.ChatTitles = append(data.ChatTitles, fmt.Sprintf("Fixture %02d", i))
	}
	text, kb := RenderHome(HomePanel{ID: 1, Revision: 1, View: "chats", Page: 2}, data)
	if !strings.Contains(text, "Fixture 10") || strings.Contains(text, "Fixture 00") || !strings.Contains(text, "3/3") || !strings.Contains(text, "недоступны") {
		t.Fatal("bad chat pagination or hidden partial data")
	}
	if len(kb.InlineKeyboard[0]) != 1 {
		t.Fatal("pagination has an unavailable next page")
	}
	text, _ = RenderHome(HomePanel{ID: 1, Revision: 1, View: "chats"}, HomeData{Incomplete: true})
	if strings.Contains(text, "нет подключённых") || !strings.Contains(text, "недоступны") {
		t.Fatal("unavailable list incorrectly claims no connected chats")
	}
	text, _ = RenderHome(HomePanel{ID: 1, Revision: 1, View: "requests"}, HomeData{PendingRequests: 1})
	if strings.Contains(text, "отправлены") {
		t.Fatal("queued request claimed delivered before receipt")
	}
}
