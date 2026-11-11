package match

import (
	"sort"
	"strings"
	"unicode"
)

const (
	MaxContextBytes       = 64 * 1024
	MaxContextTokens      = 8192
	MaxContextSegments    = 1024
	MaxContextSpans       = 1024
	MaxContextQuoteRanges = 128
	maxQuoteDepth         = 16
)

// Span is a half-open token range, never a byte or Telegram UTF-16 offset.
type Span struct {
	Start int
	End   int
}

func (s Span) Cover(other Span) Span {
	return Span{Start: min(s.Start, other.Start), End: max(s.End, other.End)}
}

func (s Span) Len() int { return s.End - s.Start }

// ContextSegment keeps a clause and quotation boundary intact. TokenStart is
// relative to the whole input, so nearby cues in comma-separated clauses can be
// compared without manufacturing a phrase across those clauses.
type ContextSegment struct {
	Prepared   Prepared
	TokenStart int
	Sentence   int
	Quoted     bool
	Question   bool
}

type Context struct {
	Segments []ContextSegment
	Partial  bool
}

// UTF16Range describes a quoted interval in the exact, unmodified source text.
// The caller must not reuse offsets after redaction or truncation.
type UTF16Range struct {
	Offset int
	Length int
}

// ParseContext is a bounded syntactic projection, not a natural-language parser.
// It recognizes sentence/clause punctuation, ordinary quotes, code quotes and
// Markdown blockquotes. Optional source ranges preserve structured blockquotes;
// invalid offsets yield explicit partial coverage. Lexical matching still uses
// the shared tokenizer.
func ParseContext(text string, quoted ...UTF16Range) Context {
	result := Context{}
	if len(text) > MaxContextBytes {
		text = text[:MaxContextBytes]
		result.Partial = true
	}
	runes := []rune(text)
	quoteEvents, partial := contextQuoteEvents(runes, quoted)
	result.Partial = result.Partial || partial
	var buffer strings.Builder
	var quotes, outerQuotes []rune
	tokenStart, sentence, sentenceStart, entityDepth := 0, 0, 0, 0
	lineStart, lineQuoted, entityQuoted, stopped := true, false, false, false
	flush := func() {
		if buffer.Len() == 0 || stopped {
			return
		}
		prepared := Prepare(buffer.String())
		buffer.Reset()
		if len(prepared.tokens) == 0 {
			return
		}
		if len(result.Segments) >= MaxContextSegments || tokenStart >= MaxContextTokens {
			result.Partial, stopped = true, true
			return
		}
		if len(prepared.tokens) > MaxContextTokens-tokenStart {
			prepared.tokens = prepared.tokens[:MaxContextTokens-tokenStart]
			result.Partial, stopped = true, true
		}
		result.Segments = append(result.Segments, ContextSegment{
			Prepared: prepared, TokenStart: tokenStart, Sentence: sentence,
			Quoted: lineQuoted || entityQuoted || len(quotes) > 0,
		})
		tokenStart += len(prepared.tokens)
	}
	endSentence := func(question bool) {
		flush()
		if question {
			quoted := lineQuoted || entityQuoted || len(quotes) > 0
			for i := sentenceStart; i < len(result.Segments); i++ {
				if result.Segments[i].Quoted == quoted {
					result.Segments[i].Question = true
				}
			}
		}
		sentence++
		sentenceStart = len(result.Segments)
	}

	for i, r := range runes {
		if stopped {
			break
		}
		if delta := quoteEvents[i]; delta != 0 {
			nextDepth := entityDepth + delta
			if entityQuoted != (nextDepth > 0) {
				flush()
				// Delimiters inside a structured quote cannot affect attribution
				// of the author's text after its boundary.
				if nextDepth > 0 {
					outerQuotes, quotes = quotes, nil
				} else {
					quotes, outerQuotes = outerQuotes, nil
				}
			}
			entityDepth, entityQuoted = nextDepth, nextDepth > 0
		}
		if r == '\n' || r == '\r' {
			endSentence(false)
			if lineQuoted {
				quotes = nil
			}
			lineStart, lineQuoted = true, false
			continue
		}
		if lineStart {
			if unicode.IsSpace(r) {
				continue
			}
			if r == '>' {
				lineQuoted = true
				continue
			}
			lineStart = false
		}
		// Apostrophes inside words are punctuation, not quotation delimiters.
		apostrophe := r == '\'' && i > 0 && i+1 < len(runes) && unicode.IsLetter(runes[i-1]) && unicode.IsLetter(runes[i+1])
		if !apostrophe {
			if len(quotes) > 0 && r == quotes[len(quotes)-1] {
				flush()
				quotes = quotes[:len(quotes)-1]
				continue
			}
			var closing rune
			switch r {
			case '«':
				closing = '»'
			case '“':
				closing = '”'
			case '„':
				closing = '“'
			case '"', '\'', '`':
				closing = r
			}
			if closing != 0 {
				flush()
				if len(quotes) >= maxQuoteDepth {
					result.Partial, stopped = true, true
					break
				}
				quotes = append(quotes, closing)
				continue
			}
		}
		switch r {
		case '.', '!', '?', ';', '。', '！', '？':
			endSentence(r == '?' || r == '？')
		case ',', ':', '—', '–':
			flush()
		default:
			buffer.WriteRune(r)
		}
	}
	flush()
	return result
}

func contextQuoteEvents(runes []rune, quoted []UTF16Range) (map[int]int, bool) {
	if len(quoted) == 0 {
		return nil, false
	}
	if len(quoted) > MaxContextQuoteRanges {
		return nil, true
	}
	offsets := make([]int, len(runes)+1)
	for i, r := range runes {
		units := 1
		if r > 0xffff {
			units = 2
		}
		offsets[i+1] = offsets[i] + units
	}
	total := offsets[len(runes)]
	events := map[int]int{}
	partial := false
	for _, interval := range quoted {
		if interval.Offset < 0 || interval.Length <= 0 || interval.Offset > total || interval.Length > total-interval.Offset {
			partial = true
			continue
		}
		end := interval.Offset + interval.Length
		startIndex, endIndex := sort.SearchInts(offsets, interval.Offset), sort.SearchInts(offsets, end)
		if offsets[startIndex] != interval.Offset || offsets[endIndex] != end {
			// A boundary in the middle of a surrogate pair is not trustworthy.
			partial = true
			continue
		}
		events[startIndex]++
		events[endIndex]--
	}
	return events, partial
}

type Occurrences struct {
	Spans   []Span
	Partial bool
}

// Find returns bounded, deduplicated token spans for token/phrase matchers. Other
// modes have no token-position semantics and deliberately return no spans.
func (m *Matcher) Find(prepared Prepared) Occurrences {
	result := Occurrences{}
	if m.mode != Token && m.mode != Phrase {
		return result
	}
	tokens := prepared.tokens
	if tokens == nil {
		tokens = tokensNormalized(prepared.text)
	}
	if len(tokens) > MaxContextTokens {
		tokens = tokens[:MaxContextTokens]
		result.Partial = true
	}
	seen := map[Span]bool{}
	for start := range tokens {
		for _, phrase := range m.phrases {
			if start+len(phrase) > len(tokens) {
				continue
			}
			matched := true
			for i, term := range phrase {
				if tokens[start+i] != term {
					matched = false
					break
				}
			}
			span := Span{Start: start, End: start + len(phrase)}
			if !matched || seen[span] {
				continue
			}
			if len(result.Spans) >= MaxContextSpans {
				result.Partial = true
				return result
			}
			seen[span] = true
			result.Spans = append(result.Spans, span)
		}
	}
	return result
}
