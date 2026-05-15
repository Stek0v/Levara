// reconcile — rebuild Levara indexes from a MemoryFS .md corpus.
//
// MemoryFS treats .md files as source of truth; Levara indexes are
// disposable derivatives. This CLI walks a MemoryFS corpus directory,
// parses each entry's YAML frontmatter + markdown body, and re-issues
// the inserts against Levara so a wiped index can be repopulated
// without touching MemoryFS state.
//
// Phase 1 (this commit) is dry-run only: scan + parse + print. The
// HTTP/gRPC writer lands in a follow-up so the parser can stabilise
// without entangling the network path.
//
// Layout assumption (matches the current corpus shipped via Claude
// Code memory):
//
//	<corpus>/
//	  decisions/*.md
//	  discoveries/*.md
//	  events/*.md
//	  facts/*.md
//	  ...
//
// Frontmatter (YAML between leading `---` fences):
//
//	type: fact
//	slug: keenetic-ssh-access
//	description: one-liner
//	created: 2026-05-11
//	status: active
package main

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type entry struct {
	Path        string
	Type        string
	Slug        string
	Description string
	Created     string
	Status      string
	Body        string
}

func main() {
	corpus := flag.String("corpus", "", "path to MemoryFS .md corpus root")
	verbose := flag.Bool("v", false, "print each parsed entry")
	flag.Parse()

	if *corpus == "" {
		fmt.Fprintln(os.Stderr, "usage: reconcile -corpus <dir> [-v]")
		os.Exit(2)
	}

	entries, errs := scan(*corpus)
	if *verbose {
		for _, e := range entries {
			fmt.Printf("%s  type=%q slug=%q created=%s status=%s\n",
				relPath(e.Path, *corpus), e.Type, e.Slug, e.Created, e.Status)
		}
	}
	for _, err := range errs {
		fmt.Fprintf(os.Stderr, "warn: %v\n", err)
	}
	fmt.Printf("scanned: %d entries, %d errors (dry-run, no writes)\n", len(entries), len(errs))
}

func relPath(p, root string) string {
	r, err := filepath.Rel(root, p)
	if err != nil {
		return p
	}
	return r
}

func scan(root string) ([]entry, []error) {
	var out []entry
	var errs []error
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			errs = append(errs, fmt.Errorf("walk %s: %w", path, walkErr))
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		if strings.EqualFold(d.Name(), "INDEX.md") || strings.EqualFold(d.Name(), "MEMORY.md") {
			return nil
		}
		e, err := parseFile(path)
		if err != nil {
			errs = append(errs, fmt.Errorf("parse %s: %w", path, err))
			return nil
		}
		out = append(out, e)
		return nil
	})
	if err != nil {
		errs = append(errs, err)
	}
	return out, errs
}

func parseFile(path string) (entry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return entry{}, err
	}
	e := entry{Path: path}
	front, body, ok := splitFrontmatter(raw)
	if !ok {
		// No frontmatter is fine — body-only files still index.
		e.Body = string(raw)
		return e, nil
	}
	parseFrontmatter(front, &e)
	e.Body = string(body)
	return e, nil
}

// parseFrontmatter reads MemoryFS-flavoured `key: value` lines. Frontmatter
// is never nested (the schema is fixed), so a line-based parser avoids the
// YAML colon-in-value trap (`description: Foo: bar` is a real corpus
// pattern that breaks strict YAML parsers).
func parseFrontmatter(front []byte, e *entry) {
	for _, line := range strings.Split(string(front), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.Index(line, ":")
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		val = strings.TrimSuffix(strings.TrimPrefix(val, `"`), `"`)
		switch key {
		case "type":
			e.Type = val
		case "slug":
			e.Slug = val
		case "description":
			e.Description = val
		case "created":
			e.Created = val
		case "status":
			e.Status = val
		}
	}
}

// splitFrontmatter peels a leading `---\n…\n---\n` YAML block off the
// document. Returns (frontmatter, body, found). When found=false the
// whole input is the body.
func splitFrontmatter(raw []byte) ([]byte, []byte, bool) {
	s := string(raw)
	if !strings.HasPrefix(s, "---\n") && !strings.HasPrefix(s, "---\r\n") {
		return nil, raw, false
	}
	// Trim the opening fence and search for the closing one.
	tail := strings.TrimPrefix(strings.TrimPrefix(s, "---\r\n"), "---\n")
	end := strings.Index(tail, "\n---\n")
	if end < 0 {
		end = strings.Index(tail, "\n---\r\n")
	}
	if end < 0 {
		return nil, raw, false
	}
	front := tail[:end]
	body := tail[end:]
	// Skip the closing fence + newline.
	body = strings.TrimPrefix(body, "\n---\r\n")
	body = strings.TrimPrefix(body, "\n---\n")
	body = strings.TrimPrefix(body, "\n")
	return []byte(front), []byte(body), true
}
