package main

import (
	"path/filepath"

	"github.com/stek0v/levara/internal/contract"
)

func writeMarkdown(c contract.Contract, outDir string) error {
	b, err := renderMarkdownBytes(c)
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(outDir, "api-contract.md"), b)
}
