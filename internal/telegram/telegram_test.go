package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/go-telegram/bot"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecode(t *testing.T) {
	cases := []struct {
		raw   string
		check func(*testing.T, string)
	}{
		{`{"update_id":1,"message":{"message_id":2,"from":{"id":3},"chat":{"id":-1001111111111,"type":"supergroup"},"date":1,"text":"😀 hidden","entities":[{"type":"text_link","offset":3,"length":6,"url":"https://hidden.example"}],"quote":{"text":"quote"}}}`, func(t *testing.T, s string) {
			if !strings.Contains(s, `"quoted":true`) || !strings.Contains(s, "hidden.example") {
				t.Fatal(s)
			}
		}},
		{`{"update_id":2,"edited_message":{"message_id":2,"sender_chat":{"id":-1009999999999},"from":{"id":777},"chat":{"id":-1001111111111,"type":"supergroup"},"date":1,"edit_date":2,"caption":"caption","caption_entities":[{"type":"text_mention","offset":0,"length":3,"user":{"id":8,"is_bot":true}}],"photo":[{}]}}`, func(t *testing.T, s string) {
			if strings.Contains(s, `"user_id"`) || !strings.Contains(s, `"sender_chat_id"`) || !strings.Contains(s, `"bot":true`) {
				t.Fatal(s)
			}
		}},
		{`{"update_id":3,"callback_query":{"id":"c","from":{"id":111},"message":{"message_id":77,"chat":{"id":111},"date":0},"data":"act:spam:1"}}`, func(t *testing.T, s string) {
			if !strings.Contains(s, `"message_id":77`) {
				t.Fatal(s)
			}
		}},
		{`{"update_id":4,"chat_member":{"chat":{"id":-1001111111111},"from":{"id":111},"date":1,"old_chat_member":{"status":"member","user":{"id":123}},"new_chat_member":{"status":"kicked","user":{"id":123},"until_date":0}}}`, func(t *testing.T, s string) {
			if !strings.Contains(s, `"actor_id":111`) || !strings.Contains(s, `"status":"kicked"`) {
				t.Fatal(s)
			}
		}},
		{`{"update_id":5,"my_chat_member":{"chat":{"id":-1001111111111},"from":{"id":111},"date":1,"old_chat_member":{"status":"member","user":{"id":999}},"new_chat_member":{"status":"administrator","user":{"id":999}}}}`, func(t *testing.T, s string) {
			if !strings.Contains(s, `"self":true`) {
				t.Fatal(s)
			}
		}},
	}
	for _, tc := range cases {
		u, err := Decode([]byte(tc.raw))
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(u)
		if err != nil {
			t.Fatal(err)
		}
		tc.check(t, string(b))
	}
	if _, err := Decode([]byte(`{"message":{}}`)); err == nil {
		t.Fatal("missing update_id accepted")
	}
}
func TestDeepLinks(t *testing.T) {
	for _, tc := range []struct {
		chat, id, thread int64
		want             string
	}{{-1001111111111, 7, 0, "https://t.me/c/1111111111/7"}, {-1001111111111, 7, 3, "https://t.me/c/1111111111/3/7"}, {-123, 7, 0, ""}, {123, 7, 0, ""}, {-1001111111111, 0, 0, ""}} {
		got, err := MessageLink(tc.chat, tc.id, tc.thread)
		if got != tc.want || (err != nil) != (tc.want == "") {
			t.Fatal(got, err)
		}
	}
}
func TestPollingOffsetsAnd429(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var p struct {
			Offset  int64    `json:"offset"`
			Allowed []string `json:"allowed_updates"`
		}
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Error(err)
		}
		if p.Offset != 78 || len(p.Allowed) != 5 {
			t.Error(p)
		}
		if calls == 1 {
			io.WriteString(w, `{"ok":true,"result":[{"update_id":78}]}`)
		} else {
			w.WriteHeader(429)
			io.WriteString(w, `{"ok":false,"error_code":429,"parameters":{"retry_after":17}}`)
		}
	}))
	defer server.Close()
	p := &Poller{Endpoint: server.URL, Client: server.Client()}
	if u, err := p.Fetch(context.Background(), 78); err != nil || len(u) != 1 {
		t.Fatal(u, err)
	}
	if _, err := p.Fetch(context.Background(), 78); err == nil {
		t.Fatal("429 accepted")
	}
}
func TestSenderClearsKeyboardAfterConnectionDecision(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			return
		}
		if r.MultipartForm != nil {
			defer r.MultipartForm.RemoveAll()
		}
		markup := r.FormValue("reply_markup")
		if markup != `{"inline_keyboard":[]}` {
			t.Errorf("resolved card must explicitly clear buttons, got %q", markup)
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"ok":false,"error_code":400,"description":"Bad Request: object expected as reply markup"}`)
			return
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":77,"date":1,"chat":{"id":111,"type":"private"}}}`)
	}))
	defer server.Close()
	b, err := bot.New("123:fake", bot.WithSkipGetMe(), bot.WithServerURL(server.URL), bot.WithHTTPClient(0, server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	sender := Sender{Bot: b}
	if err := sender.Edit(context.Background(), 111, 77, "<b>Чат подключён</b>", nil); err != nil {
		t.Fatal(err)
	}
}

func TestSenderNoSanctions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
			t.Error("unexpected method", r.URL.Path)
		}
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if !strings.Contains(string(b), `"is_disabled":true`) {
			t.Error(string(b))
		}
		if strings.Contains(string(b), `name="reply_markup"`) {
			t.Error("plain send must omit reply_markup instead of sending null")
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":77,"date":1,"chat":{"id":111,"type":"private"}}}`)
	}))
	defer server.Close()
	b, err := bot.New("123:fake", bot.WithSkipGetMe(), bot.WithServerURL(server.URL), bot.WithHTTPClient(0, server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	sender := Sender{Bot: b}
	if id, err := sender.Send(context.Background(), 111, EscapeHTML("<untrusted>"), nil); err != nil || id != 77 {
		t.Fatal(id, err)
	}
}
