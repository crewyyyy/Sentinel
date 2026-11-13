package visual

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFalContractAndSafeErrors(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       float64
		code       string
	}{
		{"ok", `{"nsfw_probability":0.82}`, 200, .82, ""},
		{"zero", `{"nsfw_probability":0}`, 200, 0, ""},
		{"missing", `{}`, 200, 0, "fal_invalid_result"},
		{"null", `{"nsfw_probability":null}`, 200, 0, "fal_invalid_result"},
		{"range", `{"nsfw_probability":1.1}`, 200, 0, "fal_invalid_result"},
		{"negative", `{"nsfw_probability":-0.1}`, 200, 0, "fal_invalid_result"},
		{"malformed", `{"nsfw_probability":`, 200, 0, "fal_invalid_result"},
		{"large", strings.Repeat("x", 16385), 200, 0, "fal_body_limit"},
		{"auth", "private provider diagnostic", 401, 0, "fal_http_401"},
		{"rate", "private provider diagnostic", 429, 0, "fal_http_429"},
		{"server", "private provider diagnostic", 500, 0, "fal_http_500"},
		{"bad image", "private provider diagnostic", 422, 0, "fal_http_422"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.Header.Get("Authorization") != "Key fixture-key" || r.Header.Get("Content-Type") != "application/json" {
					t.Error("incorrect transport contract")
				}
				var payload map[string]string
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				if len(payload) != 1 || payload["image_url"] != "data:image/png;base64,"+base64.StdEncoding.EncodeToString([]byte("fixture-png")) {
					t.Error("provider received unexpected metadata")
				}
				w.Header().Set("Retry-After", "17")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			p := &Fal{URL: server.URL, Key: "fixture-key", Client: server.Client()}
			score, err := p.Classify(context.Background(), []byte("fixture-png"))
			if tc.code == "" {
				if err != nil || score != tc.want {
					t.Fatal(score, err)
				}
				return
			}
			var failure *Error
			if !errors.As(err, &failure) || failure.Code != tc.code {
				t.Fatal("incorrect safe error", err)
			}
			if tc.status != 200 && failure.RetryAfter != 17*time.Second {
				t.Fatal("retry delay lost")
			}
			if tc.status == 422 && !failure.Permanent {
				t.Fatal("invalid image retried indefinitely")
			}
		})
	}
}

func TestFalLimitsRedirectAndTimeout(t *testing.T) {
	calls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer redirect.Close()
	p := &Fal{URL: redirect.URL, Key: "fixture-key"}
	if _, err := p.Classify(context.Background(), []byte("x")); err == nil || calls != 0 {
		t.Fatal("followed provider redirect")
	}
	for _, image := range [][]byte{nil, make([]byte, MaxFrameBytes+1)} {
		if _, err := p.Classify(context.Background(), image); err == nil {
			t.Fatal("input limit missing")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Classify(ctx, []byte("x")); err == nil || err.Error() != "fal_unavailable" {
		t.Fatal("unsafe transport error", err)
	}
}
