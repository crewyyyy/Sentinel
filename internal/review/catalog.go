package review

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

const MaxCatalogModels = 1000

var modelID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/+@-]{0,159}$`)

func ValidModelID(id string) bool { return modelID.MatchString(id) }

type ModelCatalog interface {
	Models(context.Context) ([]string, error)
}

// Models reads only metadata from the configured endpoint. It never follows redirects or returns provider bodies as errors.
func (p *Compatible) Models(ctx context.Context) ([]string, error) {
	client := http.Client{Timeout: 15 * time.Second}
	if p.Client != nil {
		client = *p.Client
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(p.BaseURL, "/")+"/models", nil)
	if err != nil {
		return nil, &Error{Code: "catalog_request_invalid"}
	}
	if p.Key != "" {
		req.Header.Set("Authorization", "Bearer "+p.Key)
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, &Error{Code: "catalog_unavailable"}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, &Error{Code: "catalog_http_error"}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (512<<10)+1))
	if err != nil || len(body) > 512<<10 {
		return nil, &Error{Code: "catalog_body_limit"}
	}
	var wire struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err = json.Unmarshal(body, &wire); err != nil || len(wire.Data) > MaxCatalogModels {
		return nil, &Error{Code: "catalog_invalid"}
	}
	ids := make([]string, 0, len(wire.Data))
	seen := map[string]bool{}
	for _, entry := range wire.Data {
		if ValidModelID(entry.ID) && !seen[entry.ID] {
			ids = append(ids, entry.ID)
			seen[entry.ID] = true
		}
	}
	if len(ids) == 0 {
		return nil, &Error{Code: "catalog_empty"}
	}
	sort.Strings(ids)
	return ids, nil
}
