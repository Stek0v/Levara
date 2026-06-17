package extract

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func extractImageTesseractCLI(data []byte, filename string) (string, error) {
	bin := strings.TrimSpace(os.Getenv("TESSERACT_BINARY"))
	if bin == "" {
		bin = "tesseract"
	}
	if _, err := exec.LookPath(bin); err != nil {
		return "", fmt.Errorf("tesseract OCR binary %q not found: install Tesseract or set TESSERACT_BINARY: %w", bin, err)
	}

	ext := filepath.Ext(filename)
	if ext == "" {
		ext = ".png"
	}
	tmp, err := os.CreateTemp("", "levara-ocr-*"+ext)
	if err != nil {
		return "", fmt.Errorf("tesseract temp file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("tesseract temp write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("tesseract temp close: %w", err)
	}

	args := []string{tmp.Name(), "stdout"}
	if langs := tesseractLanguages(); len(langs) > 0 {
		args = append(args, "-l", strings.Join(langs, "+"))
	}
	if psm := strings.TrimSpace(os.Getenv("TESSERACT_PSM")); psm != "" {
		args = append(args, "--psm", psm)
	}
	if oem := strings.TrimSpace(os.Getenv("TESSERACT_OEM")); oem != "" {
		args = append(args, "--oem", oem)
	}

	ctx, cancel := context.WithTimeout(context.Background(), tesseractTimeout())
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("tesseract OCR timed out after %s", tesseractTimeout())
	}
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("tesseract OCR: %s", msg)
	}
	return strings.TrimSpace(string(out)), nil
}

func tesseractTimeout() time.Duration {
	raw := strings.TrimSpace(os.Getenv("TESSERACT_TIMEOUT_SECONDS"))
	if raw == "" {
		return 2 * time.Minute
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return 2 * time.Minute
	}
	return time.Duration(seconds) * time.Second
}
