package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-telegram/bot"
	"sentinel/internal/domain"
)

func TestMediaNormalization(t *testing.T) {
	for _, tc := range []struct{ name, fields, kind, format, id string }{
		{"photo", `"photo":[{"file_id":"small","file_unique_id":"s","width":10,"height":10},{"file_id":"large","file_unique_id":"l","width":100,"height":100}]`, "photo", "image", "large"},
		{"sticker", `"sticker":{"file_id":"static","file_unique_id":"s"}`, "sticker", "image", "static"},
		{"tgs", `"sticker":{"file_id":"animated","file_unique_id":"a","is_animated":true,"thumbnail":{"file_id":"preview"}}`, "sticker", "tgs", "animated"},
		{"video sticker", `"sticker":{"file_id":"video","file_unique_id":"v","is_video":true}`, "sticker", "video", "video"},
		{"gif", `"animation":{"file_id":"gif","file_unique_id":"g","duration":3}`, "animation", "video", "gif"},
		{"document", `"document":{"file_id":"doc","file_unique_id":"d","mime_type":"image/gif"}`, "document", "image", "doc"},
		{"custom emoji", `"text":"😎","entities":[{"type":"custom_emoji","offset":0,"length":2,"custom_emoji_id":"1234"},{"type":"custom_emoji","offset":0,"length":2,"custom_emoji_id":"1234"}]`, "custom_emoji", "", ""},
		{"caption emoji", `"caption":"😎","caption_entities":[{"type":"custom_emoji","offset":0,"length":2,"custom_emoji_id":"1234"}]`, "custom_emoji", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := fmt.Sprintf(`{"update_id":1,"message":{"message_id":2,"date":1,"chat":{"id":111,"type":"private"},"from":{"id":111},%s}}`, tc.fields)
			u, err := Decode(json.RawMessage(raw))
			if err != nil {
				t.Fatal(err)
			}
			if len(u.Message.Media) != 1 {
				t.Fatal(u.Message.Media)
			}
			m := u.Message.Media[0]
			if m.Kind != tc.kind || m.Format != tc.format || m.FileID != tc.id {
				t.Fatal(m)
			}
			if tc.kind == "custom_emoji" && m.EmojiID != "1234" {
				t.Fatal("emoji missing")
			}
		})
	}
	entities := []entity{}
	for i := 0; i < 20; i++ {
		entities = append(entities, entity{Type: "custom_emoji", CustomEmojiID: fmt.Sprint(1000 + i)})
	}
	m := normalizeMessage(&message{Entities: entities}, 0)
	if len(m.Media) != domain.MaxMessageMedia || !m.MediaPartial {
		t.Fatal("unbounded custom emoji")
	}
	reply := normalizeMessage(&message{Reply: &message{Entities: entities}}, 0)
	if len(reply.Media) != 0 || len(reply.Reply.Media) != 0 {
		t.Fatal("scanned embedded reply media")
	}
	out, _ := extractMedia(&message{Document: json.RawMessage(`{"file_id":"svg","file_unique_id":"svg","mime_type":"image/svg+xml"}`)})
	if len(out) != 0 {
		t.Fatal("SVG accepted")
	}
}

