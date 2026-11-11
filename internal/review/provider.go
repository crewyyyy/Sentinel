// Package review implements a bounded, strict, provider-neutral advisory classifier.
package review

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"sentinel/internal/domain"
	"sentinel/internal/privacy"
	"sentinel/prompts"
)

type Provider interface {
	Review(context.Context, domain.ReviewRequest) (domain.ReviewResponse, error)
}
type Compatible struct {
	Client                  *http.Client
	BaseURL, Key, Model     string
	InputPrice, OutputPrice float64
}
type Error struct {
	Code       string
	RetryAfter time.Duration
}

func (e *Error) Error() string { return e.Code }
func Parse(data []byte) (domain.ReviewResult, error) {
	if len(data) > 16384 {
		return domain.ReviewResult{}, fmt.Errorf("review response exceeds limit")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return domain.ReviewResult{}, fmt.Errorf("review invalid JSON")
	}
	keysDecoder := json.NewDecoder(bytes.NewReader(data))
	if _, err := keysDecoder.Token(); err != nil {
		return domain.ReviewResult{}, fmt.Errorf("review invalid object")
	}
	seen := map[string]bool{}
	for keysDecoder.More() {
		key, err := keysDecoder.Token()
		if err != nil {
			return domain.ReviewResult{}, fmt.Errorf("review invalid key")
		}
		name, ok := key.(string)
		if !ok || seen[name] {
			return domain.ReviewResult{}, fmt.Errorf("review duplicate key")
		}
		seen[name] = true
		var value json.RawMessage
		if err = keysDecoder.Decode(&value); err != nil {
			return domain.ReviewResult{}, fmt.Errorf("review invalid value")
		}
	}
	for _, k := range []string{"category", "severity", "confidence", "recommendation", "reason_codes", "explanation_ru", "needs_human_review"} {
		if v, ok := raw[k]; !ok || string(v) == "null" {
			return domain.ReviewResult{}, fmt.Errorf("review missing field %s", k)
		}
	}
	var r domain.ReviewResult
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return r, fmt.Errorf("review schema mismatch")
	}
	allowed := func(s string, vs string) bool {
		for _, v := range strings.Fields(vs) {
			if s == v {
				return true
			}
		}
		return false
	}
	if !allowed(r.Category, "legitimate benign_criticism uncertain unsolicited_promotion referral_spam impersonation phishing credential_theft other_abuse") || !allowed(r.Severity, "none low medium high critical") || !allowed(r.Recommendation, "ignore monitor review warn mute ban") {
		return r, fmt.Errorf("review invalid enum")
	}
	if math.IsNaN(r.Confidence) || math.IsInf(r.Confidence, 0) || r.Confidence < 0 || r.Confidence > 1 || len(r.ReasonCodes) > 10 || len(r.ExplanationRU) == 0 || len(r.ExplanationRU) > 3000 {
		return r, fmt.Errorf("review bounds violation")
	}
	for _, code := range r.ReasonCodes {
		if !reasonCode.MatchString(code) {
			return r, fmt.Errorf("review invalid reason code")
		}
	}
	r.ExplanationRU = privacy.Redact(r.ExplanationRU)
	if unsupportedActionClaim.MatchString(r.ExplanationRU) {
		r.ExplanationRU = "Мнение AI не подтверждает выполненных санкций. Проверьте исходные сигналы и независимые события модерации."
	}
	return r, nil
}

var reasonCode = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
var unsupportedActionClaim = regexp.MustCompile(`(?i)(забан|замуч|удал[её]н|санкци[яи] (?:применена|выполнена)|banned|muted|deleted)`)

