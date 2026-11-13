package rules

import "sentinel/internal/rules/match"

func buildObservation(_ buildContext) []stage {
	return []stage{{order: 10, detector: detectorFunc(func(in input) []Finding {
		if in.first && in.message.UserID != nil {
			return []Finding{{"FIRST_OBSERVED", "first observed since installation; history coverage partial", 1}}
		}
		return nil
	})}}
}

func buildLinks(context buildContext) []stage {
	limit := context.modules["links"].thresholds["url_scan_limit"]
	return []stage{{order: 30, detector: detectorFunc(func(in input) []Finding {
		var findings []Finding
		if in.message.URLsPartial {
			findings = append(findings, Finding{"URL_SCAN_LIMIT", "link extraction incomplete; destination coverage partial", .5})
		} else if len(in.urls) >= limit {
			findings = append(findings, Finding{"URL_SCAN_LIMIT", "link extraction limit reached; destination coverage partial", .5})
		}
		for _, u := range in.urls {
			if u.Unknown {
				findings = append(findings, Finding{"UNKNOWN_EXTERNAL_DOMAIN", u.Domain, 1})
			}
			if u.Bot || u.BotHeuristic {
				evidence := u.Value
				if evidence == "" {
					evidence = "confirmed is_bot object in text_mention entity"
				}
				confidence := .8
				if u.Bot {
					confidence = 1
				}
				findings = append(findings, Finding{"TELEGRAM_BOT_LINK", evidence, confidence})
			} else if u.Telegram {
				findings = append(findings, Finding{"TELEGRAM_LINK_UNVERIFIED", u.Value, .5})
			}
			if u.Referral {
				findings = append(findings, Finding{"REFERRAL_LINK", u.Value, .8})
			}
			if u.Shortener {
				findings = append(findings, Finding{"SHORTENER", u.Domain, .8})
			}
			if u.Obfuscated {
				findings = append(findings, Finding{"DOMAIN_IMPERSONATION", u.Host, .65})
			}
			if u.Watch {
				findings = append(findings, Finding{"WATCH_DOMAIN", u.Domain, 1})
			}
			if u.Denied {
				findings = append(findings, Finding{"DENIED_DOMAIN", u.Domain, 1})
			}
		}
		return findings
	})}}
}

func buildCommerce(context buildContext) []stage {
	promotion := context.modules["commerce"].matchers["promotion"]
	criticism := context.modules["commerce"].matchers["criticism"]
	return []stage{
		{order: 20, detector: detectorFunc(func(in input) []Finding {
			if in.matches(criticism) {
				return []Finding{{"COMPETITOR_PRICE_OR_ACCUSATION", "comparison or complaint requires contextual review, not an accusation", .7}}
			}
			return nil
		})},
		{order: 50, detector: detectorFunc(func(in input) []Finding {
			if !in.matches(promotion) {
				return nil
			}
			findings := []Finding{{"EXTERNAL_PURCHASE_CTA", "commercial call to action", .7}}
			if in.matches(criticism) {
				findings = append(findings, Finding{"ALTERNATIVE_API_CHEAPER", "commercial promotion combined with comparison", .7})
			}
			return findings
		})},
	}
}

func buildRepetition(context buildContext) []stage {
	promotion := context.modules["commerce"].matchers["promotion"]
	similar := context.modules["repetition"].thresholds["recent_similar"]
	links := context.modules["repetition"].thresholds["recent_links"]
	return []stage{{order: 40, detector: detectorFunc(func(in input) []Finding {
		if !in.matches(promotion) {
			return nil
		}
		var findings []Finding
		if in.message.RecentSimilar >= similar {
			findings = append(findings, Finding{"REPEATED_PROMO", "repeated promotional content observed within a minute", .8})
		}
		if in.message.RecentLinks >= links {
			findings = append(findings, Finding{"MASS_IDENTICAL_LINKS", "repeated destination observed within a minute", .8})
		}
		return findings
	})}}
}

func buildCredentials(context buildContext) []stage {
	request := context.modules["credentials"].matchers["request"]
	secret := context.modules["credentials"].matchers["secret"]
	return []stage{{order: 60, detector: detectorFunc(func(in input) []Finding {
		if in.matches(request) && in.matches(secret) {
			return []Finding{{"CREDENTIAL_SOLICITATION", "request to disclose a credential", .9}}
		}
		return nil
	})}}
}

func buildReferral(context buildContext) []stage {
	queryKeys := context.modules["referral_cues"].matchers["query_keys"]
	botComponents := context.modules["referral_cues"].matchers["bot_components"]
	return []stage{{order: 70, detector: detectorFunc(func(in input) []Finding {
		for _, u := range in.urls {
			for _, key := range u.QueryKeys {
				if queryKeys.Match(key) {
					return []Finding{{"REFERRAL_REVIEW", "referral query key: " + match.Normalize(key), 1}}
				}
			}
			if (u.Bot || u.BotHeuristic) && matchesComponent(botComponents, u.TelegramUsername) {
				confidence := .8
				if u.Bot {
					confidence = 1
				}
				return []Finding{{"REFERRAL_REVIEW", "referral marker in bot username: " + match.Normalize(u.TelegramUsername), confidence}}
			}
		}
		partial := in.message.FeaturesPartial || in.message.URLsPartial
		for _, u := range in.urls {
			partial = partial || u.QueryKeysPartial
		}
		for _, mention := range in.message.Mentions {
			if (mention.BotStatus == "confirmed" || mention.BotStatus == "heuristic") && matchesComponent(botComponents, mention.Username) {
				confidence := .8
				if mention.BotStatus == "confirmed" {
					confidence = 1
				}
				return []Finding{{"REFERRAL_REVIEW", "referral marker in bot mention: " + match.Normalize(mention.Username), confidence}}
			}
		}
		if partial {
			return []Finding{{"REFERRAL_REVIEW", "referral feature coverage partial; a referral is not established", .5}}
		}
		return nil
	})}}
}

func matchesComponent(matcher *match.Matcher, username string) bool {
	for _, component := range match.Tokens(username) {
		if matcher.Match(component) {
			return true
		}
	}
	return false
}
