package audit

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAnalyticsSanitizerRejectsUntrustedPayload(t *testing.T) {
	for _, raw := range []string{`{"value":"private`, `{"room":{"value":"private"},"key":["private"],"value":{"secret":"private"},"token":"[redacted]"}`, `{"hall":"fact","value":["private"]}`} {
		t.Run(raw, func(t *testing.T) {
			out := SanitizeArgsForAnalytics("save_memory", raw)
			if !json.Valid([]byte(out)) || strings.Contains(out, "private") {
				t.Fatalf("untrusted payload escaped sanitizer: %s", out)
			}
		})
	}
}
