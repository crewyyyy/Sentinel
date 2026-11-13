// Package visual classifies bounded image samples independently of text review.
package visual

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"time"
)

const Model = "fal-ai/imageutils/nsfw"
const Endpoint = "https://fal.run/" + Model
const MaxFrameBytes = 4 << 20
const maxResponseBytes = 16 << 10

type Error struct {
	Code       string
	Permanent  bool
	RetryAfter time.Duration
}

func (e *Error) Error() string { return e.Code }

type Classifier interface {
	Classify(context.Context, []byte) (float64, error)
}

type Fal struct {
	Client *http.Client
	Key    string
	URL    string
}

func (p *Fal) Classify(ctx context.Context, image []byte) (float64, error) {
	if len(image) == 0 || len(image) > MaxFrameBytes {
		return 0, &Error{Code: "image_limit", Permanent: true}
	}
	url := p.URL
	if url == "" {
		url = Endpoint
	}
	payload, err := json.Marshal(map[string]string{"image_url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(image)})
	if err != nil {
		return 0, &Error{Code: "image_request_invalid", Permanent: true}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return 0, &Error{Code: "image_request_invalid", Permanent: true}
	}
	req.Header.Set("Authorization", "Key "+p.Key)
	req.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 30 * time.Second}
	if p.Client != nil {
		client = *p.Client
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return 0, &Error{Code: "fal_unavailable"}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		delay := time.Duration(0)
		if n, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && n > 0 && n <= 86400 {
			delay = time.Duration(n) * time.Second
		}
		return 0, &Error{Code: fmt.Sprintf("fal_http_%d", resp.StatusCode), RetryAfter: delay, Permanent: resp.StatusCode == 400 || resp.StatusCode == 413 || resp.StatusCode == 422}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return 0, &Error{Code: "fal_body_limit"}
	}
	var result struct {
		Probability *float64 `json:"nsfw_probability"`
	}
	if json.Unmarshal(body, &result) != nil || result.Probability == nil || math.IsNaN(*result.Probability) || math.IsInf(*result.Probability, 0) || *result.Probability < 0 || *result.Probability > 1 {
		return 0, &Error{Code: "fal_invalid_result"}
	}
	return *result.Probability, nil
}
