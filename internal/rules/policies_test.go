package rules_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
	"unicode"

	"sentinel/internal/domain"
	"sentinel/internal/rules"
	"sentinel/internal/rules/match"
)

type policyCase struct{ id, code, text, bare string }

func policyCases() []policyCase {
	return []policyCase{
		{"sales", "SALES_REVIEW", "продам доступ к API", "продам доступ"},
		{"distribution", "DISTRIBUTION_REVIEW", "раздаю инвайты, пишите", "раздаю инвайты"},
		{"services", "SERVICES_REVIEW", "настрою сервер за оплату", "настрою сервер"},
		{"recruitment", "RECRUITMENT_REVIEW", "ищу клиентов, пишите мне", "ищу клиентов"},
	}
}

func policyOptions(sensitivity, policy int) rules.Options {
	options := rules.Options{Modules: map[string]rules.ModuleConfig{}}
	for _, tc := range policyCases() {
		options.Modules[tc.id] = rules.ModuleConfig{Enabled: enabled(true), Thresholds: map[string]int{"sensitivity": sensitivity, "policy": policy}}
	}
	return options
}

func assertNoPolicySignals(t *testing.T, assessment domain.Assessment) {
	t.Helper()
	for _, tc := range policyCases() {
		assertCode(t, assessment, tc.code, false)
	}
}

func TestPolicyCatalogDescriptorsAndGenericControls(t *testing.T) {
	catalog := rules.Catalog()
	for _, spec := range catalog {
		if !strings.ContainsFunc(spec.Title, func(r rune) bool { return unicode.In(r, unicode.Cyrillic) }) || !strings.ContainsFunc(spec.Description, func(r rune) bool { return unicode.In(r, unicode.Cyrillic) }) {
			t.Fatalf("missing Russian UI metadata for %s", spec.ID)
		}
		data, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		var metadata map[string]any
		if err := json.Unmarshal(data, &metadata); err != nil {
			t.Fatal(err)
		}
		if metadata["title"] != spec.Title || metadata["description"] != spec.Description {
			t.Fatalf("UI metadata absent from JSON for %s", spec.ID)
		}
	}
	for _, tc := range policyCases() {
		var spec rules.Spec
		for _, candidate := range catalog {
			if candidate.ID == tc.id {
				spec = candidate
			}
		}
		if spec.ID == "" || spec.Required || spec.DefaultEnabled || spec.Version == "" || len(spec.Signals) != 1 {
			t.Fatalf("invalid optional module descriptor: %+v", spec)
		}
		signal := spec.Signals[0]
		if signal.Code != tc.code || signal.DefaultWeight != 0 || !signal.Mandatory || signal.LegacyWeight || signal.Family != tc.id {
			t.Fatal(signal)
		}
		wantThresholds := map[string]rules.ThresholdSpec{
			"match_window_tokens": {Default: 12, Min: 2, Max: 64},
			"sensitivity":         {Default: 2, Min: 1, Max: 3},
			"policy":              {Default: 0, Min: 0, Max: 1},
		}
		if !reflect.DeepEqual(spec.Thresholds, wantThresholds) {
			t.Fatal(tc.id, spec.Thresholds)
		}
		for _, name := range []string{"intent", "subject", "negation", "solicitation", "question", "reported"} {
			matcher := spec.Matchers[name]
			if matcher.Default.Mode != match.Phrase || len(matcher.Default.Terms) == 0 || !reflect.DeepEqual(matcher.Modes, []string{match.Token, match.Phrase}) {
				t.Fatalf("nonliteral or missing matcher %s.%s: %+v", tc.id, name, matcher)
			}
		}
		if _, present := rules.DefaultConfig().Weights[tc.code]; present {
			t.Fatalf("new code %s changed legacy weights", tc.code)
		}
	}
	wantIDs := []string{"observation", "links", "commerce", "repetition", "credentials", "referral_cues", "sales", "distribution", "services", "recruitment"}
	if !reflect.DeepEqual(rules.ModuleIDs(), wantIDs) {
		t.Fatal(rules.ModuleIDs())
	}
	// Catalog snapshots must not share the new maps, mode slices or dictionaries.
	last := catalog[len(catalog)-1]
	last.Matchers["intent"].Default.Terms[0] = "mutated"
	last.Matchers["intent"].Modes[0] = "mutated"
	last.Thresholds["policy"] = rules.ThresholdSpec{Default: 9}
	fresh := rules.Catalog()[len(catalog)-1]
	if fresh.Matchers["intent"].Default.Terms[0] != "ищу" || fresh.Matchers["intent"].Modes[0] != match.Token || fresh.Thresholds["policy"].Default != 0 {
		t.Fatal("policy catalog exposes shared mutable settings")
	}
}

