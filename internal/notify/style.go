package notify

import (
	"fmt"

	"github.com/go-telegram/bot/models"
)

// Public custom-emoji IDs from t.me/addemoji/tgiosicons, verified with getStickerSet.
const (
	iconLink   = "6028171274939797252"
	iconCheck  = "5774022692642492953"
	iconClose  = "5774077015388852135"
	iconSearch = "6032850693348399258"
	iconAI     = "6030400221232501136"
	iconShield = "6030537007350944596"
	iconStats  = "5936143551854285132"
	iconBack   = "5960671702059848143"
	iconNext   = "5895383238473421210"
)

func heading(icon, fallback, text string) string {
	return fmt.Sprintf(`<tg-emoji emoji-id="%s">%s</tg-emoji> <b>%s</b>`, icon, fallback, text)
}

func button(text, data, icon, style string) models.InlineKeyboardButton {
	return models.InlineKeyboardButton{Text: text, CallbackData: data, IconCustomEmojiID: icon, Style: style}
}

func HelpHeading() string   { return heading(iconShield, "🛡", "Sentinel") }
func StatusHeading() string { return heading(iconStats, "📊", "Состояние") }
