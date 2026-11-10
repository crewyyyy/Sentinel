package match

import "testing"

func TestPreparedPartsKeepLegacyButDoNotInventPhrases(t *testing.T) {
	joined, parts := PrepareParts("КУПИТЕ", "У другого")
	legacy, err := New(LegacySubstring, []string{"купите у"})
	if err != nil {
		t.Fatal(err)
	}
	phrase, err := New(Phrase, []string{"купите у"})
	if err != nil {
		t.Fatal(err)
	}
	if !legacy.MatchParts(joined, parts) {
		t.Fatal("legacy cross-part matching changed")
	}
	if phrase.MatchParts(joined, parts) {
		t.Fatal("phrase invented across original and quoted text")
	}
	joined, parts = PrepareParts("КУПИТЕ У другого", "")
	if !phrase.MatchParts(joined, parts) {
		t.Fatal("prepared case folding lost valid phrase")
	}
}