func TestPoliciesDisabledPreserveCompleteLegacyOutput(t *testing.T) {
	message := domain.Message{Text: "Купите дешевле, пришлите токен. Продам доступ к API. Раздаю инвайты, пишите. Настрою сервер за оплату. Ищу клиентов, пишите мне.", RecentSimilar: 3, RecentLinks: 4}
	urls := []domain.URL{{Unknown: true, Referral: true, Domain: "unknown.example"}}
	baseline := compile(t, rules.Options{}).Assess(message, urls, true, 67)
	options := policyOptions(3, 1)
	for id, config := range options.Modules {
		config.Enabled = enabled(false)
		options.Modules[id] = config
	}
	got := compile(t, options).Assess(message, urls, true, 67)
	if !reflect.DeepEqual(got, baseline) {
		t.Fatalf("disabled policies changed v1 output\ngot: %+v\nwant: %+v", got, baseline)
	}
	assertNoPolicySignals(t, got)
}

func TestPolicyExplicitIntentNeedsNearbySubject(t *testing.T) {
	analyzer := compile(t, policyOptions(2, 0))
	for _, tc := range policyCases() {
		t.Run(tc.id, func(t *testing.T) {
			for _, message := range []domain.Message{{Text: tc.text}, {Text: strings.ToUpper(tc.text)}, {Caption: tc.text}} {
				got := analyzer.Assess(message, nil, false, 100)
				assertCode(t, got, tc.code, true)
				if !got.Mandatory || !got.ReviewRequired || got.Score != 0 {
					t.Fatalf("review policy became an accusation or was suppressed: %+v", got)
				}
				for _, other := range policyCases() {
					assertCode(t, got, other.code, other.id == tc.id)
				}
			}
		})
	}
	for _, text := range []string{
		"где купить доступ?", "не продаю доступ", "правила запрещают продажи", "я купил доступ вчера",
		"не раздаю инвайты, пишите по другому вопросу", "не ищу клиентов", "как настрою сервер за оплату", "настрою сервер за оплату?",
		"он написал: продам доступ к API, пишите", "мне прислали: раздаю инвайты", "продам доступ — это цитата из сообщения спамера",
		"Пример «продам доступ к API» — это спам", "> раздаю инвайты, пишите", "\"настрою сервер за оплату\"",
		"доступ api сервер инвайты клиенты", "продам раздаю настрою ищу", "я делюсь опытом настройки сервера",
		"перепродам доступ", "продам недоступный", "продам велосипед. доступ к API работает", "продам, доступ к API работает",
		"продам " + strings.Repeat("слово ", 12) + "доступ", "настрою " + strings.Repeat("слово ", 12) + "сервер за оплату",
	} {
		t.Run(text, func(t *testing.T) {
			assertNoPolicySignals(t, analyzer.Assess(domain.Message{Text: text}, nil, false, 0))
		})
	}
	for _, tc := range policyCases() {
		assertNoPolicySignals(t, analyzer.Assess(domain.Message{Text: "Жалоба на чужое сообщение", QuoteText: tc.text, Quoted: true}, nil, false, 0))
		assertNoPolicySignals(t, analyzer.Assess(domain.Message{Text: tc.text, Forwarded: true}, nil, false, 0))
	}
}

