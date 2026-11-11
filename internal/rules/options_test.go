package rules

import "testing"

func TestMergeOptionsKeepsInheritanceAndOwnership(t *testing.T) {
	enabled := true
	base := Options{Modules: map[string]ModuleConfig{"referral_cues": {Enabled: &enabled, Matchers: map[string]MatcherConfig{"query_keys": {Mode: "exact", Terms: []string{"ref"}}}}}, FamilyCaps: map[string]int{"repetition": 25}}
	override := Options{Modules: map[string]ModuleConfig{"referral_cues": {Matchers: map[string]MatcherConfig{"query_keys": {Terms: []string{"partner"}}}}}}
	merged := MergeOptions(base, override)
	if merged.Modules["referral_cues"].Matchers["query_keys"].Mode != "exact" || merged.FamilyCaps["repetition"] != 25 {
		t.Fatal("partial override lost inherited values")
	}
	merged.Modules["referral_cues"].Matchers["query_keys"].Terms[0] = "changed"
	*merged.Modules["referral_cues"].Enabled = false
	if !enabled || override.Modules["referral_cues"].Matchers["query_keys"].Terms[0] != "partner" {
		t.Fatal("merged policy shares mutable inputs")
	}
}
