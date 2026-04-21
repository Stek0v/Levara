package http

import (
	"testing"
)

// BL-4: isQueryWordRune used to be ASCII-only via `'a' <= r && r <= 'z'`,
// which silently dropped every Russian / Cyrillic / accented-Latin query
// token. These cases lock in the unicode-aware behaviour.
func TestIsQueryWordRune_Unicode(t *testing.T) {
	cases := []struct {
		name string
		r    rune
		want bool
	}{
		{"ASCII lowercase", 'a', true},
		{"ASCII uppercase", 'Z', true},
		{"ASCII digit", '5', true},
		{"ASCII hyphen", '-', false},
		{"ASCII space", ' ', false},

		{"Cyrillic lowercase", 'а', true},
		{"Cyrillic uppercase", 'Я', true},
		{"Cyrillic letter", 'ё', true},

		{"Latin accented", 'é', true},
		{"Latin accented upper", 'Ä', true},

		{"Greek letter", 'Ω', true},
		{"CJK ideograph", '中', true},

		{"Punctuation question", '?', false},
		{"Punctuation comma", ',', false},
		{"Currency", '€', false},
		{"Emoji", '🚀', false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isQueryWordRune(tc.r); got != tc.want {
				t.Errorf("isQueryWordRune(%q) = %v, want %v", tc.r, got, tc.want)
			}
		})
	}
}
