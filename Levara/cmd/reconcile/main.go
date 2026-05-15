// reconcile — rebuild Levara indexes from a MemoryFS .md corpus.
//
// MemoryFS treats .md files as source of truth; Levara indexes are
// disposable derivatives. This CLI walks a MemoryFS corpus directory,
// parses each entry's YAML frontmatter + markdown body, and either
// prints what would be written (dry-run, default) or POSTs each entry
// to Levara's /api/v1/add so a wiped index can be repopulated without
// touching MemoryFS state.
//
// Recommended workflow:
//
//	# 1. Inspect what will be sent
//	reconcile -corpus /path/to/memoryfs -v
//
//	# 2. Apply against a fresh dataset (idempotency = wipe + reapply)
//	reconcile -corpus /path/to/memoryfs \
//	    -levara-url http://localhost:8080 \
//	    -token $LEVARA_JWT \
//	    -dataset memoryfs-reconcile \
//	    -apply
//
// Layout assumption:
//
//	<corpus>/
//	  decisions/*.md
//	  discoveries/*.md
//	  events/*.md
//	  facts/*.md
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
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
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

type config struct {
	corpus    string
	verbose   bool
	apply     bool
	levaraURL string
	token     string
	dataset   string
	typeOnly  string
	since     string
	timeout   time.Duration
}

func main() {
	cfg := parseFlags()
	if cfg.corpus == "" {
		fmt.Fprintln(os.Stderr, "usage: reconcile -corpus <dir> [-v] [-apply -levara-url URL -token JWT -dataset NAME]")
		os.Exit(2)
	}

	entries, errs := scan(cfg.corpus)
	entries = filterEntries(entries, cfg)

	if cfg.verbose {
		for _, e := range entries {
			fmt.Printf("%s  type=%q slug=%q created=%s status=%s\n",
				relPath(e.Path, cfg.corpus), e.Type, e.Slug, e.Created, e.Status)
		}
	}
	for _, err := range errs {
		fmt.Fprintf(os.Stderr, "warn: %v\n", err)
	}

	if !cfg.apply {
		fmt.Printf("scanned: %d entries, %d errors (dry-run, no writes)\n", len(entries), len(errs))
		return
	}

	written, failed := writeAll(cfg, entries)
	fmt.Printf("applied: %d/%d entries to %s (dataset=%s), %d parse errors, %d write failures\n",
		written, len(entries), cfg.levaraURL, cfg.dataset, len(errs), failed)
	if failed > 0 {
		os.Exit(1)
	}
}

func parseFlags() config {
	var c config
	flag.StringVar(&c.corpus, "corpus", "", "path to MemoryFS .md corpus root")
	flag.BoolVar(&c.verbose, "v", false, "print each parsed entry")
	flag.BoolVar(&c.apply, "apply", false, "POST entries to Levara (default: dry-run)")
	flag.StringVar(&c.levaraURL, "levara-url", "http://localhost:8080", "Levara HTTP base URL")
	flag.StringVar(&c.token, "token", os.Getenv("LEVARA_JWT"), "JWT bearer (default: $LEVARA_JWT)")
	flag.StringVar(&c.dataset, "dataset", "memoryfs-reconcile", "target dataset name")
	flag.StringVar(&c.typeOnly, "type", "", "filter: only entries with this frontmatter type")
	flag.StringVar(&c.since, "since", "", "filter: only entries with created >= YYYY-MM-DD")
	flag.DurationVar(&c.timeout, "timeout", 30*time.Second, "per-request timeout")
	flag.Parse()
	return c
}

func filterEntries(in []entry, cfg config) []entry {
	if cfg.typeOnly == "" && cfg.since == "" {
		return in
	}
	out := in[:0]
	for _, e := range in {
		if cfg.typeOnly != "" && e.Type != cfg.typeOnly {
			continue
		}
		if cfg.since != "" && e.Created < cfg.since {
			continue
		}
		out = append(out, e)
	}
	return out
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
	body = strings.TrimPrefix(body, "\n---\r\n")
	body = strings.TrimPrefix(body, "\n---\n")
	body = strings.TrimPrefix(body, "\n")
	return []byte(front), []byte(body), true
}

func writeAll(cfg config, entries []entry) (written, failed int) {
	client := &http.Client{Timeout: cfg.timeout}
	url := strings.TrimRight(cfg.levaraURL, "/") + "/api/v1/add"
	for _, e := range entries {
		if err := postEntry(client, url, cfg, e); err != nil {
			fmt.Fprintf(os.Stderr, "write %s: %v\n", relPath(e.Path, cfg.corpus), err)
			failed++
			continue
		}
		written++
		if cfg.verbose {
			fmt.Printf("ok  %s\n", relPath(e.Path, cfg.corpus))
		}
	}
	return
}

type addPayload struct {
	Data        string   `json:"data"`
	DatasetName string   `json:"dataset_name"`
	Tags        []string `json:"tags"`
}

func postEntry(client *http.Client, url string, cfg config, e entry) error {
	body := assembleBody(e)
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("empty body")
	}
	tags := []string{"reconcile"}
	if e.Type != "" {
		tags = append(tags, e.Type)
	}
	if e.Slug != "" {
		tags = append(tags, "slug:"+e.Slug)
	}
	if e.Status != "" {
		tags = append(tags, "status:"+e.Status)
	}
	payload := addPayload{Data: body, DatasetName: cfg.dataset, Tags: tags}
	buf, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", url, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	io.Copy(io.Discard, resp.Body)
	return nil
}

// assembleBody re-injects critical frontmatter fields into the body so
// the indexed text remains recall-friendly even after the YAML block is
// stripped. Type/slug/description carry the strongest semantic signal.
func assembleBody(e entry) string {
	var b strings.Builder
	if e.Slug != "" {
		fmt.Fprintf(&b, "# %s\n\n", e.Slug)
	}
	if e.Description != "" {
		fmt.Fprintf(&b, "%s\n\n", e.Description)
	}
	if e.Type != "" || e.Created != "" || e.Status != "" {
		fmt.Fprintf(&b, "(type=%s created=%s status=%s)\n\n", e.Type, e.Created, e.Status)
	}
	b.WriteString(strings.TrimSpace(e.Body))
	return b.String()
}
