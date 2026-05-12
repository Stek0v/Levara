package agenthosts

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Host string

const (
	HostClaude Host = "claude"
	HostCursor Host = "cursor"
	HostCodex  Host = "codex"
)

type InstallOptions struct {
	Host      Host
	Target    string
	ServerURL string
	TokenEnv  string
	DryRun    bool
	Now       time.Time
}

type InstallResult struct {
	Host       Host   `json:"host"`
	Target     string `json:"target"`
	BackupPath string `json:"backup_path,omitempty"`
	Changed    bool   `json:"changed"`
	DryRun     bool   `json:"dry_run"`
	Content    []byte `json:"-"`
}

func Install(opts InstallOptions) (InstallResult, error) {
	opts = normalizeInstallOptions(opts)
	if opts.Host == "" {
		return InstallResult{}, errors.New("host required")
	}
	if opts.Target == "" {
		opts.Target = DefaultTarget(opts.Host)
	}
	if opts.Target == "" {
		return InstallResult{}, fmt.Errorf("unsupported host %q", opts.Host)
	}

	existing, err := os.ReadFile(opts.Target)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return InstallResult{}, err
	}
	if errors.Is(err, os.ErrNotExist) {
		existing = nil
	}

	merged, err := Merge(opts.Host, existing, opts.ServerURL, opts.TokenEnv)
	if err != nil {
		return InstallResult{}, err
	}
	result := InstallResult{
		Host:    opts.Host,
		Target:  opts.Target,
		Changed: !bytes.Equal(bytes.TrimSpace(existing), bytes.TrimSpace(merged)),
		DryRun:  opts.DryRun,
		Content: merged,
	}
	if opts.DryRun || !result.Changed {
		return result, nil
	}

	if len(existing) > 0 {
		backupPath := opts.Target + ".bak-" + opts.Now.UTC().Format("20060102T150405Z")
		if err := os.WriteFile(backupPath, existing, 0644); err != nil {
			return InstallResult{}, err
		}
		result.BackupPath = backupPath
	}
	if err := os.MkdirAll(filepath.Dir(opts.Target), 0755); err != nil {
		return InstallResult{}, err
	}
	if err := os.WriteFile(opts.Target, merged, 0644); err != nil {
		return InstallResult{}, err
	}
	return result, nil
}

func Merge(host Host, existing []byte, serverURL, tokenEnv string) ([]byte, error) {
	serverURL = strings.TrimSpace(serverURL)
	if serverURL == "" {
		serverURL = "http://localhost:8080/mcp"
	}
	tokenEnv = strings.TrimSpace(tokenEnv)
	if tokenEnv == "" {
		tokenEnv = "LEVARA_TOKEN"
	}
	switch host {
	case HostClaude, HostCursor:
		return mergeMCPJSON(existing, serverURL, tokenEnv)
	case HostCodex:
		return mergeCodexTOML(existing, serverURL, tokenEnv), nil
	default:
		return nil, fmt.Errorf("unsupported host %q", host)
	}
}

func DefaultTarget(host Host) string {
	switch host {
	case HostClaude:
		return ".mcp.json"
	case HostCursor:
		return filepath.Join(".cursor", "mcp.json")
	case HostCodex:
		return filepath.Join(".codex", "config.toml")
	default:
		return ""
	}
}

func SupportedHosts() []Host {
	return []Host{HostClaude, HostCursor, HostCodex}
}

func normalizeInstallOptions(opts InstallOptions) InstallOptions {
	if opts.ServerURL == "" {
		opts.ServerURL = "http://localhost:8080/mcp"
	}
	if opts.TokenEnv == "" {
		opts.TokenEnv = "LEVARA_TOKEN"
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	return opts
}

func mergeMCPJSON(existing []byte, serverURL, tokenEnv string) ([]byte, error) {
	root := map[string]any{}
	if len(bytes.TrimSpace(existing)) > 0 {
		if err := json.Unmarshal(existing, &root); err != nil {
			return nil, err
		}
	}
	servers := map[string]any{}
	if raw, ok := root["mcpServers"].(map[string]any); ok {
		servers = raw
	}
	levara := map[string]any{}
	if raw, ok := servers["levara"].(map[string]any); ok {
		levara = raw
	}
	headers := map[string]any{}
	if raw, ok := levara["headers"].(map[string]any); ok {
		headers = raw
	}
	headers["Authorization"] = "Bearer ${" + tokenEnv + "}"
	levara["url"] = serverURL
	levara["headers"] = headers
	servers["levara"] = levara
	root["mcpServers"] = servers
	return marshalStableJSON(root)
}

func marshalStableJSON(v any) ([]byte, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func mergeCodexTOML(existing []byte, serverURL, tokenEnv string) []byte {
	lines := strings.Split(strings.ReplaceAll(string(existing), "\r\n", "\n"), "\n")
	var kept []string
	skip := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if isTOMLSection(trimmed) {
			section := strings.Trim(trimmed, "[]")
			skip = section == "mcp_servers.levara" || section == "mcp_servers.levara.headers"
		}
		if !skip {
			kept = append(kept, line)
		}
	}
	for len(kept) > 0 && strings.TrimSpace(kept[len(kept)-1]) == "" {
		kept = kept[:len(kept)-1]
	}
	if len(kept) > 0 {
		kept = append(kept, "")
	}
	kept = append(kept,
		"[mcp_servers.levara]",
		fmt.Sprintf("url = %q", serverURL),
		"",
		"[mcp_servers.levara.headers]",
		fmt.Sprintf("Authorization = %q", "Bearer ${"+tokenEnv+"}"),
	)
	return []byte(strings.Join(kept, "\n") + "\n")
}

func isTOMLSection(line string) bool {
	return strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") && !strings.HasPrefix(line, "[[")
}

func ParseHost(s string) (Host, error) {
	host := Host(strings.ToLower(strings.TrimSpace(s)))
	for _, supported := range SupportedHosts() {
		if host == supported {
			return host, nil
		}
	}
	var names []string
	for _, supported := range SupportedHosts() {
		names = append(names, string(supported))
	}
	sort.Strings(names)
	return "", fmt.Errorf("host must be one of: %s", strings.Join(names, ", "))
}
