package rules

import (
	"sort"

	"sentinel/internal/rules/match"
)

// SignalSpec is the only source of weights, mandatory policy and signal families.
type SignalSpec struct {
	Code          string `json:"code"`
	DefaultWeight int    `json:"default_weight"`
	Mandatory     bool   `json:"mandatory_ai"`
	Family        string `json:"family"`
	LegacyWeight  bool   `json:"legacy_weight"`
}

type MatcherSpec struct {
	Default MatcherConfig `json:"default"`
	Modes   []string      `json:"modes"`
}

type ThresholdSpec struct {
	Default int `json:"default"`
	Min     int `json:"min"`
	Max     int `json:"max"`
}

// Spec describes a built-in module. Catalog returns independent copies; there is
// no mutable registry and no runtime registration or plugin mechanism.
type Spec struct {
	ID             string                   `json:"id"`
	Version        string                   `json:"version"`
	Required       bool                     `json:"required"`
	DefaultEnabled bool                     `json:"default_enabled"`
	Signals        []SignalSpec             `json:"signals"`
	Matchers       map[string]MatcherSpec   `json:"matchers"`
	Thresholds     map[string]ThresholdSpec `json:"thresholds"`
	build          func(buildContext) []stage
}

func literal(terms ...string) MatcherSpec {
	return MatcherSpec{Default: MatcherConfig{Mode: match.LegacySubstring, Terms: terms}, Modes: []string{match.LegacySubstring, match.Token, match.Phrase, match.Exact}}
}

func signal(code string, weight int, mandatory bool, family string) SignalSpec {
	return SignalSpec{Code: code, DefaultWeight: weight, Mandatory: mandatory, Family: family, LegacyWeight: true}
}

// Catalog centralizes metadata, defaults, settings and module construction.
func Catalog() []Spec {
	return []Spec{
		{ID: "observation", Version: "v1", Required: true, DefaultEnabled: true,
			Signals: []SignalSpec{signal("FIRST_OBSERVED", 12, true, "observation"), {Code: "ANALYSIS_MODULE_ERROR", Mandatory: true, Family: "observation"}}, build: buildObservation},
		{ID: "links", Version: "v1", Required: true, DefaultEnabled: true,
			Signals: []SignalSpec{
				signal("UNKNOWN_EXTERNAL_DOMAIN", 22, true, "links"), signal("TELEGRAM_BOT_LINK", 25, true, "links"), signal("TELEGRAM_LINK_UNVERIFIED", 0, true, "links"),
				signal("REFERRAL_LINK", 22, false, "links"), signal("SHORTENER", 10, false, "links"), signal("DOMAIN_IMPERSONATION", 25, false, "links"), signal("WATCH_DOMAIN", 10, false, "links"), signal("DENIED_DOMAIN", 35, false, "links"),
				{Code: "URL_SCAN_LIMIT", DefaultWeight: 0, Mandatory: true, Family: "links"},
			}, Thresholds: map[string]ThresholdSpec{"url_scan_limit": {Default: 32, Min: 1, Max: 32}}, build: buildLinks},
		{ID: "commerce", Version: "v1", Required: true, DefaultEnabled: true,
			Signals: []SignalSpec{signal("COMPETITOR_PRICE_OR_ACCUSATION", 0, true, "commerce"), signal("EXTERNAL_PURCHASE_CTA", 22, false, "commerce"), signal("ALTERNATIVE_API_CHEAPER", 15, false, "commerce")},
			Matchers: map[string]MatcherSpec{
				"promotion": literal("купите", "покупайте", "переходи", "заходи", "пишите в лс", "пиши в лс", "мой api", "наш api", "мой сервис", "наш сервис", "buy now", "sign up", "dm me", "use my"),
				"criticism": literal("дешев", "дорого", "скам", "обман", "тариф", "китайский api", "cheaper", "scam", "fraud", "rip-off", "купите у", "купить у", "покупайте у", "buy from"),
			}, build: buildCommerce},
		{ID: "repetition", Version: "v1", DefaultEnabled: true,
			Signals:    []SignalSpec{signal("REPEATED_PROMO", 20, false, "repetition"), signal("MASS_IDENTICAL_LINKS", 18, false, "repetition")},
			Thresholds: map[string]ThresholdSpec{"recent_similar": {Default: 2, Min: 1, Max: 1000000}, "recent_links": {Default: 3, Min: 1, Max: 1000000}}, build: buildRepetition},
		{ID: "credentials", Version: "v1", DefaultEnabled: true,
			Signals:  []SignalSpec{signal("CREDENTIAL_SOLICITATION", 35, false, "credentials")},
			Matchers: map[string]MatcherSpec{"request": literal("введите", "пришлите", "отправьте", "enter your", "send your"), "secret": literal("api key", "api-key", "api_key", "токен", "пароль", "password", "token")}, build: buildCredentials},
		{ID: "referral_cues", Version: "v1", DefaultEnabled: false,
			Signals: []SignalSpec{{Code: "REFERRAL_REVIEW", DefaultWeight: 0, Mandatory: true, Family: "referral_cues"}},
			Matchers: map[string]MatcherSpec{
				"query_keys":     {Default: MatcherConfig{Mode: match.Exact, Terms: []string{"ref", "referral", "affiliate"}}, Modes: []string{match.Exact}},
				"bot_components": {Default: MatcherConfig{Mode: match.Token, Terms: []string{"ref", "referral"}}, Modes: []string{match.Token, match.Exact}},
			}, build: buildReferral},
	}
}

// DefaultWeights includes all known signals. DefaultConfig deliberately retains
// the legacy map without the formerly unknown URL_SCAN_LIMIT or new policies.
func DefaultWeights() map[string]int {
	weights := map[string]int{}
	for _, spec := range Catalog() {
		for _, signal := range spec.Signals {
			weights[signal.Code] = signal.DefaultWeight
		}
	}
	return weights
}

func AllowedCodes() []string {
	codes := []string{}
	for _, spec := range Catalog() {
		for _, signal := range spec.Signals {
			codes = append(codes, signal.Code)
		}
	}
	sort.Strings(codes)
	return codes
}

func ModuleIDs() []string {
	ids := []string{}
	for _, spec := range Catalog() {
		ids = append(ids, spec.ID)
	}
	return ids
}
