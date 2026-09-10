package http

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gofiber/fiber/v2"
)

func TestUploadMultipartKeepsLargePartsInMemory(t *testing.T) {
	// A temp directory that cannot be used proves parsing never needs a file,
	// even above fasthttp's ordinary 16 MiB spill threshold.
	dir := t.TempDir()
	blocked := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocked, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", blocked)
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("data", "large.txt")
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.Repeat([]byte("private"), (17<<20)/7)
	if _, err := part.Write(want); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteField("datasetName", "Большой документ"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	app := fiber.New(fiber.Config{BodyLimit: 20 << 20, DisablePreParseMultipartForm: true})
	app.Post("/upload", func(c *fiber.Ctx) error {
		form, err := uploadMultipartForm(c)
		if err != nil {
			return err
		}
		defer form.RemoveAll()
		if form.Value["datasetName"][0] != "Большой документ" {
			t.Fatal("form field lost")
		}
		file, err := form.File["data"][0].Open()
		if err != nil {
			return err
		}
		defer file.Close()
		if _, disk := file.(*os.File); disk {
			t.Fatal("plaintext part stored on disk")
		}
		got, err := io.ReadAll(file)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("multipart bytes mismatch %v", err)
		}
		return c.SendStatus(204)
	})
	req := httptest.NewRequest("POST", "/upload", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := app.Test(req, 10000)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 204 {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("upload %d %s", resp.StatusCode, raw)
	}
}