func TestPolicySensitivityHasConsistentReviewOnlySemantics(t *testing.T) {
	conservative, normal, strict := compile(t, policyOptions(1, 0)), compile(t, policyOptions(2, 0)), compile(t, policyOptions(3, 0))
	for _, tc := range policyCases() {
		assertCode(t, conservative.Assess(domain.Message{Text: tc.bare}, nil, false, 0), tc.code, false)
		assertCode(t, conservative.Assess(domain.Message{Text: tc.bare + ", пишите мне"}, nil, false, 0), tc.code, true)
		assertCode(t, normal.Assess(domain.Message{Text: tc.bare}, nil, false, 0), tc.code, true)
		for _, text := range []string{
			tc.bare + ". Пишите мне", tc.bare + " «пишите мне»", tc.bare + " " + strings.Repeat("слово ", 12) + "пишите мне",
		} {
			assertCode(t, conservative.Assess(domain.Message{Text: text}, nil, false, 0), tc.code, false)
		}
		for _, candidate := range []struct {
			message domain.Message
			flag    string
		}{
			{domain.Message{Text: "«" + tc.bare + "»"}, "quoted"},
			{domain.Message{Text: tc.bare, Forwarded: true}, "quoted or forwarded"},
			{domain.Message{Text: "Жалоба", QuoteText: tc.bare, Quoted: true}, "quoted"},
			{domain.Message{Text: "он написал: " + tc.bare}, "reported"},
			{domain.Message{Text: tc.bare + "?"}, "question"},
			{domain.Message{Text: "не " + tc.bare}, "negated"},
		} {
			assertCode(t, normal.Assess(candidate.message, nil, false, 0), tc.code, false)
			got := strict.Assess(candidate.message, nil, false, 0)
			signal, ok := signalByCode(got, tc.code)
			if !ok || !signal.MandatoryAI || signal.Weight != 0 || !strings.Contains(signal.Evidence, candidate.flag) || !strings.Contains(signal.Evidence, "contextual review only") || signal.Confidence >= .7 {
				t.Fatalf("context was asserted as an offer: %+v", got)
			}
		}
	}
	// Strict mode still needs both cues; a buying question is not a selling intent.
	assertNoPolicySignals(t, strict.Assess(domain.Message{Text: "где купить доступ?"}, nil, false, 0))
	for _, analyzer := range []*rules.Analyzer{conservative, normal, strict} {
		got := analyzer.Assess(domain.Message{Text: "не продаю доступ. Продам ключ, пишите мне"}, nil, false, 0)
		assertCode(t, got, "SALES_REVIEW", true)
		signal, _ := signalByCode(got, "SALES_REVIEW")
		if strings.Contains(signal.Evidence, "negated") {
			t.Fatalf("context of a different candidate leaked: %+v", signal)
		}
	}
	got := strict.Assess(domain.Message{Text: "«продам доступ». Продам ключ", QuoteText: "продам доступ", Quoted: true}, nil, false, 0)
	signal, _ := signalByCode(got, "SALES_REVIEW")
	if strings.Contains(signal.Evidence, "quoted") {
		t.Fatalf("quote suppressed an independent direct candidate: %+v", signal)
	}
	for _, analyzer := range []*rules.Analyzer{normal, strict} {
		assertNoPolicySignals(t, analyzer.Assess(domain.Message{Text: "продам", QuoteText: "доступ", Quoted: true}, nil, false, 0))
	}
}

