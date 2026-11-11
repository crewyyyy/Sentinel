package match

import (
	"reflect"
	"strings"
	"testing"
)

func TestContextKeepsClauseQuoteAndQuestionBoundaries(t *testing.T) {
	context := ParseContext("Он написал: «ПрОдаМ доступ, пишите мне». А где купить доступ?\n> раздаю инвайты\nОбычный текст")
	if context.Partial || len(context.Segments) != 6 {
		t.Fatalf("unexpected context: %+v", context)
	}
	want := []struct {
		tokens   []string
		start    int
		sentence int
		quoted   bool
		question bool
	}{
		{[]string{"он", "написал"}, 0, 0, false, false},
		{[]string{"продам", "доступ"}, 2, 0, true, false},
		{[]string{"пишите", "мне"}, 4, 0, true, false},
		{[]string{"а", "где", "купить", "доступ"}, 6, 1, false, true},
		{[]string{"раздаю", "инвайты"}, 10, 3, true, false},
		{[]string{"обычный", "текст"}, 12, 4, false, false},
	}
	// A newline after a question is also a sentence boundary, but never a cue.
	if len(context.Segments) != len(want) {
		t.Fatalf("segments=%d, want %d: %+v", len(context.Segments), len(want), context)
	}
	for i, expected := range want {
		got := context.Segments[i]
		if !reflect.DeepEqual(got.Prepared.tokens, expected.tokens) || got.TokenStart != expected.start || got.Sentence != expected.sentence || got.Quoted != expected.quoted || got.Question != expected.question {
			t.Fatalf("segment %d: %+v, want %+v", i, got, expected)
		}
	}
}

func TestContextQuoteFormsAndApostrophes(t *testing.T) {
	for _, text := range []string{
		"«продам доступ»", "\"продам доступ\"", "'продам доступ'", "“продам доступ”", "„продам доступ“",
		"`продам доступ`", "```продам доступ```", "> продам доступ", "  >> продам доступ", "«цитата \"продам доступ\"»",
	} {
		context := ParseContext(text)
		if context.Partial || len(context.Segments) == 0 {
			t.Fatal(text, context)
		}
		for _, segment := range context.Segments {
			if !segment.Quoted {
				t.Fatalf("lost quote context for %q: %+v", text, segment)
			}
		}
	}
	context := ParseContext("I'm selling access")
	if len(context.Segments) != 1 || context.Segments[0].Quoted || !reflect.DeepEqual(context.Segments[0].Prepared.tokens, Tokens("I'm selling access")) {
		t.Fatalf("apostrophe changed tokenization or quote attribution: %+v", context)
	}
	context = ParseContext("Продаю доступ, пишите? После вопроса продам ключ.")
	if len(context.Segments) != 3 || !context.Segments[0].Question || !context.Segments[1].Question || context.Segments[2].Question {
		t.Fatalf("question scope leaked: %+v", context)
	}
}

func TestFindTokenSpansAreLiteralNormalizedAndDeduplicated(t *testing.T) {
	matcher, err := New(Phrase, []string{"продам доступ", "КЛЮЧ", "ключ", "еще"})
	if err != nil {
		t.Fatal(err)
	}
	got := matcher.Find(Prepare("ЕЩЁ продам доступ, ключник и ключ"))
	want := []Span{{0, 1}, {1, 3}, {5, 6}}
	if got.Partial || !reflect.DeepEqual(got.Spans, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if span := got.Spans[0].Cover(got.Spans[1]); span != (Span{0, 3}) || span.Len() != 3 {
		t.Fatal(span)
	}
	for _, mode := range []string{LegacySubstring, Exact} {
		matcher, err := New(mode, []string{"access"})
		if err != nil {
			t.Fatal(err)
		}
		if got := matcher.Find(Prepare("access")); len(got.Spans) != 0 || got.Partial {
			t.Fatalf("invented positional semantics for %s", mode)
		}
	}
}

func TestContextStructuredQuotesUseUTF16AndKeepAttributionLocal(t *testing.T) {
	quoted := "продам доступ"
	context := ParseContext("🙂 "+quoted+" конец", UTF16Range{Offset: 3, Length: len([]rune(quoted))})
	if context.Partial || len(context.Segments) != 2 || !context.Segments[0].Quoted || context.Segments[1].Quoted || context.Segments[1].TokenStart != 2 {
		t.Fatalf("structured quote used byte offsets or leaked: %+v", context)
	}
	if !reflect.DeepEqual(context.Segments[0].Prepared.tokens, Tokens(quoted)) {
		t.Fatal("structured quote changed lexical evidence")
	}
	// An unmatched textual delimiter inside an entity cannot quote the author.
	quoted += " «"
	context = ParseContext(quoted+" продам ключ", UTF16Range{Offset: 0, Length: len([]rune(quoted))})
	if context.Partial || len(context.Segments) != 2 || !context.Segments[0].Quoted || context.Segments[1].Quoted {
		t.Fatalf("delimiter inside entity escaped its boundary: %+v", context)
	}
	context = ParseContext("продам ключ, где?", UTF16Range{Offset: len([]rune("продам ключ, ")), Length: len([]rune("где?"))})
	if context.Partial || len(context.Segments) != 2 || context.Segments[0].Question || !context.Segments[1].Question {
		t.Fatalf("quoted question changed the author's intent: %+v", context)
	}
	for _, interval := range []UTF16Range{
		{Offset: -1, Length: 1}, {Offset: 0, Length: -1}, {Offset: 0, Length: 0},
		{Offset: 1, Length: 2}, {Offset: 0, Length: 1}, {Offset: 999, Length: 1}, {Offset: 0, Length: int(^uint(0) >> 1)},
	} {
		if context := ParseContext("🙂 продам доступ", interval); !context.Partial {
			t.Fatalf("invalid or surrogate-splitting quote silently accepted: %+v", interval)
		}
	}
	if context := ParseContext("доступ", make([]UTF16Range, MaxContextQuoteRanges+1)...); !context.Partial {
		t.Fatal("structured quote range budget was silent")
	}
}

func TestContextAndOccurrencesHaveExplicitBounds(t *testing.T) {
	for _, text := range []string{
		strings.Repeat("x", MaxContextBytes+1),
		strings.Repeat("x ", MaxContextTokens+1),
		strings.Repeat("x,", MaxContextSegments+1),
		strings.Repeat("«", maxQuoteDepth+1),
	} {
		context := ParseContext(text)
		if !context.Partial || len(context.Segments) > MaxContextSegments {
			t.Fatalf("context limit was silent: segments=%d partial=%v", len(context.Segments), context.Partial)
		}
		for _, segment := range context.Segments {
			if segment.TokenStart+len(segment.Prepared.tokens) > MaxContextTokens {
				t.Fatal("token limit exceeded")
			}
		}
	}
	matcher, err := New(Token, []string{"x"})
	if err != nil {
		t.Fatal(err)
	}
	got := matcher.Find(Prepare(strings.Repeat("x ", MaxContextSpans+1)))
	if !got.Partial || len(got.Spans) != MaxContextSpans {
		t.Fatalf("span limit was silent: spans=%d partial=%v", len(got.Spans), got.Partial)
	}
}
