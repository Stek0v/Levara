package http

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHTTPPolicyBoundary_NoDirectPolicySQLOutsideApprovedFiles(t *testing.T) {
	root := "."
	approvedTableFiles := map[string]bool{
		"rbac.go":                true, // share CRUD/listing; decisions call pkg/access.
		"schema.go":              true, // schema definitions and indexes.
		"search_test_helpers.go": true, // test fixture schema/data helpers.
		"tenants.go":             true, // tenant CRUD/listing; membership decisions call pkg/access.
	}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(b)
		base := filepath.Base(path)
		if strings.Contains(src, "SELECT COALESCE(is_superuser") {
			t.Fatalf("%s contains direct superuser policy SQL; use pkg/access.SQLPolicy.IsSuperuser", path)
		}
		if !approvedTableFiles[base] && (strings.Contains(src, "dataset_shares") || strings.Contains(src, "user_tenant")) {
			t.Fatalf("%s contains direct access table SQL outside approved CRUD/schema files", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