func TestPolicyMatchersAndInclusiveWindowAreConfigurable(t *testing.T) {
	options := rules.Options{Modules: map[string]rules.ModuleConfig{"sales": {
		Enabled: enabled(true), Thresholds: map[string]int{"match_window_tokens": 4},
		Matchers: map[string]rules.MatcherConfig{
			"intent": {Terms: []string{"offer premium"}}, "subject": {Terms: []string{"neural seats"}},
			"negation": {Terms: []string{"decline"}}, "question": {Terms: []string{"would you"}},
			"reported": {Terms: []string{"moderator notes"}}, "solicitation": {Terms: []string{"contact us"}},
		},
	}}}
	analyzer := compile(t, options)
	for _, tc := range []struct {
		text string
		want bool
	}{
		{"offer premium neural seats", true}, {"neural seats offer premium", true},
		{"offer excellent premium neural seats", false}, {"offer premium new neural seats", false},
		{"decline offer premium neural seats", false}, {"продам доступ", false},
	} {
		assertCode(t, analyzer.Assess(domain.Message{Text: tc.text}, nil, false, 0), "SALES_REVIEW", tc.want)
	}
	config := options.Modules["sales"]
	config.Thresholds["match_window_tokens"] = 3
	assertCode(t, compile(t, options).Assess(domain.Message{Text: "offer premium neural seats"}, nil, false, 0), "SALES_REVIEW", false)
	config.Thresholds["match_window_tokens"] = 8
	config.Thresholds["sensitivity"] = 1
	assertCode(t, compile(t, options).Assess(domain.Message{Text: "offer premium neural seats, contact us"}, nil, false, 0), "SALES_REVIEW", true)
	assertCode(t, compile(t, options).Assess(domain.Message{Text: "offer premium neural seats, пишите мне"}, nil, false, 0), "SALES_REVIEW", false)
	config.Thresholds["sensitivity"] = 2
	config.Matchers["intent"] = rules.MatcherConfig{Mode: match.Token, Terms: []string{"vend"}}
	config.Matchers["subject"] = rules.MatcherConfig{Mode: match.Token, Terms: []string{"seat"}}
	assertCode(t, compile(t, options).Assess(domain.Message{Text: "vend seat"}, nil, false, 0), "SALES_REVIEW", true)
	assertCode(t, compile(t, options).Assess(domain.Message{Text: "vending seats"}, nil, false, 0), "SALES_REVIEW", false)
	config.Matchers["subject"] = rules.MatcherConfig{Mode: match.Token, Terms: []string{"vend"}}
	assertCode(t, compile(t, options).Assess(domain.Message{Text: "vend"}, nil, false, 0), "SALES_REVIEW", false)
}

func TestPolicyConfigRejectsInvalidControlsAndNonTokenModes(t *testing.T) {
	for _, tc := range policyCases() {
		for _, threshold := range []struct {
			name  string
			value int
		}{{"match_window_tokens", 1}, {"match_window_tokens", 65}, {"sensitivity", 0}, {"sensitivity", 4}, {"policy", -1}, {"policy", 2}} {
			options := rules.Options{Modules: map[string]rules.ModuleConfig{tc.id: {Thresholds: map[string]int{threshold.name: threshold.value}}}}
			if err := rules.ValidateOptions(options); err == nil {
				t.Fatalf("invalid control accepted: %s.%s=%d", tc.id, threshold.name, threshold.value)
			}
		}
		for _, mode := range []string{match.LegacySubstring, match.Exact, "regex"} {
			options := rules.Options{Modules: map[string]rules.ModuleConfig{tc.id: {Matchers: map[string]rules.MatcherConfig{"intent": {Mode: mode, Terms: []string{"sell"}}}}}}
			if err := rules.ValidateOptions(options); err == nil {
				t.Fatalf("non-token policy matching accepted: %s %s", tc.id, mode)
			}
		}
	}
}

