package telegram

import (
	"context"
	"fmt"
	"github.com/go-telegram/bot"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPrivateDestinations(t *testing.T) {
	for _, tc := range []struct {
		payload string
		private bool
	}{
		{`{"id":111,"type":"private","username":"fixture_admin"}`, true},
		{`{"id":-1001111111111,"type":"supergroup","username":"public_fixture"}`, false},
		{`{"id":-1001111111111,"type":"supergroup","active_usernames":["public_fixture"]}`, false},
		{`{"id":-1001111111111,"type":"supergroup"}`, true},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, `{"ok":true,"result":%s}`, tc.payload) }))
		b, err := bot.New("123:fake", bot.WithSkipGetMe(), bot.WithServerURL(server.URL), bot.WithHTTPClient(0, server.Client()))
		if err != nil {
			t.Fatal(err)
		}
		s := Sender{Bot: b}
		private, err := s.PrivateDestination(context.Background(), 111)
		server.Close()
		if err != nil || private != tc.private {
			t.Fatal(private, err, tc)
		}
	}
}
