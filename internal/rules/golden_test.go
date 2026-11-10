package rules_test

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"sentinel/internal/domain"
	"sentinel/internal/rules"
)

const goldenPath = "../../testdata/rules_v1_golden.json"

type goldenConfig struct {
	Version          string         `json:"version"`
	AIThreshold      int            `json:"ai_threshold"`
	MaxTrustDiscount int            `json:"max_trust_discount"`
	Weights          map[string]int `json:"weights"`
	Thresholds       []int          `json:"thresholds"`
}

func (c goldenConfig) rulesConfig() rules.Config {
	return rules.Config{Version: c.Version, AIThreshold: c.AIThreshold, MaxTrustDiscount: c.MaxTrustDiscount, Weights: c.Weights, Thresholds: c.Thresholds}
}

type goldenCase struct {
	Name       string            `json:"name"`
	Message    domain.Message    `json:"message"`
	URLs       []domain.URL      `json:"urls"`
	First      bool              `json:"first"`
	Trust      int               `json:"trust"`
	Config     goldenConfig      `json:"config"`
	Assessment domain.Assessment `json:"assessment"`
}

// These complete inputs and outputs were frozen using the original inline v1
// Assess, before introducing Analyzer. Do not regenerate them from the refactor.
func TestLegacyGolden(t *testing.T) {
	data, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	var cases []goldenCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) != 101 {
		t.Fatal("legacy baseline matrix must retain all 101 frozen cases")
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			got := rules.Assess(tc.Message, tc.URLs, tc.First, tc.Trust, tc.Config.rulesConfig())
			if !reflect.DeepEqual(got, tc.Assessment) {
				t.Fatalf("legacy assessment changed\ngot:  %+v\nwant: %+v", got, tc.Assessment)
			}
			analyzer, err := rules.NewAnalyzer(tc.Config.rulesConfig(), rules.Options{})
			if err != nil {
				t.Fatal(err)
			}
			if got := analyzer.Assess(tc.Message, tc.URLs, tc.First, tc.Trust); !reflect.DeepEqual(got, tc.Assessment) {
				t.Fatalf("analyzer differs from frozen legacy assessment\ngot:  %+v\nwant: %+v", got, tc.Assessment)
			}
		})
	}
}