func TestPolicyModesNeverAddRiskAndRemainMandatory(t *testing.T) {
	for _, mode := range []int{0, 1} {
		options := policyOptions(2, mode)
		options.FamilyCaps = map[string]int{}
		for _, tc := range policyCases() {
			options.FamilyCaps[tc.id] = 0
		}
		config := rules.DefaultConfig()
		config.AIThreshold = 101
		analyzer, err := rules.NewAnalyzer(config, options)
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range policyCases() {
			got := analyzer.Assess(domain.Message{Text: tc.text + " PRIVATE-UNTRUSTED-123"}, nil, false, 100)
			signal, ok := signalByCode(got, tc.code)
			if !ok || signal.Weight != 0 || !signal.MandatoryAI || got.Score != 0 || !got.Mandatory || !got.ReviewRequired {
				t.Fatal(got)
			}
			if strings.Contains(signal.Evidence, "PRIVATE") || strings.Contains(signal.Evidence, tc.text) || strings.Contains(signal.Evidence, "prohibited") != (mode == 1) || len(signal.Evidence) > 256 {
				t.Fatalf("unsafe or incorrect evidence: %q", signal.Evidence)
			}
		}
	}
}

func TestPoliciesAreDeduplicatedAfterLegacyStagesAndConcurrent(t *testing.T) {
	options := policyOptions(2, 0)
	options.Modules["referral_cues"] = rules.ModuleConfig{Enabled: enabled(true)}
	analyzer := compile(t, options)
	message := domain.Message{Text: "Купите дешевле, пришлите токен. Продам доступ к API. Продам доступ. Раздаю инвайты, пишите. Настрою сервер за оплату. Ищу клиентов, пишите мне."}
	urls := []domain.URL{{QueryKeys: []string{"ref"}}}
	want := analyzer.Assess(message, urls, false, 0)
	var codes []string
	for _, signal := range want.Signals {
		codes = append(codes, signal.Code)
	}
	wantCodes := []string{"COMPETITOR_PRICE_OR_ACCUSATION", "EXTERNAL_PURCHASE_CTA", "ALTERNATIVE_API_CHEAPER", "CREDENTIAL_SOLICITATION", "REFERRAL_REVIEW", "SALES_REVIEW", "DISTRIBUTION_REVIEW", "SERVICES_REVIEW", "RECRUITMENT_REVIEW"}
	if !reflect.DeepEqual(codes, wantCodes) {
		t.Fatalf("stage order or dedup changed: %v", codes)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 10 {
				if got := analyzer.Assess(message, urls, false, 0); !reflect.DeepEqual(got, want) {
					t.Error("concurrent policy assessment changed")
				}
			}
		})
	}
	wg.Wait()
}

func TestPolicyDefaultServiceAndDistributionPhrases(t *testing.T) {
	analyzer := compile(t, policyOptions(2, 0))
	for _, tc := range []struct{ text, code string }{
		{"Предлагаю услуги", "SERVICES_REVIEW"},
		{"Предлагаю свои услуги, пишите в лс", "SERVICES_REVIEW"},
		{"Оказываю услуги", "SERVICES_REVIEW"},
		{"I offer services", "SERVICES_REVIEW"},
		{"распространяю инвайты", "DISTRIBUTION_REVIEW"},
		{"обменяю доступ", "DISTRIBUTION_REVIEW"},
	} {
		assertCode(t, analyzer.Assess(domain.Message{Text: tc.text}, nil, false, 0), tc.code, true)
	}
	for _, text := range []string{"услуги", "предлагаю", "services", "распространяю", "обменяю", "не предлагаю услуги", "кто предлагает услуги?", "он написал: предлагаю услуги", "«обменяю доступ»"} {
		assertNoPolicySignals(t, analyzer.Assess(domain.Message{Text: text}, nil, false, 0))
	}
}

