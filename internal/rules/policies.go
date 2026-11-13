package rules

import (
	"sentinel/internal/domain"
	"sentinel/internal/rules/match"
)

const policyNegationGapTokens = 3

// policySpec is declarative: the catalog owns every dictionary and descriptor;
// the same detector implements all categories and consumes resolved overrides.
type policySpec struct {
	id          string
	code        string
	title       string
	description string
	order       int
	matchers    map[string]MatcherSpec
}

func policyModule(spec policySpec) Spec {
	return Spec{
		ID: spec.id, Version: "v1", Title: spec.title, Description: spec.description,
		Signals:  []SignalSpec{{Code: spec.code, Mandatory: true, Family: spec.id}},
		Matchers: spec.matchers,
		Thresholds: map[string]ThresholdSpec{
			"match_window_tokens": {Default: 12, Min: 2, Max: 64},
			"sensitivity":         {Default: 2, Min: 1, Max: 3},
			"policy":              {Default: 0, Min: 0, Max: 1},
		},
		build: func(context buildContext) []stage { return buildPolicy(spec, context) },
	}
}

// preparePolicyContexts is shared across enabled policy modules. Entity offsets
// belong to the original source, so stale/redacted text must never reuse them.
func preparePolicyContexts(message domain.Message) []match.Context {
	quote := match.ParseContext(message.QuoteText)
	if message.FeaturesPartial {
		return []match.Context{{Partial: true}, quote}
	}
	entities := message.Entities
	if message.Text == "" {
		entities = message.CaptionEntities
	}
	var quoted []match.UTF16Range
	for _, entity := range entities {
		switch entity.Type {
		case "blockquote", "expandable_blockquote", "code", "pre":
			if message.FeaturesVersion != domain.CurrentFeaturesVersion || len(quoted) >= match.MaxContextQuoteRanges {
				return []match.Context{{Partial: true}, quote}
			}
			quoted = append(quoted, match.UTF16Range{Offset: entity.Offset, Length: entity.Length})
		}
	}
	return []match.Context{match.ParseContext(message.Content(), quoted...), quote}
}

func buildPolicy(spec policySpec, context buildContext) []stage {
	settings := context.modules[spec.id]
	return []stage{{order: spec.order, detector: detectorFunc(func(in input) []Finding {
		window := settings.thresholds["match_window_tokens"]
		sensitivity := settings.thresholds["sensitivity"]
		prohibited := settings.thresholds["policy"] == 1
		var contextual *policyContext
		incomplete := in.message.FeaturesPartial
		contexts := in.contexts
		if len(contexts) < 2 {
			contexts = preparePolicyContexts(in.message)
		}
		// A Telegram reply quote is not the author's body. Keep it separate even
		// when the ordinary message also contains an explicit, unrelated offer.
		for index, quoted := range []bool{in.message.Forwarded, true} {
			parsed := contexts[index]
			if parsed.Partial {
				incomplete = true
				continue
			}
			segments, partial := policySegments(parsed, settings.matchers)
			if partial {
				incomplete = true
				continue
			}
			for _, segment := range segments {
				for _, intent := range segment.cues["intent"] {
					for _, subject := range segment.cues["subject"] {
						pair := intent.Cover(subject)
						// One lexical occurrence must not play both roles.
						if pair.Len() > window || intent.Start < subject.End && subject.Start < intent.End {
							continue
						}
						if sensitivity == 1 && !nearPolicyCue(segments, segment, "solicitation", pair, window, false, true) {
							continue
						}
						flags := policyContext{
							quoted:   quoted || segment.context.Quoted,
							question: segment.context.Question || nearPolicyCue(segments, segment, "question", intent, window, true, true),
							reported: nearPolicyCue(segments, segment, "reported", pair, window, false, true),
						}
						for _, negation := range segment.cues["negation"] {
							if negation.End <= intent.Start && intent.Start-negation.End <= min(policyNegationGapTokens, window) {
								flags.negated = true
								break
							}
						}
						if flags.contextual() {
							if sensitivity == 3 && contextual == nil {
								copy := flags
								contextual = &copy
							}
							continue
						}
						return []Finding{{spec.code, policyEvidence(spec.id, flags, prohibited, false), .7}}
					}
				}
			}
		}
		if contextual != nil {
			return []Finding{{spec.code, policyEvidence(spec.id, *contextual, prohibited, incomplete), .5}}
		}
		if incomplete {
			// Bounded parsing must not silently turn incomplete evidence into safe.
			return []Finding{{spec.code, policyEvidence(spec.id, policyContext{}, prohibited, true), .3}}
		}
		return nil
	})}}
}

type policySegment struct {
	context match.ContextSegment
	cues    map[string][]match.Span
}

func policySegments(context match.Context, matchers map[string]*match.Matcher) ([]policySegment, bool) {
	var segments []policySegment
	counts := map[string]int{}
	for _, segment := range context.Segments {
		current := policySegment{context: segment, cues: map[string][]match.Span{}}
		for _, name := range []string{"intent", "subject", "negation", "solicitation", "question", "reported"} {
			found := matchers[name].Find(segment.Prepared)
			counts[name] += len(found.Spans)
			if found.Partial || counts[name] > match.MaxContextSpans {
				return nil, true
			}
			for i := range found.Spans {
				found.Spans[i].Start += segment.TokenStart
				found.Spans[i].End += segment.TokenStart
			}
			current.cues[name] = found.Spans
		}
		segments = append(segments, current)
	}
	return segments, false
}

func nearPolicyCue(segments []policySegment, candidate policySegment, name string, pair match.Span, window int, preceding, sameQuotation bool) bool {
	for _, segment := range segments {
		if segment.context.Sentence != candidate.context.Sentence || sameQuotation && segment.context.Quoted != candidate.context.Quoted {
			continue
		}
		for _, cue := range segment.cues[name] {
			if preceding && cue.End > pair.Start {
				continue
			}
			if pair.Cover(cue).Len() <= window {
				return true
			}
		}
	}
	return false
}

type policyContext struct {
	quoted   bool
	reported bool
	question bool
	negated  bool
}

func (c policyContext) contextual() bool { return c.quoted || c.reported || c.question || c.negated }

func policyEvidence(id string, flags policyContext, prohibited, incomplete bool) string {
	evidence := id + " cues nearby; contextual review only"
	if incomplete {
		evidence = id + " context coverage incomplete; contextual review only"
	}
	if flags.quoted {
		evidence += "; quoted or forwarded text"
	}
	if flags.reported {
		evidence += "; reported text"
	}
	if flags.question {
		evidence += "; question context"
	}
	if flags.negated {
		evidence += "; negated intent"
	}
	if prohibited {
		evidence += "; prohibited by chat policy, violation not established"
	}
	return evidence
}
