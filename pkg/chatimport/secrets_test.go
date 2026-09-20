package chatimport

import "testing"

func TestScanSecrets(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string // expected pattern name, empty means no findings
	}{
		{"openai", "ключ: sk-proj-abcdefghijklmnopqrst", "OpenAI API key"},
		{"anthropic", "export ANTHROPIC_API_KEY=sk-ant-api03-xxxxxxxxxxxxxxxxxxxx", "Anthropic API key"},
		{"github", "token ghp_0123456789abcdefghijkl in text", "GitHub token"},
		{"aws", "aws_access_key_id = " + "AKIA" + "IOSFODNN7EXAMPLE", "AWS access key"},
		{"slack", "xoxb-123456789012345", "Slack token"},
		{"google", "key: AIzaSyA1234567890abcdefghijklmnopq", "Google API key"},
		{"jwt", "Authorization: eyJhbGciOiJI.eyJzdWIiOiIx.SflKxwRJSM", "JWT"},
		{"pem", "-----BEGIN RSA PRIVATE KEY-----\nMIIB\n-----END RSA PRIVATE KEY-----", "private key block"},
		{"bearer", "curl -H 'Authorization: Bearer abcdef1234567890abcdef12' url", "bearer token"},
		{"clean prose", "обычный текст про WAL и снапшоты, никаких ключей", ""},
		{"short sk", "sk-abc не ключ", ""},
		{"code identifier", "task_status_is_ready = true", ""},
		{"uuid", "id: 11111111-2222-3333-4444-555555555555", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ScanSecrets(tc.content)
			if tc.want == "" {
				if len(got) != 0 {
					t.Fatalf("false positive: %v", got)
				}
				return
			}
			for _, g := range got {
				if g == tc.want {
					return
				}
			}
			t.Fatalf("pattern %q not found in %v", tc.want, got)
		})
	}
}
