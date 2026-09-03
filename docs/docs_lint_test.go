package docs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// contractCounts mirrors docs/contract.json so doc claims can be checked
// against the machine-derived API surface.
type contractCounts struct {
	MCP  int `json:"mcp"`
	REST int `json:"rest"`
	GRPC int `json:"grpc"`
}

func loadContractCounts(t *testing.T) contractCounts {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "docs", "contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	var full struct {
		Rest []json.RawMessage `json:"rest"`
		GRPC []json.RawMessage `json:"grpc"`
		MCP  []json.RawMessage `json:"mcp"`
	}
	if err := json.Unmarshal(raw, &full); err != nil {
		t.Fatal(err)
	}
	return contractCounts{MCP: len(full.MCP), REST: len(full.Rest), GRPC: len(full.GRPC)}
}

// TestDocSurfaceCounts pins prose claims about the API surface to
// contract.json. If a route or tool is added and a doc says a stale count,
// this fails and points at the file.
func TestDocSurfaceCounts(t *testing.T) {
	cc := loadContractCounts(t)
	// Patterns of the form "N canonical MCP tools" / "N REST routes" etc.
	// Only count-bearing phrases near the word canonical/contract are checked,
	// to avoid matching incidental numbers.
	type claim struct {
		file string
		re   *regexp.Regexp
		want int
	}
	pairs := []struct {
		name string
		re   *regexp.Regexp
		want int
	}{
		{"MCP tools", regexp.MustCompile(`(\d+)\s+(?:canonical\s+)?MCP[- ]tools`), cc.MCP},
		{"MCP tools RU", regexp.MustCompile(`(\d+)\s+канонич\w*\s+MCP-инструмент\w*`), cc.MCP},
		{"REST routes", regexp.MustCompile(`(\d+)\s+REST(?:[- ]routes)?`), cc.REST},
		{"REST routes RU", regexp.MustCompile(`(\d+)\s+REST-маршрут\w*`), cc.REST},
		{"gRPC methods", regexp.MustCompile(`(\d+)\s+gRPC(?:\s+methods)?`), cc.GRPC},
		{"gRPC methods RU", regexp.MustCompile(`(\d+)\s+gRPC-метод\w*`), cc.GRPC},
	}
	files := []string{
		"../README.md",
		"../README_RU.md",
		"api-reference.md",
		"current-state.md",
		"features-guide.md",
		"api-contract.md",
	}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		text := string(raw)
		for _, p := range pairs {
			for _, m := range p.re.FindAllStringSubmatch(text, -1) {
				n, err := strconv.Atoi(m[1])
				if err != nil {
					continue
				}
				if n != p.want {
					t.Errorf("%s: claims %d %s, contract.json has %d", file, n, p.name, p.want)
				}
			}
		}
	}
}

// TestDocsInternalLinks verifies that relative markdown links in user-facing
// docs resolve to files in the repository.
func TestDocsInternalLinks(t *testing.T) {
	linkRe := regexp.MustCompile(`\[[^\]]*\]\(([^)#?]+?)(?:#[^)]*)?\)`)
	skipExt := map[string]bool{".png": true, ".svg": true, ".jpg": true, ".gif": true}
	roots := []string{".", "../README.md", "../README_RU.md", "../AGENTS.md", "../CLAUDE.md"}

	var files []string
	for _, root := range roots {
		info, err := os.Stat(root)
		if err != nil {
			t.Fatal(err)
		}
		if !info.IsDir() {
			files = append(files, root)
			continue
		}
		err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				name := d.Name()
				if name == "node_modules" || name == ".git" {
					return filepath.SkipDir
				}
				if name == "internal" {
					// docs/internal is historical context, not guaranteed
					// current — links there are not part of the contract.
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(path, ".md") {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	checked := 0
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range linkRe.FindAllStringSubmatch(string(raw), -1) {
			target := strings.TrimSpace(m[1])
			if target == "" || strings.Contains(target, "://") ||
				strings.HasPrefix(target, "mailto:") || filepath.IsAbs(target) {
				continue
			}
			if skipExt[filepath.Ext(target)] {
				continue
			}
			resolved := filepath.Join(filepath.Dir(file), target)
			if _, err := os.Stat(resolved); err != nil {
				t.Errorf("%s: broken link -> %s", file, target)
			}
			checked++
		}
	}
	if checked < 100 {
		t.Fatalf("suspiciously few links checked (%d) — link regex or file walk broken", checked)
	}
	t.Logf("checked %d internal links", checked)
}