func TestPolicyStructuredQuotesRequireFreshUnmodifiedSource(t *testing.T) {
	normal, strict := compile(t, policyOptions(2, 0)), compile(t, policyOptions(3, 0))
	for _, tc := range policyCases() {
		for _, kind := range []string{"blockquote", "expandable_blockquote", "code", "pre"} {
			entity := domain.Entity{Type: kind, Offset: 3, Length: len([]rune(tc.text))}
			message := domain.Message{Text: "🙂 " + tc.text + " конец", Entities: []domain.Entity{entity}, FeaturesVersion: domain.CurrentFeaturesVersion}
			assertNoPolicySignals(t, normal.Assess(message, nil, false, 0))
			got := strict.Assess(message, nil, false, 0)
			signal, ok := signalByCode(got, tc.code)
			if !ok || !strings.Contains(signal.Evidence, "quoted") {
				t.Fatalf("structured quote lost attribution: %+v", got)
			}
			message.Caption, message.CaptionEntities = message.Text, message.Entities
			message.Text, message.Entities = "", nil
			assertNoPolicySignals(t, normal.Assess(message, nil, false, 0))
			assertCode(t, strict.Assess(message, nil, false, 0), tc.code, true)
		}
		quoted := tc.text + " «"
		message := domain.Message{Text: quoted + " " + tc.text, Entities: []domain.Entity{{Type: "blockquote", Offset: 0, Length: len([]rune(quoted))}}, FeaturesVersion: domain.CurrentFeaturesVersion}
		got := normal.Assess(message, nil, false, 0)
		assertCode(t, got, tc.code, true)
		signal, _ := signalByCode(got, tc.code)
		if strings.Contains(signal.Evidence, "quoted") {
			t.Fatalf("quoted entity changed attribution outside its boundary: %+v", signal)
		}
		for _, partial := range []domain.Message{
			{Text: tc.text, Entities: []domain.Entity{{Type: "blockquote", Length: len([]rune(tc.text))}}},
			{Text: tc.text, Entities: []domain.Entity{{Type: "blockquote", Length: len([]rune(tc.text))}}, FeaturesVersion: domain.CurrentFeaturesVersion - 1},
			{Text: "redacted", Entities: []domain.Entity{{Type: "blockquote", Offset: 900, Length: 500}}, FeaturesVersion: domain.CurrentFeaturesVersion, FeaturesPartial: true},
			{Text: "🙂 " + tc.text, Entities: []domain.Entity{{Type: "blockquote", Offset: 1, Length: 1}}, FeaturesVersion: domain.CurrentFeaturesVersion},
			{Text: "redacted", FeaturesPartial: true},
		} {
			got := normal.Assess(partial, nil, false, 0)
			signal, ok := signalByCode(got, tc.code)
			if !ok || !signal.MandatoryAI || !strings.Contains(signal.Evidence, "coverage incomplete") || strings.Contains(signal.Evidence, "quoted or forwarded") {
				t.Fatalf("stale/redacted offsets reused or partial evidence silently safe: %+v", got)
			}
		}
	}
	// A phrase cannot be assembled across an entity attribution boundary.
	message := domain.Message{Text: "продам доступ", Entities: []domain.Entity{{Type: "blockquote", Length: len([]rune("продам"))}}, FeaturesVersion: domain.CurrentFeaturesVersion}
	assertNoPolicySignals(t, strict.Assess(message, nil, false, 0))
}

func TestPolicyParsingLimitsRemainExplicitReview(t *testing.T) {
	for _, tc := range policyCases() {
		analyzer := compile(t, rules.Options{Modules: map[string]rules.ModuleConfig{tc.id: {Enabled: enabled(true)}}})
		got := analyzer.Assess(domain.Message{Text: strings.Repeat("x", match.MaxContextBytes+1)}, nil, false, 0)
		signal, ok := signalByCode(got, tc.code)
		if !ok || !signal.MandatoryAI || signal.Weight != 0 || !strings.Contains(signal.Evidence, "coverage incomplete") || !strings.Contains(signal.Evidence, "contextual review only") {
			t.Fatalf("bounded context silently treated as safe: %+v", got)
		}
	}
}
