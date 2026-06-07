// Package audio provides audio transcription via Whisper API (OpenAI-compatible).
// Works with: OpenAI Whisper API, local whisper.cpp server, Ollama (future).
package audio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// WhisperClient transcribes audio files via Whisper API (OpenAI-compatible).
type WhisperClient struct {
	endpoint string // e.g. http://localhost:9002/v1/audio/transcriptions
	apiKey   string // optional for local servers
	model    string // "whisper-1" for OpenAI, "base" for local
	client   *http.Client
}

// NewWhisperClient creates a client for Whisper-compatible transcription API.
func NewWhisperClient(endpoint, apiKey, model string) *WhisperClient {
	if model == "" {
		model = "whisper-1"
	}
	return &WhisperClient{
		endpoint: endpoint,
		apiKey:   apiKey,
		model:    model,
		client: &http.Client{
			Timeout: 5 * time.Minute, // audio transcription can be slow for long files
		},
	}
}

// whisperJSON is the response format when response_format=json.
type whisperJSON struct {
	Text string `json:"text"`
}

// Transcribe sends audio file to Whisper API and returns transcribed text.
// Supports: mp3, mp4, mpeg, mpga, m4a, wav, webm, ogg, flac.
func (w *WhisperClient) Transcribe(ctx context.Context, audioData []byte, filename string) (string, error) {
	if len(audioData) == 0 {
		return "", fmt.Errorf("empty audio data")
	}
	if !IsSupported(filename) {
		return "", fmt.Errorf("unsupported audio format: %s", filepath.Ext(filename))
	}

	// Build multipart form
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	// file field
	part, err := writer.CreateFormFile("file", filepath.Base(filename))
	if err != nil {
		return "", fmt.Errorf("create form file: %w", err)
	}
	if _, err := part.Write(audioData); err != nil {
		return "", fmt.Errorf("write audio data: %w", err)
	}

	// model field
	if err := writer.WriteField("model", w.model); err != nil {
		return "", fmt.Errorf("write model field: %w", err)
	}

	// response_format=json for structured parsing
	if err := writer.WriteField("response_format", "json"); err != nil {
		return "", fmt.Errorf("write response_format field: %w", err)
	}

	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("close multipart: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.endpoint, &body)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if w.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+w.apiKey)
	}

	resp, err := w.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("whisper request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("whisper API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	// Parse JSON response
	var result whisperJSON
	if err := json.Unmarshal(respBody, &result); err != nil {
		// Fallback: treat response as plain text (some servers return text directly)
		return strings.TrimSpace(string(respBody)), nil
	}

	return strings.TrimSpace(result.Text), nil
}

// TranscribeFile reads file from path and transcribes.
func (w *WhisperClient) TranscribeFile(ctx context.Context, filePath string) (string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("read audio file: %w", err)
	}
	return w.Transcribe(ctx, data, filepath.Base(filePath))
}

// supportedExts lists audio formats supported by Whisper API.
var supportedExts = map[string]bool{
	".mp3":  true,
	".mp4":  true,
	".mpeg": true,
	".mpga": true,
	".m4a":  true,
	".wav":  true,
	".webm": true,
	".ogg":  true,
	".flac": true,
}

// IsSupported checks if file extension is a supported audio format.
func IsSupported(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	return supportedExts[ext]
}
