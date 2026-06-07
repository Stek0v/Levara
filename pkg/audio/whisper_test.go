package audio

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// T-9b smoke for the Whisper-compatible transcription client.

func TestIsSupported(t *testing.T) {
	cases := []struct {
		filename string
		want     bool
	}{
		{"talk.mp3", true},
		{"voice.WAV", true}, // case-insensitive
		{"clip.m4a", true},
		{"audio.ogg", true},
		{"music.flac", true},
		{"video.webm", true},
		{"video.mp4", true},
		{"document.txt", false},
		{"image.png", false},
		{"noext", false},
	}
	for _, c := range cases {
		if got := IsSupported(c.filename); got != c.want {
			t.Errorf("IsSupported(%q) = %v, want %v", c.filename, got, c.want)
		}
	}
}

func TestTranscribe_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Validate request shape: multipart, model field, response_format=json
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
		}
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			t.Errorf("ParseMultipartForm: %v", err)
		}
		if got := r.FormValue("model"); got != "whisper-1" {
			t.Errorf("model = %q, want whisper-1", got)
		}
		if got := r.FormValue("response_format"); got != "json" {
			t.Errorf("response_format = %q, want json", got)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"text":"hello world"}`)
	}))
	defer srv.Close()

	c := NewWhisperClient(srv.URL, "", "")
	got, err := c.Transcribe(context.Background(), []byte("fake audio"), "test.wav")
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello world" {
		t.Errorf("got %q, want hello world", got)
	}
}

func TestTranscribe_BearerAuthSent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk-secret" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"text":"ok"}`)
	}))
	defer srv.Close()

	c := NewWhisperClient(srv.URL, "sk-secret", "whisper-1")
	if _, err := c.Transcribe(context.Background(), []byte("x"), "t.mp3"); err != nil {
		t.Fatal(err)
	}
}

func TestTranscribe_PlainTextFallback(t *testing.T) {
	// Some local Whisper servers return text directly, not JSON. Client falls
	// back to using the raw body as transcription.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "  raw transcript  ")
	}))
	defer srv.Close()

	c := NewWhisperClient(srv.URL, "", "")
	got, err := c.Transcribe(context.Background(), []byte("x"), "t.mp3")
	if err != nil {
		t.Fatal(err)
	}
	if got != "raw transcript" {
		t.Errorf("got %q, want trimmed 'raw transcript'", got)
	}
}

func TestTranscribe_EmptyData(t *testing.T) {
	c := NewWhisperClient("http://unused", "", "")
	_, err := c.Transcribe(context.Background(), nil, "t.mp3")
	if err == nil || !strings.Contains(err.Error(), "empty audio data") {
		t.Errorf("want 'empty audio data' err, got %v", err)
	}
}

func TestTranscribe_UnsupportedFormat(t *testing.T) {
	c := NewWhisperClient("http://unused", "", "")
	_, err := c.Transcribe(context.Background(), []byte("x"), "doc.txt")
	if err == nil || !strings.Contains(err.Error(), "unsupported audio format") {
		t.Errorf("want 'unsupported' err, got %v", err)
	}
}

func TestTranscribe_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "broken", http.StatusBadGateway)
	}))
	defer srv.Close()

	c := NewWhisperClient(srv.URL, "", "")
	_, err := c.Transcribe(context.Background(), []byte("x"), "t.mp3")
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Errorf("want 502 in err, got %v", err)
	}
}

func TestNewWhisperClient_DefaultModel(t *testing.T) {
	c := NewWhisperClient("http://x", "", "")
	if c.model != "whisper-1" {
		t.Errorf("default model = %q, want whisper-1", c.model)
	}
}
