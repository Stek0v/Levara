package extract

import (
	"reflect"
	"testing"
)

func TestAnalyzeCodeCheckedSupported(t *testing.T) {
	for _, tc := range []struct{ name, source, language string }{
		{"empty.GO", "package empty", "go"},
		{"sample.Go", goSample, "go"},
		{"sample.PY", pySample, "python"},
		{"empty.py", "", "python"},
		// Python is heuristic extraction, not a syntax validator.
		{"heuristic.py", "def found(:\n  pass\n", "python"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := AnalyzeCodeChecked(tc.source, tc.name)
			if err != nil {
				t.Fatal(err)
			}
			if got.Language != tc.language || !reflect.DeepEqual(got, AnalyzeCode(tc.source, tc.name)) {
				t.Fatalf("legacy supported parity: %+v", got)
			}
		})
	}
}
func TestAnalyzeCodeCheckedRejectsUnsupportedAndPartialGo(t *testing.T) {
	for _, tc := range []struct{ name, source string }{
		{"sample.js", "function valid() {}"}, {"sample.TS", "export const a=1"},
		{"sample.rb", "puts 'hello'"}, {"missing-extension", "package sample"},
		{"empty.go", ""}, {"broken.go", "package sample\nfunc Valid() {}\nfunc Broken( {"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := AnalyzeCodeChecked(tc.source, tc.name)
			if err == nil {
				t.Fatal("accepted unsupported or malformed input")
			}
			if len(got.Entities) != 0 || len(got.Relations) != 0 {
				t.Fatalf("partial results admitted: %+v", got)
			}
		})
	}
	partial := AnalyzeCode("package sample\nfunc Valid() {}\nfunc Broken( {", "legacy.go")
	if !contains(entityNames(partial, "function"), "Valid") {
		t.Fatal("legacy partial AST extraction changed")
	}
}
