// Package rules produces deterministic, versioned review reasons, not findings of guilt.
package rules

import (
	"sentinel/internal/domain"
	"strings"
)

type Config struct {
	Version                       string
	AIThreshold, MaxTrustDiscount int
	Weights                       map[string]int
	Thresholds                    []int
}

func DefaultConfig() Config {
	return Config{Version: "v1", AIThreshold: 45, MaxTrustDiscount: 20, Weights: map[string]int{
		"FIRST_OBSERVED": 12, "UNKNOWN_EXTERNAL_DOMAIN": 22, "TELEGRAM_BOT_LINK": 25, "TELEGRAM_LINK_UNVERIFIED": 0, "COMPETITOR_PRICE_OR_ACCUSATION": 0, "REFERRAL_LINK": 22, "EXTERNAL_PURCHASE_CTA": 22, "ALTERNATIVE_API_CHEAPER": 15, "DOMAIN_IMPERSONATION": 25, "CREDENTIAL_SOLICITATION": 35, "WATCH_DOMAIN": 10, "DENIED_DOMAIN": 35, "SHORTENER": 10, "REPEATED_PROMO": 20, "MASS_IDENTICAL_LINKS": 18,
	}}
}
func has(s string, parts ...string) bool {
	for _, p := range parts {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}
func Assess(m domain.Message, urls []domain.URL, first bool, trust int, c Config) domain.Assessment {
	a := domain.Assessment{Trust: max(0, min(100, trust)), URLs: urls, Version: c.Version, Signals: []domain.Signal{}}
	seen := map[string]bool{}
	defaults := DefaultConfig().Weights
	add := func(code, evidence string, mandatory bool, confidence float64) {
		if seen[code] {
			return
		}
		seen[code] = true
		w, ok := c.Weights[code]
		if !ok {
			w = defaults[code]
		}
		a.Signals = append(a.Signals, domain.Signal{Code: code, Weight: w, Evidence: evidence, Confidence: confidence, MandatoryAI: mandatory, RulesetVersion: c.Version})
		a.Score += w
		a.Mandatory = a.Mandatory || mandatory
	}
	text := strings.ReplaceAll(strings.ToLower(m.Content()+" "+m.QuoteText), "ё", "е")
	promo := has(text, "купите", "покупайте", "переходи", "заходи", "пишите в лс", "пиши в лс", "мой api", "наш api", "мой сервис", "наш сервис", "buy now", "sign up", "dm me", "use my")
	criticism := has(text, "дешев", "дорого", "скам", "обман", "тариф", "китайский api", "cheaper", "scam", "fraud", "rip-off", "купите у", "купить у", "покупайте у", "buy from")
	if first && m.UserID != nil {
		add("FIRST_OBSERVED", "first observed since installation; history coverage partial", true, 1)
	}
	if criticism {
		add("COMPETITOR_PRICE_OR_ACCUSATION", "comparison or complaint requires contextual review, not an accusation", true, .7)
	}
	if len(urls) >= 32 {
		add("URL_SCAN_LIMIT", "link extraction limit reached; destination coverage partial", true, .5)
	}
	for _, u := range urls {
		if u.Unknown {
			add("UNKNOWN_EXTERNAL_DOMAIN", u.Domain, true, 1)
		}
		if u.Bot || u.BotHeuristic {
			evidence := u.Value
			if evidence == "" {
				evidence = "confirmed is_bot object in text_mention entity"
			}
			add("TELEGRAM_BOT_LINK", evidence, true, func() float64 {
				if u.Bot {
					return 1
				}
				return .8
			}())
		} else if u.Telegram {
			add("TELEGRAM_LINK_UNVERIFIED", u.Value, true, .5)
		}
		if u.Referral {
			add("REFERRAL_LINK", u.Value, false, .8)
		}
		if u.Shortener {
			add("SHORTENER", u.Domain, false, .8)
		}
		if u.Obfuscated {
			add("DOMAIN_IMPERSONATION", u.Host, false, .65)
		}
		if u.Watch {
			add("WATCH_DOMAIN", u.Domain, false, 1)
		}
		if u.Denied {
			add("DENIED_DOMAIN", u.Domain, false, 1)
		}
	}
	if promo && m.RecentSimilar >= 2 {
		add("REPEATED_PROMO", "repeated promotional content observed within a minute", false, .8)
	}
	if promo && m.RecentLinks >= 3 {
		add("MASS_IDENTICAL_LINKS", "repeated destination observed within a minute", false, .8)
	}
	if promo {
		add("EXTERNAL_PURCHASE_CTA", "commercial call to action", false, .7)
		if criticism {
			add("ALTERNATIVE_API_CHEAPER", "commercial promotion combined with comparison", false, .7)
		}
	}
	if has(text, "введите", "пришлите", "отправьте", "enter your", "send your") && has(text, "api key", "api-key", "api_key", "токен", "пароль", "password", "token") {
		add("CREDENTIAL_SOLICITATION", "request to disclose a credential", false, .9)
	}
	discount := min(20, max(0, c.MaxTrustDiscount)) * a.Trust / 100
	a.Score = max(0, min(100, a.Score-discount))
	thresholds := c.Thresholds
	if len(thresholds) != 4 {
		thresholds = []int{25, 45, 70, 85}
	}
	bands := []string{"normal", "unusual", "suspicious", "high", "critical"}
	tier := 0
	for _, v := range thresholds {
		if a.Score >= v {
			tier++
		}
	}
	a.Band = bands[tier]
	a.ReviewRequired = a.Mandatory || a.Score >= c.AIThreshold
	return a
}
