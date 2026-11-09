package app

import (
	"bytes"
	"encoding/json"
	"sentinel/internal/domain"
	"sentinel/internal/rules"
	"strings"
	"testing"
)

func TestOfflineReplayAndUnknownRatios(t *testing.T) {
	var input bytes.Buffer
	enc := json.NewEncoder(&input)
	for _, s := range []Sample{{Text: "hello", Label: "benign"}, {Text: "Купите мой API дешевле", Label: "spam", First: true, URLs: []domain.URL{{Unknown: true, Referral: true, Domain: "fixture.example"}}}} {
		if err := enc.Encode(s); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	if err := Replay(&input, &output, rules.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	var result ReplayResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.TP != 1 || result.TN != 1 || result.AIRequired != 1 {
		t.Fatal(result)
	}
	output.Reset()
	if err := Replay(strings.NewReader(""), &output, rules.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"Precision":null`) {
		t.Fatal("invented precision", output.String())
	}
}
