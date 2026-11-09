package privacy

import (
	"encoding/json"
	"sentinel/internal/domain"
	"strings"
	"testing"
)

func TestRedaction(t *testing.T) {
	for _, s := range []string{"api_key=supersecret123", "password: secret123", "https://example.org?access_token=abcdsecret&ref=1", "sk-testsecretkey1234", "123456789:abcdefghijklmnopqrstuvwxy", "Authorization: Bearer secretkey12345", "email@example.org", "+12345678901"} {
		got := Redact(s)
		if got == s || strings.Contains(got, "supersecret123") || strings.Contains(got, "abcdsecret") || strings.Contains(got, "secretkey12345") {
			t.Fatalf("not redacted: %s", got)
		}
	}
}
func TestNestedMessage(t *testing.T) {
	m := Message(domain.Message{Text: "api_key=secretvalue", Reply: &domain.Message{Caption: "password=secretvalue"}, URLs: []domain.URL{{Value: "https://example.org?token=secretvalue"}}})
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "secretvalue") {
		t.Fatal(string(b))
	}
}
