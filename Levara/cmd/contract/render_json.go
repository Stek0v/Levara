package main

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/stek0v/levara/internal/contract"
)

func writeJSON(c contract.Contract, outDir string) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return atomicWrite(filepath.Join(outDir, "contract.json"), b)
}

func atomicWrite(dst string, data []byte) error {
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}
