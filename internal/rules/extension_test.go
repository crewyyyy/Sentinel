package rules

import (
	"testing"

	"sentinel/internal/domain"
)

func TestUnknownModuleSignalFailsClosed(t *testing.T) {
	analyzer, err := NewAnalyzer(DefaultConfig(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	analyzer.stages = append(analyzer.stages, stage{order: 100, detector: detectorFunc(func(input) []Finding {
		return []Finding{{Code: "MISSPELLED_NEW_SIGNAL"}, {Code: "ANOTHER_UNREGISTERED_SIGNAL"}}
	})})
	a := analyzer.Assess(domain.Message{}, nil, false, 100)
	if !a.Mandatory || !a.ReviewRequired || len(a.Signals) != 1 || a.Signals[0].Code != "ANALYSIS_MODULE_ERROR" {
		t.Fatal("new module silently bypassed central signal catalog", a)
	}
}