func TestMediaDownloadBoundariesAndTGSPreview(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		format     string
		limit      int64
		redirect   bool
		wantError  bool
	}{
		{"static", "photos/file.png", "image", 1024, false, false},
		{"preview", "stickers/preview.webp", "tgs", 1024, false, false},
		{"path traversal", "../secret", "image", 1024, false, true},
		{"URL", "https://private.example/a", "image", 1024, false, true},
		{"encoded", "photos/%2e%2e/x", "image", 1024, false, true},
		{"oversize", "photos/file.png", "image", 3, false, true},
		{"redirect", "photos/file.png", "image", 1024, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			downloaded := 0
			fileID := ""
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/getFile"):
					if err := r.ParseMultipartForm(1 << 20); err != nil {
						t.Error(err)
						return
					}
					defer r.MultipartForm.RemoveAll()
					fileID = r.FormValue("file_id")
					fmt.Fprintf(w, `{"ok":true,"result":{"file_id":"fixture","file_unique_id":"fixture","file_path":%q}}`, tc.path)
				case strings.HasPrefix(r.URL.Path, "/file/bot123:fake/"):
					downloaded++
					if tc.redirect {
						http.Redirect(w, r, "https://private.example/", 302)
						return
					}
					fmt.Fprint(w, "image-bytes")
				default:
					t.Error("unexpected method")
					w.WriteHeader(400)
				}
			}))
			defer srv.Close()
			b, err := bot.New("123:fake", bot.WithSkipGetMe(), bot.WithServerURL(srv.URL), bot.WithHTTPClient(0, srv.Client()))
			if err != nil {
				t.Fatal(err)
			}
			s := &MediaSource{Bot: b, Client: srv.Client(), FileBase: srv.URL}
			data, preview, _, err := s.Fetch(context.Background(), domain.Media{FileID: "original", ThumbnailID: "preview", Format: tc.format}, tc.limit)
			if (err != nil) != tc.wantError {
				t.Fatal("download outcome", err)
			}
			if err != nil && strings.Contains(err.Error(), "123:fake") {
				t.Fatal("token leaked")
			}
			if !tc.wantError && (string(data) != "image-bytes" || downloaded != 1) {
				t.Fatal("download missing")
			}
			if tc.format == "tgs" && (!preview || fileID != "preview") {
				t.Fatal("TGS not checked via preview")
			}
		})
	}
}

func TestCustomEmojiDownloadAndReply(t *testing.T) {
	emojiLookups := 0
	replies := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/file/") {
			fmt.Fprint(w, "emoji-png")
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			return
		}
		defer r.MultipartForm.RemoveAll()
		switch {
		case strings.HasSuffix(r.URL.Path, "/getCustomEmojiStickers"):
			emojiLookups++
			if r.FormValue("custom_emoji_ids") != `["1234"]` {
				t.Error("incorrect emoji lookup")
			}
			fmt.Fprint(w, `{"ok":true,"result":[{"file_id":"emoji-file","file_unique_id":"emoji-unique","custom_emoji_id":"1234","type":"custom_emoji","width":100,"height":100,"is_animated":false,"is_video":false}]}`)
		case strings.HasSuffix(r.URL.Path, "/getFile"):
			fmt.Fprint(w, `{"ok":true,"result":{"file_id":"emoji-file","file_unique_id":"emoji-unique","file_path":"stickers/emoji.webp"}}`)
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			replies++
			var reply struct {
				ID int `json:"message_id"`
			}
			if err := json.Unmarshal([]byte(r.FormValue("reply_parameters")), &reply); err != nil || reply.ID != 55 {
				t.Error("response is not a reply", err)
			}
			if r.FormValue("parse_mode") != "HTML" {
				t.Error("HTML formatting missing")
			}
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":77,"date":1,"chat":{"id":111,"type":"private"}}}`)
		default:
			io.WriteString(w, `{"ok":false,"error_code":400,"description":"unexpected method"}`)
		}
	}))
	defer srv.Close()
	b, err := bot.New("123:fake", bot.WithSkipGetMe(), bot.WithServerURL(srv.URL), bot.WithHTTPClient(0, srv.Client()))
	if err != nil {
		t.Fatal(err)
	}
	source := &MediaSource{Bot: b, Client: srv.Client(), FileBase: srv.URL}
	data, preview, _, err := source.Fetch(context.Background(), domain.Media{EmojiID: "1234"}, 1024)
	if err != nil || preview || string(data) != "emoji-png" || emojiLookups != 1 {
		t.Fatal("emoji download", err)
	}
	if _, _, _, err = source.Fetch(context.Background(), domain.Media{Format: "tgs"}, 1024); err == nil {
		t.Fatal("missing preview accepted")
	}
	sender := &Sender{Bot: b}
	id, err := sender.Reply(context.Background(), 111, 55, "✅ <b>Низкий риск NSFW</b>", nil)
	if err != nil || id != 77 || replies != 1 {
		t.Fatal("reply delivery", err)
	}
}
