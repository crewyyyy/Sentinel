package review

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sentinel/internal/domain"
	"sentinel/prompts"
)

func TestCatalogBoundedSortedAndPrivateErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" || r.Header.Get("Authorization") != "Bearer fixture-key" {
			t.Error("catalog request boundary")
		}
		json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"id": "z/model"}, map[string]string{"id": "a/model"}, map[string]string{"id": "a/model"}, map[string]string{"id": "<invalid>"}}})
	}))
	defer server.Close()
	p := &Compatible{Client: server.Client(), BaseURL: server.URL, Key: "fixture-key"}
	ids, err := p.Models(context.Background())
	if err != nil || len(ids) != 2 || ids[0] != "a/model" {
		t.Fatal(ids, err)
	}
	for _, body := range []string{`{"data":[]}`, `{"data":[{"id":"<bad>"}]}`, strings.Repeat("x", (512<<10)+1), `not json fixture-secret`} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
		p := &Compatible{Client: s.Client(), BaseURL: s.URL}
		_, err := p.Models(context.Background())
		s.Close()
		if err == nil || strings.Contains(err.Error(), "fixture-secret") {
			t.Fatal("invalid catalog/error leakage", err)
		}
	}
}
func TestCatalogNeverFollowsRedirects(t *testing.T) {
	reached := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer server.Close()
	p := &Compatible{Client: server.Client(), BaseURL: server.URL, Key: "fixture-key"}
	if _, err := p.Models(context.Background()); err == nil || reached {
		t.Fatal("catalog followed redirect", err)
	}
}
func TestCatalogHTTPStatusIsActionableWithoutProviderBody(t *testing.T) {
	for _, test := range []struct {
		status int
		code   string
	}{{401, "catalog_unauthorized"}, {403, "catalog_forbidden"}, {429, "catalog_rate_limited"}, {500, "catalog_http_error"}} {
		t.Run(test.code, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				w.Write([]byte(`{"error":"fixture-private-key"}`))
			}))
			defer server.Close()
			provider := &Compatible{Client: server.Client(), BaseURL: server.URL}
			_, err := provider.Models(context.Background())
			failure, ok := err.(*Error)
			if !ok || failure.Code != test.code || strings.Contains(err.Error(), "fixture-private-key") {
				t.Fatal("catalog status lost or body leaked", err)
			}
		})
	}
}

func TestPerAttemptModelAndVersionedJSONPrompt(t *testing.T) {
	keys := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Model    string
			Messages []struct{ Role, Content string }
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if len(payload.Messages) != 2 || !strings.Contains(payload.Messages[0].Content, "json") || !strings.Contains(payload.Messages[1].Content, `"format":"json"`) {
			t.Error("JSON-mode-compatible instruction missing")
		}
		if payload.Model != "chosen/model" && payload.Model != "other/model" {
			t.Error("per-attempt model ignored", payload.Model)
		}
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": valid}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 12, "completion_tokens": 10}})
	}))
	defer server.Close()
	p := &Compatible{Client: server.Client(), BaseURL: server.URL, Model: "default/model", InputPrice: 100, OutputPrice: 100}
	for _, model := range []string{"chosen/model", "chosen/model", "other/model"} {
		r, err := p.Review(context.Background(), domain.ReviewRequest{Model: model, PromptVersion: prompts.CurrentVersion})
		if err != nil {
			t.Fatal(err)
		}
		if r.Model != model || r.Usage.EstimatedCost != nil {
			t.Fatal("wrong model or fabricated cross-model price", r)
		}
	}
	if keys[0] != keys[1] || keys[1] == keys[2] {
		t.Fatal("retry/model idempotency mismatch", keys)
	}
	if p.Model != "default/model" {
		t.Fatal("shared provider was mutated")
	}
}
