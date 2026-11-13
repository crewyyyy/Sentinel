package match_test

import (
	"reflect"
	"strings"
	"testing"

	"sentinel/internal/rules/match"
)

func TestModes(t *testing.T) {
	cases := []struct {
		mode  string
		terms []string
		text  string
		want  bool
	}{
		{match.LegacySubstring, []string{"дешев"}, "ДЕШЁВЫЙ API", true},
		{match.LegacySubstring, []string{"ref"}, "reference", true},
		{match.Token, []string{"ref"}, "reference", false},
		{match.Token, []string{"ref"}, "some_REF123_bot", true},
		{match.Token, []string{"ref"}, "some_prefix_bot", false},
		{match.Token, []string{"buy"}, "buyer", false},
		{match.Token, []string{"buy"}, "BUY now", true},
		{match.Phrase, []string{"send your"}, "Please SEND, YOUR token", true},
		{match.Phrase, []string{"send your"}, "send me your token", false},
		{match.Phrase, []string{"send your"}, "resend your token", false},
		{match.Exact, []string{"ref"}, "REF", true},
		{match.Exact, []string{"ref"}, "reference", false},
		{match.Exact, []string{"ref"}, "ref_id", false},
		{match.Exact, []string{"ref"}, " ref", false},
	}
	for _, tc := range cases {
		t.Run(tc.mode+"/"+tc.text, func(t *testing.T) {
			m, err := match.New(tc.mode, tc.terms)
			if err != nil {
				t.Fatal(err)
			}
			if got := m.Match(tc.text); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNormalizeAndTokens(t *testing.T) {
	if got := match.Normalize("ДЕШЁВЫЙ API"); got != "дешевый api" {
		t.Fatal(got)
	}
	got := match.Tokens("@my_REF123_bot/reФ45 ЁЖ2 reference")
	want := []string{"my", "ref", "123", "bot", "reф", "45", "еж", "2", "reference"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := match.Tokens(" _/@ "); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestLiteralLimitsAndCopies(t *testing.T) {
	for _, tc := range []struct {
		mode  string
		terms []string
	}{
		{"regex", []string{"ref"}}, {match.Token, []string{"buy now"}}, {match.Phrase, []string{"!?"}},
		{match.Exact, nil}, {match.Exact, []string{}}, {match.Exact, []string{" "}},
		{match.Exact, []string{string([]byte{0xff})}},
		{match.Exact, []string{strings.Repeat("x", match.MaxTermBytes+1)}},
		{match.Exact, strings.Split(strings.Repeat("x,", match.MaxTerms), ",")},
		{match.Exact, func() []string {
			v := make([]string, match.MaxTerms)
			for i := range v {
				v[i] = strings.Repeat("x", match.MaxTermBytes)
			}
			return v
		}()},
	} {
		if _, err := match.New(tc.mode, tc.terms); err == nil {
			t.Fatalf("accepted invalid matcher %q", tc.mode)
		}
	}
	terms := []string{"ref"}
	m, err := match.New(match.Token, terms)
	if err != nil {
		t.Fatal(err)
	}
	terms[0] = "reference"
	if !m.Match("ref") || m.Match("reference") {
		t.Fatal("matcher retained caller terms")
	}
	literal, err := match.New(match.LegacySubstring, []string{".*"})
	if err != nil {
		t.Fatal(err)
	}
	if literal.Match("anything") || !literal.Match("literal .* characters") {
		t.Fatal("terms were interpreted as regex")
	}
}
