package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const taxonomyMaxSeedBytes = 1 << 20

type taxonomyCLIArgs struct {
	action, target, dataset, revision, collection, document string
	force                                                   bool
}

func cmdTaxonomy(args []string) {
	options, err := parseTaxonomyCLIArgs(args)
	if err != nil {
		fatalf("%v", err)
		return
	}

	path := "/datasets/" + url.PathEscape(options.dataset) + "/taxonomy"
	method := http.MethodGet
	var payload map[string]any
	switch options.action {
	case "import":
		seed, err := readTaxonomySeed(options.target)
		if err != nil {
			fatalf("taxonomy seed: %v", err)
			return
		}
		method = http.MethodPost
		path += "/import"
		payload = map[string]any{"seed": seed, "source_name": filepath.Base(options.target)}
		if options.revision != "" {
			payload["source_revision"] = options.revision
		}
	case "remove":
		method = http.MethodDelete
		payload = map[string]any{"domain": options.target, "force": options.force}
		if options.collection != "" {
			payload["collection"] = options.collection
		}
		if options.document != "" {
			payload["document"] = options.document
		}
	}

	body, status := documentRequest(method, path, payload)
	if status < 200 || status >= 300 {
		fatalf("taxonomy %s failed (%d): %s", options.action, status, body)
		return
	}
	printJSON(body)
}

func parseTaxonomyCLIArgs(args []string) (taxonomyCLIArgs, error) {
	var options taxonomyCLIArgs
	if len(args) == 0 {
		return options, fmt.Errorf("usage: levara taxonomy [import <file>|list|remove <domain>] --dataset <id>")
	}
	options.action = args[0]
	allowed := map[string]bool{"--dataset": true}
	switch options.action {
	case "import":
		allowed["--revision"] = true
	case "list":
	case "remove":
		allowed["--collection"], allowed["--document"], allowed["--force"] = true, true, true
	default:
		return options, fmt.Errorf("unknown taxonomy subcommand: %s", options.action)
	}

	var positional []string
	values := make(map[string]string)
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			positional = append(positional, arg)
			continue
		}
		name, value, hasValue := strings.Cut(arg, "=")
		if !allowed[name] {
			return options, fmt.Errorf("unknown taxonomy %s option: %s", options.action, name)
		}
		if _, present := values[name]; present {
			return options, fmt.Errorf("duplicate taxonomy option: %s", name)
		}
		if name == "--force" {
			if hasValue {
				return options, fmt.Errorf("--force does not take a value")
			}
			options.force = true
			values[name] = "true"
			continue
		}
		if !hasValue {
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return options, fmt.Errorf("%s requires a value", name)
			}
			i++
			value = args[i]
		}
		if strings.TrimSpace(value) == "" {
			return options, fmt.Errorf("%s requires a nonempty value", name)
		}
		values[name] = value
	}

	options.dataset = values["--dataset"]
	options.revision = values["--revision"]
	options.collection = values["--collection"]
	options.document = values["--document"]
	if options.dataset == "" {
		return options, fmt.Errorf("taxonomy %s requires --dataset <id>", options.action)
	}
	if options.action == "list" {
		if len(positional) != 0 {
			return options, fmt.Errorf("usage: levara taxonomy list --dataset <id>")
		}
	} else {
		if len(positional) != 1 || strings.TrimSpace(positional[0]) == "" {
			return options, fmt.Errorf("taxonomy %s requires exactly one target", options.action)
		}
		options.target = positional[0]
	}
	if options.document != "" && options.collection == "" {
		return options, fmt.Errorf("--document requires --collection")
	}
	return options, nil
}

func readTaxonomySeed(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, taxonomyMaxSeedBytes+1))
	if err != nil {
		return "", err
	}
	if len(raw) > taxonomyMaxSeedBytes {
		return "", fmt.Errorf("seed exceeds 1 MiB")
	}
	if !utf8.Valid(raw) {
		return "", fmt.Errorf("seed must be UTF-8")
	}
	return string(raw), nil
}
