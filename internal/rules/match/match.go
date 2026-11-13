// Package match provides bounded, literal matchers shared by rule modules.
package match

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	NormalizationVersion = "lowercase-yo-v1"
	LegacySubstring      = "legacy_substring"
	Token                = "token"
	Phrase               = "phrase"
	Exact                = "exact"
	MaxTerms             = 128
	MaxTermBytes         = 256
	MaxTotalTermBytes    = 16 * 1024
)

// Normalize preserves v1's Unicode lowercasing and Russian ё/е equivalence.
func Normalize(text string) string {
	return strings.ReplaceAll(strings.ToLower(text), "ё", "е")
}

// Tokens splits on punctuation and letter/digit boundaries: ref_123_bot becomes
// [ref 123 bot], while reference stays one token. No user-supplied regex is run.
func Tokens(text string) []string {
	return tokensNormalized(Normalize(text))
}

func tokensNormalized(text string) []string {
	var result []string
	var token strings.Builder
	previous := 0
	flush := func() {
		if token.Len() > 0 {
			result = append(result, token.String())
			token.Reset()
		}
	}
	for _, r := range text {
		kind := 0
		if unicode.IsLetter(r) {
			kind = 1
		} else if unicode.IsDigit(r) {
			kind = 2
		}
		if kind == 0 || previous != 0 && previous != kind {
			flush()
		}
		if kind != 0 {
			token.WriteRune(r)
		}
		previous = kind
	}
	flush()
	return result
}

// Prepared is a per-message immutable projection shared by all text detectors.
type Prepared struct {
	text   string
	tokens []string
}

func Prepare(text string) Prepared {
	normalized := Normalize(text)
	return Prepared{text: normalized, tokens: tokensNormalized(normalized)}
}

func PrepareParts(texts ...string) (Prepared, []Prepared) {
	parts := make([]Prepared, 0, len(texts))
	normalized := make([]string, 0, len(texts))
	for _, text := range texts {
		part := Prepare(text)
		parts = append(parts, part)
		normalized = append(normalized, part.text)
	}
	return Prepared{text: strings.Join(normalized, " ")}, parts
}

// Matcher owns normalized copies of its terms and can be shared by goroutines.
// It exposes no mutable settings and compiles only during analyzer construction.
type Matcher struct {
	mode    string
	terms   []string
	phrases [][]string
}

func ValidMode(mode string) bool {
	return mode == LegacySubstring || mode == Token || mode == Phrase || mode == Exact
}

func New(mode string, terms []string) (*Matcher, error) {
	if !ValidMode(mode) {
		return nil, fmt.Errorf("unknown matcher mode %q", mode)
	}
	if len(terms) == 0 || len(terms) > MaxTerms {
		return nil, fmt.Errorf("matcher needs 1..%d terms", MaxTerms)
	}
	m := &Matcher{mode: mode}
	total := 0
	for i, term := range terms {
		total += len(term)
		if !utf8.ValidString(term) || strings.TrimSpace(term) == "" || len(term) > MaxTermBytes || total > MaxTotalTermBytes {
			return nil, fmt.Errorf("invalid or oversized matcher term at index %d", i)
		}
		normalized := Normalize(term)
		m.terms = append(m.terms, normalized)
		if mode == Token || mode == Phrase {
			tokens := Tokens(normalized)
			if len(tokens) == 0 || mode == Token && len(tokens) != 1 {
				return nil, fmt.Errorf("%s matcher term at index %d has invalid token count", mode, i)
			}
			m.phrases = append(m.phrases, tokens)
		}
	}
	return m, nil
}

func (m *Matcher) Match(text string) bool {
	return m.matchPrepared(Prepared{text: Normalize(text)})
}

// MatchParts keeps legacy cross-part behavior for compatibility. New token and
// phrase matchers cannot manufacture a phrase across body/caption and quote boundaries.
func (m *Matcher) MatchParts(joined Prepared, parts []Prepared) bool {
	if m.mode == LegacySubstring {
		return m.matchPrepared(joined)
	}
	for _, part := range parts {
		if m.matchPrepared(part) {
			return true
		}
	}
	return false
}

func (m *Matcher) matchPrepared(prepared Prepared) bool {
	text := prepared.text
	switch m.mode {
	case LegacySubstring:
		for _, term := range m.terms {
			if strings.Contains(text, term) {
				return true
			}
		}
	case Exact:
		for _, term := range m.terms {
			if text == term {
				return true
			}
		}
	case Token, Phrase:
		tokens := prepared.tokens
		if tokens == nil {
			tokens = tokensNormalized(text)
		}
		for _, phrase := range m.phrases {
			for start := 0; start+len(phrase) <= len(tokens); start++ {
				matched := true
				for i, term := range phrase {
					if tokens[start+i] != term {
						matched = false
						break
					}
				}
				if matched {
					return true
				}
			}
		}
	}
	return false
}
