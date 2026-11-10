// Package rules produces deterministic, versioned review reasons, not findings of guilt.
package rules

import "sentinel/internal/domain"

type Config struct {
	Version          string         `json:"version"`
	AIThreshold      int            `json:"ai_threshold"`
	MaxTrustDiscount int            `json:"max_trust_discount"`
	Weights          map[string]int `json:"weights"`
	Thresholds       []int          `json:"thresholds"`
}

// DefaultConfig preserves v1's defaults, including the absence of a weight for
// URL_SCAN_LIMIT (which has always defaulted to zero).
func DefaultConfig() Config {
	weights := map[string]int{}
	for _, spec := range Catalog() {
		for _, signal := range spec.Signals {
			if signal.LegacyWeight {
				weights[signal.Code] = signal.DefaultWeight
			}
		}
	}
	return Config{Version: "v1", AIThreshold: 45, MaxTrustDiscount: 20, Weights: weights}
}

// Assess retains the v1 convenience API. Production should construct and reuse
// an Analyzer instead of compiling default module settings for each message.
func Assess(m domain.Message, urls []domain.URL, first bool, trust int, c Config) domain.Assessment {
	// Empty options contain no untrusted configuration; built-in defaults are
	// validated by catalog tests. No caller input can make construction fail.
	analyzer, err := NewAnalyzer(c, Options{})
	if err != nil {
		// A malformed built-in catalog is a programmer error, not an input error.
		// Retain an explicit pending review rather than silently return safe.
		return domain.Assessment{Trust: max(0, min(100, trust)), URLs: urls, Version: c.Version, Band: "normal", Mandatory: true, ReviewRequired: true, Signals: []domain.Signal{}}
	}
	return analyzer.Assess(m, urls, first, trust)
}
