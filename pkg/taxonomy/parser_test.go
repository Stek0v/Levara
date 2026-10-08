package taxonomy

import (
	"errors"
	"strings"
	"testing"
)

func TestTaxonomySeedGrammar(t *testing.T) {
	valid := "# Domains\n## AUTH\nОписание: identity\nАлиасы: login, 登录\n### Collections\n#### sessions\n##### Документ: Rules\nИсточник: data-1\n"
	for _, seed := range []string{valid, "\ufeff" + strings.ReplaceAll(valid, "\n", "\r\n"), "# Domains\n"} {
		c, err := Parse(seed)
		if err != nil {
			t.Fatal(err)
		}
		if c.Domains == nil {
			t.Fatal("nil domains")
		}
	}
	for name, seed := range map[string]string{
		"second_domain":               valid + "## next\nunsupported",
		"duplicate_unicode":           "# Domains\n## ЁЖ\n## ёж\n",
		"duplicate_collection":        "# Domains\n## a\n### Collections\n#### x\n#### X",
		"duplicate_document":          valid + "##### Документ: rules\nИсточник: another",
		"missing_source":              strings.ReplaceAll(valid, "Источник: data-1\n", ""),
		"duplicate_field":             valid + "Алиасы: first\nАлиасы: twice",
		"collection_without_section":  "# Domains\n## a\n#### c",
		"document_without_collection": "# Domains\n## a\n##### Документ: d\nИсточник: x",
		"unexpected_header":           valid + "# Domains",
		"source_on_domain":            "# Domains\n## a\nИсточник: x",
		"nul":                         "# Domains\n## a\x00b",
		"control":                     "# Domains\n## a\u0001b",
		"invalid_utf8":                "# Domains\n## " + string([]byte{0xff}),
		"too_large":                   strings.Repeat(" ", MaxSeedBytes+1),
		"too_many_nodes":              "# Domains\n" + strings.Repeat("## a\n", MaxNodes+1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(seed); !errors.Is(err, ErrInvalid) {
				t.Fatalf("accepted invalid seed: %v", err)
			}
		})
	}
	var exact strings.Builder
	exact.WriteString("# Domains\n")
	for i := 0; i < MaxNodes; i++ {
		exact.WriteString("## " + strings.Repeat("x", i+1) + "\n")
	}
	if _, err := Parse(exact.String()); err != nil {
		t.Fatalf("exact node bound: %v", err)
	}
	exact.WriteString("## overflow\n")
	if _, err := Parse(exact.String()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("node limit: %v", err)
	}
}
