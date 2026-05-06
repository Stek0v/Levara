package fileio

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
)

type FileHashResult struct {
	FilePath string
	SHA256   string
	FileSize int64
	MimeType string
	Error    string
}

func HashFiles(paths []string, maxConcurrent int) []FileHashResult {
	if maxConcurrent <= 0 {
		maxConcurrent = 8
	}

	results := make([]FileHashResult, len(paths))
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup

	for i, path := range paths {
		wg.Add(1)
		go func(idx int, filePath string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			results[idx] = hashFile(filePath)
		}(i, path)
	}

	wg.Wait()
	return results
}

func hashFile(path string) FileHashResult {
	result := FileHashResult{FilePath: path}

	f, err := os.Open(path)
	if err != nil {
		result.Error = fmt.Sprintf("open: %v", err)
		return result
	}
	defer f.Close()

	// File size
	info, err := f.Stat()
	if err != nil {
		result.Error = fmt.Sprintf("stat: %v", err)
		return result
	}
	result.FileSize = info.Size()

	// SHA256
	h := sha256.New()
	buf := make([]byte, 32*1024) // 32KB buffer
	if _, err := io.CopyBuffer(h, f, buf); err != nil {
		result.Error = fmt.Sprintf("hash: %v", err)
		return result
	}
	result.SHA256 = hex.EncodeToString(h.Sum(nil))

	// MIME type (read first 512 bytes)
	f.Seek(0, io.SeekStart)
	header := make([]byte, 512)
	n, _ := f.Read(header)
	if n > 0 {
		result.MimeType = http.DetectContentType(header[:n])
	}

	return result
}
