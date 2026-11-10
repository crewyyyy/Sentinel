package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func TestCustomEmojiFallbackPreservesActionsAndStyles(t *testing.T) {
	for _, method := range []string{"send", "edit"} {
		t.Run(method, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if err := r.ParseMultipartForm(1 << 20); err != nil {
					t.Error(err)
					return
				}
				if r.MultipartForm != nil {
					defer r.MultipartForm.RemoveAll()
				}
				text := r.FormValue("text")
				var kb models.InlineKeyboardMarkup
				if err := json.Unmarshal([]byte(r.FormValue("reply_markup")), &kb); err != nil {
					t.Error(err)
					return
				}
				b := kb.InlineKeyboard[0][0]
				if b.CallbackData != "conn:1:1:yes" || b.Style != "success" {
					t.Error("fallback changed the action", b)
				}
				if calls == 1 {
					if !strings.Contains(text, "<tg-emoji") || b.IconCustomEmojiID == "" {
						t.Error("initial request lost requested custom icons")
					}
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprint(w, `{"ok":false,"error_code":400,"description":"Bad Request: not enough rights to use custom emoji"}`)
					return
				}
				if strings.Contains(text, "<tg-emoji") || b.IconCustomEmojiID != "" || !strings.Contains(text, "<b>Подключить</b>") {
					t.Error("invalid plain fallback", text, kb)
				}
				fmt.Fprint(w, `{"ok":true,"result":{"message_id":77,"date":1,"chat":{"id":111,"type":"private"}}}`)
			}))
			defer server.Close()
			b, err := bot.New("123:fake", bot.WithSkipGetMe(), bot.WithServerURL(server.URL), bot.WithHTTPClient(0, server.Client()))
			if err != nil {
				t.Fatal(err)
			}
			s := &Sender{Bot: b}
			kb := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "Подключить", CallbackData: "conn:1:1:yes", IconCustomEmojiID: "5774022692642492953", Style: "success"}}}}
			invoke := func() error {
				text := `<tg-emoji emoji-id="5774022692642492953">✅</tg-emoji> <b>Подключить</b>`
				if method == "edit" {
					return s.Edit(context.Background(), 111, 77, text, kb)
				}
				_, err := s.Send(context.Background(), 111, text, kb)
				return err
			}
			if err := invoke(); err != nil {
				t.Fatal(err)
			}
			if err := invoke(); err != nil {
				t.Fatal(err)
			}
			if calls != 3 || kb.InlineKeyboard[0][0].IconCustomEmojiID == "" {
				t.Fatal("unsupported decoration retried forever or shared keyboard was mutated", calls)
			}
		})
	}
}

func TestEmojiFallbackDoesNotRetryRateLimitOrUnknownTransportOutcome(t *testing.T) {
	for _, code := range []int{429, 500} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(code)
				fmt.Fprintf(w, `{"ok":false,"error_code":%d,"description":"failed","parameters":{"retry_after":17}}`, code)
			}))
			defer server.Close()
			b, err := bot.New("123:fake", bot.WithSkipGetMe(), bot.WithServerURL(server.URL), bot.WithHTTPClient(0, server.Client()))
			if err != nil {
				t.Fatal(err)
			}
			s := &Sender{Bot: b}
			if _, err := s.Send(context.Background(), 111, `<tg-emoji emoji-id="5774022692642492953">✅</tg-emoji> Text`, nil); err == nil || calls != 1 {
				t.Fatal("presentation fallback bypassed durable retry policy", err, calls)
			}
		})
	}
}