func (p *Compatible) Review(ctx context.Context, r domain.ReviewRequest) (domain.ReviewResponse, error) {
	model := p.Model
	if r.Model != "" {
		model = r.Model
	}
	r.PromptVersion = prompts.CurrentVersion
	keyData, keyErr := json.Marshal(struct {
		Request domain.ReviewRequest
		Model   string
	}{r, model})
	if keyErr != nil {
		return domain.ReviewResponse{}, fmt.Errorf("invalid review request")
	}
	requestKey := sha256.Sum256(keyData)
	// Only job-local pseudonyms leave the service; Telegram identifiers and usernames do not help classification.
	people := map[int64]int64{}
	next := int64(0)
	var scrub func(domain.Message) domain.Message
	scrub = func(m domain.Message) domain.Message {
		m = privacy.Message(m)
		m.ChatID = 0
		m.ID = 0
		m.ThreadID = 0
		m.ReplyID = 0
		m.Username = ""
		m.ChatUsername = ""
		// Local extractor metadata adds no context beyond the already supplied signals and URLs.
		m.Mentions = nil
		m.FeaturesVersion = 0
		m.FeaturesPartial = false
		m.URLsPartial = false
		m.Date = time.Time{}
		m.EditedAt = nil
		if m.UserID != nil {
			id, ok := people[*m.UserID]
			if !ok {
				next++
				id = next
				people[*m.UserID] = id
			}
			m.UserID = &id
		}
		m.SenderChatID = nil
		m.ContentHash = ""
		if m.Reply != nil {
			reply := scrub(*m.Reply)
			m.Reply = &reply
		}
		return m
	}
	boundURLs := func(us []domain.URL, n int) []domain.URL {
		us = append([]domain.URL(nil), us...)
		if len(us) > n {
			us = us[:n]
		}
		for i := range us {
			us[i].Value = privacy.Limit(us[i].Value, 384)
			us[i].QueryKeys = nil
			us[i].QueryKeysPartial = false
			us[i].TelegramUsername = ""
		}
		return us
	}
	trim := func(m domain.Message, n int) domain.Message {
		m = scrub(m)
		m.Text = privacy.Limit(m.Text, n)
		m.Caption = privacy.Limit(m.Caption, n)
		m.Entities = nil
		m.CaptionEntities = nil
		m.URLs = boundURLs(m.URLs, 8)
		if m.Reply != nil {
			reply := *m.Reply
			reply.Text = privacy.Limit(reply.Text, 1000)
			reply.Caption = privacy.Limit(reply.Caption, 1000)
			reply.Entities = nil
			reply.CaptionEntities = nil
			reply.URLs = boundURLs(reply.URLs, 4)
			reply.Reply = nil
			m.Reply = &reply
		}
		return m
	}
	r.Message = trim(r.Message, 6000)
	r.Assessment.URLs = boundURLs(r.Assessment.URLs, 16)
	if len(r.Context) > 6 {
		r.Context = r.Context[:6]
	}
	contextCopy := append([]domain.Message(nil), r.Context...)
	for i := range contextCopy {
		contextCopy[i] = trim(contextCopy[i], 1000)
		contextCopy[i].Reply = nil
	}
	r.Context = contextCopy
	// Some subscription gateways validate JSON mode only against user input, not system instructions.
	evidence, err := json.Marshal(struct {
		Format   string               `json:"format"`
		Evidence domain.ReviewRequest `json:"evidence"`
	}{"json", r})
	if err != nil {
		return domain.ReviewResponse{}, fmt.Errorf("marshal review evidence")
	}
	if len(evidence) > 128<<10 {
		return domain.ReviewResponse{}, fmt.Errorf("review evidence too large")
	}
	payload, err := json.Marshal(map[string]any{"model": model, "messages": []map[string]string{{"role": "system", "content": prompts.ReviewV3}, {"role": "user", "content": string(evidence)}}, "response_format": map[string]string{"type": "json_object"}, "max_tokens": 700, "temperature": 0})
	if err != nil {
		return domain.ReviewResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.BaseURL, "/")+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return domain.ReviewResponse{}, &Error{Code: "request_invalid"}
	}
	req.Header.Set("Content-Type", "application/json")
	if p.Key != "" {
		req.Header.Set("Authorization", "Bearer "+p.Key)
	}
	req.Header.Set("Idempotency-Key", fmt.Sprintf("sentinel-review-%x", requestKey))
	resp, err := p.Client.Do(req)
	if err != nil {
		return domain.ReviewResponse{}, &Error{Code: "transport_or_timeout"}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		retry := time.Duration(0)
		if n, e := strconv.Atoi(resp.Header.Get("Retry-After")); e == nil && n > 0 {
			retry = time.Duration(n) * time.Second
		}
		return domain.ReviewResponse{}, &Error{Code: fmt.Sprintf("provider_http_%d", resp.StatusCode), RetryAfter: retry}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil || len(data) > 65536 {
		return domain.ReviewResponse{}, &Error{Code: "provider_body_limit"}
	}
	var wire struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			Input  *int64 `json:"prompt_tokens"`
			Output *int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err = json.Unmarshal(data, &wire); err != nil || len(wire.Choices) != 1 {
		return domain.ReviewResponse{}, &Error{Code: "provider_invalid_envelope"}
	}
	if f := wire.Choices[0].FinishReason; f != "" && f != "stop" {
		return domain.ReviewResponse{}, &Error{Code: "provider_incomplete_output"}
	}
	result, err := Parse([]byte(wire.Choices[0].Message.Content))
	if err != nil {
		return domain.ReviewResponse{}, &Error{Code: "provider_invalid_review"}
	}
	out := domain.ReviewResponse{Result: result, Provider: "openai_compatible", Model: model}
	if u := wire.Usage; u != nil && u.Input != nil && u.Output != nil && *u.Input >= 0 && *u.Output >= 0 {
		out.Usage.InputTokens = u.Input
		out.Usage.OutputTokens = u.Output
		if model == p.Model && (p.InputPrice > 0 || p.OutputPrice > 0) {
			cost := (float64(*u.Input)*p.InputPrice + float64(*u.Output)*p.OutputPrice) / 1e6
			out.Usage.EstimatedCost = &cost
		}
	}
	return out, nil
}
