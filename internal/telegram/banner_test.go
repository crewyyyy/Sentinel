package telegram

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func TestBannerIsEmbeddedAndUploadedWithButtons(t *testing.T) {
	config, err := png.DecodeConfig(bytes.NewReader(homeBanner))
	if err != nil || config.Width != 1536 || config.Height != 864 || len(homeBanner) > 10<<20 {
		t.Fatal("invalid embedded menu photo", config, err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if !strings.HasSuffix(r.URL.Path, "/sendPhoto") {
			t.Error("banner used wrong method")
		}
		if err := r.ParseMultipartForm(2 << 20); err != nil {
			t.Error(err)
			return
		}
		defer r.MultipartForm.RemoveAll()
		if r.FormValue("caption") != "<b>Sentinel</b>" || r.FormValue("parse_mode") != "HTML" || r.FormValue("chat_id") != "111" {
			t.Error("caption or destination missing")
		}
		file, _, err := r.FormFile("photo")
		if err != nil {
			t.Error(err)
			return
		}
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil || sha256.Sum256(data) != sha256.Sum256(homeBanner) {
			t.Error("photo upload changed or was empty", err)
		}
		var kb models.InlineKeyboardMarkup
		if err := json.Unmarshal([]byte(r.FormValue("reply_markup")), &kb); err != nil || kb.InlineKeyboard[0][0].CallbackData != "nav:1:1:help:0" {
			t.Error("banner lost navigation", err)
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":77,"date":1,"chat":{"id":111,"type":"private"},"photo":[{"file_id":"fixture","file_unique_id":"fixture","width":1536,"height":864}]}}`)
	}))
	defer server.Close()
	b, err := bot.New("123:fake", bot.WithSkipGetMe(), bot.WithServerURL(server.URL), bot.WithHTTPClient(0, server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	s := &Sender{Bot: b}
	kb := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "Помощь", CallbackData: "nav:1:1:help:0"}}}}
	id, err := s.SendBanner(context.Background(), 111, "<b>Sentinel</b>", kb)
	if err != nil || id != 77 || calls != 1 {
		t.Fatal(id, err, calls)
	}
}

func TestBannerEmojiFallbackRewindsUploadAndEditsCaption(t *testing.T) {
	for _, method := range []string{"sendPhoto", "editMessageCaption"} {
		t.Run(method, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if !strings.HasSuffix(r.URL.Path, "/"+method) {
					t.Error("wrong media method")
				}
				if err := r.ParseMultipartForm(2 << 20); err != nil {
					t.Error(err)
					return
				}
				defer r.MultipartForm.RemoveAll()
				if method == "sendPhoto" {
					file, _, err := r.FormFile("photo")
					if err != nil {
						t.Error(err)
						return
					}
					data, err := io.ReadAll(file)
					file.Close()
					if err != nil || len(data) != len(homeBanner) {
						t.Error("fallback reused an exhausted upload reader", err)
					}
				}
				if calls == 1 {
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprint(w, `{"ok":false,"error_code":400,"description":"Bad Request: custom emoji unavailable"}`)
					return
				}
				if strings.Contains(r.FormValue("caption"), "tg-emoji") || strings.Contains(r.FormValue("reply_markup"), "icon_custom_emoji_id") || !strings.Contains(r.FormValue("caption"), "<b>Sentinel</b>") {
					t.Error("invalid caption fallback")
				}
				fmt.Fprint(w, `{"ok":true,"result":{"message_id":77,"date":1,"chat":{"id":111,"type":"private"}}}`)
			}))
			defer server.Close()
			b, err := bot.New("123:fake", bot.WithSkipGetMe(), bot.WithServerURL(server.URL), bot.WithHTTPClient(0, server.Client()))
			if err != nil {
				t.Fatal(err)
			}
			s := &Sender{Bot: b}
			kb := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "Помощь", CallbackData: "nav:1:1:help:0", IconCustomEmojiID: "5774022692642492953"}}}}
			caption := `<tg-emoji emoji-id="5774022692642492953">✅</tg-emoji> <b>Sentinel</b>`
			if method == "sendPhoto" {
				_, err = s.SendBanner(context.Background(), 111, caption, kb)
			} else {
				err = s.EditCaption(context.Background(), 111, 77, caption, kb)
			}
			if err != nil || calls != 2 || !s.emojiUnavailable.Load() || kb.InlineKeyboard[0][0].IconCustomEmojiID == "" {
				t.Fatal("media emoji fallback failed or mutated keyboard", err, calls)
			}
		})
	}
}
