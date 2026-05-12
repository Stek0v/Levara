package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/stek0v/levara/pkg/agenthosts"
)

func main() {
	hostFlag := flag.String("host", "", "Host to install: claude, cursor, or codex")
	target := flag.String("target", "", "Config path. Defaults: .mcp.json, .cursor/mcp.json, .codex/config.toml")
	serverURL := flag.String("server-url", "http://localhost:8080/mcp", "Levara MCP endpoint")
	tokenEnv := flag.String("token-env", "LEVARA_TOKEN", "Environment variable referenced in Authorization header")
	dryRun := flag.Bool("dry-run", false, "Print merged config to stdout without writing")
	flag.Parse()

	host, err := agenthosts.ParseHost(*hostFlag)
	if err != nil {
		log.Fatal(err)
	}
	result, err := agenthosts.Install(agenthosts.InstallOptions{
		Host:      host,
		Target:    *target,
		ServerURL: *serverURL,
		TokenEnv:  *tokenEnv,
		DryRun:    *dryRun,
	})
	if err != nil {
		log.Fatal(err)
	}
	if *dryRun {
		_, _ = os.Stdout.Write(result.Content)
		return
	}
	if result.Changed {
		fmt.Printf("installed Levara MCP config for %s at %s\n", result.Host, result.Target)
		if result.BackupPath != "" {
			fmt.Printf("backup written to %s\n", result.BackupPath)
		}
	} else {
		fmt.Printf("Levara MCP config for %s already up to date at %s\n", result.Host, result.Target)
	}
}
