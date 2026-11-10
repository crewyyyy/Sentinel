package rules_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"

	"sentinel/internal/domain"
	"sentinel/internal/rules"
)

func enabled(value bool) *bool { return &value }
func compile(t *testing.T, options rules.Options) *rules.Analyzer {
	t.Helper()
	a, err := rules.NewAnalyzer(rules.DefaultConfig(), options)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func signalByCode(a domain.Assessment, code string) (domain.Signal, bool) {
	for _, signal := range a.Signals {
		if signal.Code == code {
			return signal, true
		}
	}
	return domain.Signal{}, false
}
func assertCode(t *testing.T, a domain.Assessment, code string, want bool) {
	t.Helper()
	_, got := signalByCode(a, code)
	if got != want {
		t.Fatalf("signal %s presence=%v, want %v: %+v", code, got, want, a)
	}
}

func TestOptionsStrictValidation(t *testing.T) {
	invalid := []rules.Options{
		{Modules: map[string]rules.ModuleConfig{"unknown": {}}},
		{Modules: map[string]rules.ModuleConfig{"commerce": {Matchers: map[string]rules.MatcherConfig{"typo": {Terms: []string{"ref"}}}}}},
		{Modules: map[string]rules.ModuleConfig{"credentials": {Thresholds: map[string]int{"recent_links": 2}}}},
		{Modules: map[string]rules.ModuleConfig{"commerce": {Matchers: map[string]rules.MatcherConfig{"promotion": {Mode: "regex", Terms: []string{".*"}}}}}},
		{Modules: map[string]rules.ModuleConfig{"commerce": {Matchers: map[string]rules.MatcherConfig{"criticism": {Terms: []string{}}}}}},
		{Modules: map[string]rules.ModuleConfig{"commerce": {Matchers: map[string]rules.MatcherConfig{"promotion": {Terms: []string{strings.Repeat("x", 257)}}}}}},
		{Modules: map[string]rules.ModuleConfig{"referral_cues": {Matchers: map[string]rules.MatcherConfig{"query_keys": {Mode: "legacy_substring"}}}}},
		{Modules: map[string]rules.ModuleConfig{"referral_cues": {Matchers: map[string]rules.MatcherConfig{"bot_components": {Mode: "phrase", Terms: []string{"ref"}}}}}},
		{Modules: map[string]rules.ModuleConfig{"links": {Thresholds: map[string]int{"url_scan_limit": 0}}}},
		{Modules: map[string]rules.ModuleConfig{"links": {Thresholds: map[string]int{"url_scan_limit": 33}}}},
		{Modules: map[string]rules.ModuleConfig{"repetition": {Thresholds: map[string]int{"recent_similar": 0}}}},
		{Modules: map[string]rules.ModuleConfig{"repetition": {Thresholds: map[string]int{"recent_links": 1000001}}}},
		{FamilyCaps: map[string]int{"unknown": 20}}, {FamilyCaps: map[string]int{"links": -1}}, {FamilyCaps: map[string]int{"links": 101}},
	}
	for i, options := range invalid {
		if _, err := rules.NewAnalyzer(rules.DefaultConfig(), options); err == nil {
			t.Fatalf("invalid case %d accepted", i)
		}
		if err := rules.ValidateOptions(options); err == nil {
			t.Fatalf("validator accepted invalid case %d", i)
		}
	}
	for _, id := range []string{"observation", "links", "commerce"} {
		if _, err := rules.NewAnalyzer(rules.DefaultConfig(), rules.Options{Modules: map[string]rules.ModuleConfig{id: {Enabled: enabled(false)}}}); err == nil {
			t.Fatalf("disabled required module %s", id)
		}
	}
}

func TestCatalogDerivedDefaultsAndIsolation(t *testing.T) {
	catalog := rules.Catalog()
	codes := map[string]bool{}
	weights := rules.DefaultWeights()
	legacy := rules.DefaultConfig()
	if legacy.Version != "v1" || len(legacy.Weights) != 15 {
		t.Fatal(legacy)
	}
	for _, spec := range catalog {
		for _, signal := range spec.Signals {
			if codes[signal.Code] {
				t.Fatalf("duplicate code %s", signal.Code)
			}
			codes[signal.Code] = true
			if weights[signal.Code] != signal.DefaultWeight {
				t.Fatal(signal)
			}
			if signal.LegacyWeight && legacy.Weights[signal.Code] != signal.DefaultWeight {
				t.Fatal(signal)
			}
		}
	}
	if len(codes) != len(rules.AllowedCodes()) || len(catalog) != len(rules.ModuleIDs()) {
		t.Fatal("catalog helpers drifted")
	}
	if _, present := legacy.Weights["URL_SCAN_LIMIT"]; present {
		t.Fatal("legacy URL_SCAN_LIMIT became explicit")
	}
	if _, present := legacy.Weights["REFERRAL_REVIEW"]; present {
		t.Fatal("legacy referral policy changed")
	}
	catalog[0].Signals[0].DefaultWeight = 100
	catalog[2].Matchers["promotion"].Default.Terms[0] = "changed"
	weights["FIRST_OBSERVED"] = 100
	if rules.DefaultWeights()["FIRST_OBSERVED"] != 12 || rules.Catalog()[2].Matchers["promotion"].Default.Terms[0] != "купите" {
		t.Fatal("mutable shared catalog")
	}
}

func TestMandatoryPolicyIgnoresZeroWeightsTrustAndCaps(t *testing.T) {
	id := int64(42)
	c := rules.DefaultConfig()
	c.AIThreshold = 101
	for _, code := range rules.AllowedCodes() {
		c.Weights[code] = 0
	}
	a, err := rules.NewAnalyzer(c, rules.Options{Modules: map[string]rules.ModuleConfig{"referral_cues": {Enabled: enabled(true)}}, FamilyCaps: map[string]int{"observation": 0, "links": 0, "commerce": 0, "referral_cues": 0}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		m     domain.Message
		urls  []domain.URL
		first bool
		code  string
	}{
		{m: domain.Message{UserID: &id}, first: true, code: "FIRST_OBSERVED"},
		{urls: []domain.URL{{Unknown: true, Domain: "unknown.example"}}, code: "UNKNOWN_EXTERNAL_DOMAIN"},
		{urls: []domain.URL{{Bot: true}}, code: "TELEGRAM_BOT_LINK"},
		{m: domain.Message{Text: "У вас скам"}, code: "COMPETITOR_PRICE_OR_ACCUSATION"},
		{urls: []domain.URL{{Telegram: true}}, code: "TELEGRAM_LINK_UNVERIFIED"},
		{urls: make([]domain.URL, 32), code: "URL_SCAN_LIMIT"},
		{urls: []domain.URL{{QueryKeys: []string{"ref"}}}, code: "REFERRAL_REVIEW"},
	} {
		got := a.Assess(tc.m, tc.urls, tc.first, 100)
		assertCode(t, got, tc.code, true)
		if got.Score != 0 || !got.Mandatory || !got.ReviewRequired {
			t.Fatalf("mandatory suppressed: %+v", got)
		}
	}
}

func TestOptionalModulesAndIsolatedOverrides(t *testing.T) {
	a := compile(t, rules.Options{Modules: map[string]rules.ModuleConfig{
		"credentials": {Enabled: enabled(false)}, "repetition": {Enabled: enabled(false)},
	}})
	m := domain.Message{Text: "Купите дешевле, введите токен", RecentSimilar: 10, RecentLinks: 10}
	got := a.Assess(m, nil, false, 0)
	assertCode(t, got, "CREDENTIAL_SOLICITATION", false)
	assertCode(t, got, "REPEATED_PROMO", false)
	assertCode(t, got, "EXTERNAL_PURCHASE_CTA", true)
	assertCode(t, got, "COMPETITOR_PRICE_OR_ACCUSATION", true)

	a = compile(t, rules.Options{Modules: map[string]rules.ModuleConfig{
		"commerce":   {Matchers: map[string]rules.MatcherConfig{"promotion": {Mode: "phrase", Terms: []string{"try our"}}}},
		"repetition": {Thresholds: map[string]int{"recent_similar": 4, "recent_links": 5}},
		"links":      {Thresholds: map[string]int{"url_scan_limit": 2}},
	}})
	got = a.Assess(m, nil, false, 0)
	assertCode(t, got, "EXTERNAL_PURCHASE_CTA", false)
	assertCode(t, got, "COMPETITOR_PRICE_OR_ACCUSATION", true)
	assertCode(t, got, "CREDENTIAL_SOLICITATION", true)
	m.Text = "try our cheaper API"
	m.RecentSimilar = 3
	m.RecentLinks = 4
	got = a.Assess(m, nil, false, 0)
	assertCode(t, got, "EXTERNAL_PURCHASE_CTA", true)
	assertCode(t, got, "ALTERNATIVE_API_CHEAPER", true)
	assertCode(t, got, "REPEATED_PROMO", false)
	assertCode(t, got, "MASS_IDENTICAL_LINKS", false)
	m.RecentSimilar = 4
	m.RecentLinks = 5
	got = a.Assess(m, make([]domain.URL, 2), false, 0)
	assertCode(t, got, "REPEATED_PROMO", true)
	assertCode(t, got, "MASS_IDENTICAL_LINKS", true)
	assertCode(t, got, "URL_SCAN_LIMIT", true)
	assertCode(t, compile(t, rules.Options{}).Assess(domain.Message{Text: "try our API"}, nil, false, 0), "EXTERNAL_PURCHASE_CTA", false)
}

func TestFamilyCapsOnlyAffectScore(t *testing.T) {
	id := int64(42)
	urls := []domain.URL{{Unknown: true, Domain: "unknown.example", Referral: true, Value: "https://unknown.example/?ref=x"}}
	m := domain.Message{UserID: &id, Text: "Купите дешевле"}
	baseline := compile(t, rules.Options{}).Assess(m, urls, true, 0)
	got := compile(t, rules.Options{FamilyCaps: map[string]int{"links": 5, "commerce": 10}}).Assess(m, urls, true, 0)
	if got.Score != 27 || !got.Mandatory || !got.ReviewRequired {
		t.Fatal(got)
	}
	if !reflect.DeepEqual(got.Signals, baseline.Signals) {
		t.Fatal("cap hid configured weights or evidence")
	}
	got = compile(t, rules.Options{FamilyCaps: map[string]int{"links": 0}}).Assess(domain.Message{}, urls, false, 0)
	if got.Score != 0 || !got.Mandatory {
		t.Fatal(got)
	}
}

func TestAnalyzerOwnsSettingsAndSnapshot(t *testing.T) {
	c := rules.DefaultConfig()
	c.Thresholds = []int{25, 45, 70, 85}
	flag := true
	terms := []string{"special offer"}
	options := rules.Options{Modules: map[string]rules.ModuleConfig{"commerce": {Matchers: map[string]rules.MatcherConfig{"promotion": {Mode: "phrase", Terms: terms}}}, "referral_cues": {Enabled: &flag}}, FamilyCaps: map[string]int{"commerce": 22}}
	a, err := rules.NewAnalyzer(c, options)
	if err != nil {
		t.Fatal(err)
	}
	m := domain.Message{Text: "special offer"}
	before := a.Assess(m, nil, false, 0)
	baseline, err := json.Marshal(a.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	terms[0] = "mutated"
	flag = false
	c.Weights["EXTERNAL_PURCHASE_CTA"] = 100
	c.Thresholds[0] = 0
	options.FamilyCaps["commerce"] = 0
	module := options.Modules["commerce"]
	module.Matchers["promotion"] = rules.MatcherConfig{Terms: []string{"mutated"}}
	if got := a.Assess(m, nil, false, 0); !reflect.DeepEqual(got, before) {
		t.Fatal("caller mutated analyzer")
	}
	snapshot := a.Snapshot().(rules.ResolvedSnapshot)
	if snapshot.FeaturesVersion != domain.CurrentFeaturesVersion || snapshot.NormalizationVersion == "" || snapshot.EngineVersion == "" {
		t.Fatal("snapshot omits extractor or implementation versions")
	}
	snapshot.Config.Weights["EXTERNAL_PURCHASE_CTA"] = 100
	snapshot.Config.Thresholds[0] = 0
	snapshot.Options.Modules["commerce"].Matchers["promotion"].Terms[0] = "mutated"
	*snapshot.Options.Modules["referral_cues"].Enabled = false
	snapshot.Options.FamilyCaps["commerce"] = 0
	snapshot.Catalog[0].Signals[0].DefaultWeight = 100
	later, err := json.Marshal(a.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if string(later) != string(baseline) {
		t.Fatal("snapshot exposes internal state")
	}
	if got := a.Assess(m, nil, false, 0); !reflect.DeepEqual(got, before) {
		t.Fatal("snapshot mutated analyzer")
	}
}

func TestDeterministicOrderSnapshotAndConcurrentReuse(t *testing.T) {
	forward := rules.Options{Modules: map[string]rules.ModuleConfig{}, FamilyCaps: map[string]int{}}
	reverse := rules.Options{Modules: map[string]rules.ModuleConfig{}, FamilyCaps: map[string]int{}}
	ids := rules.ModuleIDs()
	for _, id := range ids {
		forward.Modules[id] = rules.ModuleConfig{}
	}
	for i := len(ids) - 1; i >= 0; i-- {
		reverse.Modules[ids[i]] = rules.ModuleConfig{}
	}
	forward.FamilyCaps["links"] = 70
	forward.FamilyCaps["commerce"] = 30
	reverse.FamilyCaps["commerce"] = 30
	reverse.FamilyCaps["links"] = 70
	a, b := compile(t, forward), compile(t, reverse)
	left, err := json.Marshal(a.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	right, err := json.Marshal(b.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if string(left) != string(right) {
		t.Fatal("snapshot depends on map insertion order")
	}
	m := domain.Message{Text: "Купите дешевле, пришлите токен", RecentSimilar: 2, RecentLinks: 3}
	urls := []domain.URL{{Watch: true, Domain: "watch.example"}, {Unknown: true, Referral: true, Domain: "unknown.example"}}
	original, err := json.Marshal(struct {
		Message domain.Message
		URLs    []domain.URL
	}{m, urls})
	if err != nil {
		t.Fatal(err)
	}
	want := a.Assess(m, urls, false, 31)
	if !reflect.DeepEqual(want, b.Assess(m, urls, false, 31)) {
		t.Fatal("module map changed signal order")
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				if got := a.Assess(m, urls, false, 31); !reflect.DeepEqual(got, want) {
					t.Errorf("concurrent result changed")
				}
				if _, err := json.Marshal(a.Snapshot()); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	later, err := json.Marshal(struct {
		Message domain.Message
		URLs    []domain.URL
	}{m, urls})
	if err != nil {
		t.Fatal(err)
	}
	if string(original) != string(later) {
		t.Fatal("assessment mutated input")
	}
}
