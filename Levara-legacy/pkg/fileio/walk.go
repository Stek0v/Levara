package fileio

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

func ListDirectory(rootPath string, recursive bool, extensions []string) []string {
	extSet := make(map[string]bool, len(extensions))
	for _, ext := range extensions {
		ext = strings.ToLower(ext)
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		extSet[ext] = true
	}

	if !recursive {
		return listFlat(rootPath, extSet)
	}
	return listRecursive(rootPath, extSet)
}

func listFlat(rootPath string, extSet map[string]bool) []string {
	entries, err := os.ReadDir(rootPath)
	if err != nil {
		return nil
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if matchExt(e.Name(), extSet) {
			files = append(files, filepath.Join(rootPath, e.Name()))
		}
	}
	return files
}

func listRecursive(rootPath string, extSet map[string]bool) []string {
	var mu sync.Mutex
	var files []string
	var wg sync.WaitGroup

	// Use bounded goroutine pool for parallel directory traversal
	sem := make(chan struct{}, 16)

	var walk func(dir string)
	walk = func(dir string) {
		defer wg.Done()
		sem <- struct{}{}
		defer func() { <-sem }()

		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}

		var localFiles []string
		for _, e := range entries {
			fullPath := filepath.Join(dir, e.Name())
			if e.IsDir() {
				wg.Add(1)
				go walk(fullPath)
			} else if matchExt(e.Name(), extSet) {
				localFiles = append(localFiles, fullPath)
			}
		}

		if len(localFiles) > 0 {
			mu.Lock()
			files = append(files, localFiles...)
			mu.Unlock()
		}
	}

	wg.Add(1)
	go walk(rootPath)
	wg.Wait()

	return files
}

func matchExt(name string, extSet map[string]bool) bool {
	if len(extSet) == 0 {
		return true // no filter
	}
	return extSet[strings.ToLower(filepath.Ext(name))]
}
