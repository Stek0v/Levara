// Package fetch provides URL fetching and HTML text extraction.
// Supports HTTP/HTTPS URLs, GitHub repos, and raw content URLs.
package fetch

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// IsURL returns true if the string looks like an HTTP(S) URL.
func IsURL(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// IsGitHubURL returns true if the URL points to GitHub.
func IsGitHubURL(s string) bool {
	return strings.Contains(s, "github.com/")
}

// FetchURL fetches a URL and extracts readable text from HTML.
func FetchURL(url string) (string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("fetch %s: status %d", url, resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")

	// Plain text / JSON / XML — return as-is
	if strings.Contains(ct, "text/plain") || strings.Contains(ct, "application/json") ||
		strings.Contains(ct, "text/xml") || strings.Contains(ct, "text/markdown") {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", err
		}
		return string(body), nil
	}

	// HTML — extract text
	return extractTextFromHTML(resp.Body)
}

// FetchGitHub fetches a GitHub URL. Converts to raw URL if needed.
func FetchGitHub(url string) (string, error) {
	// Convert github.com/user/repo to raw README
	if strings.HasPrefix(url, "https://github.com/") && !strings.Contains(url, "/raw/") {
		parts := strings.Split(strings.TrimPrefix(url, "https://github.com/"), "/")
		if len(parts) >= 2 {
			// Try raw README.md
			rawURL := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/main/README.md", parts[0], parts[1])
			text, err := FetchURL(rawURL)
			if err == nil && len(text) > 50 {
				return text, nil
			}
			// Fallback: try master branch
			rawURL = fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/master/README.md", parts[0], parts[1])
			text, err = FetchURL(rawURL)
			if err == nil && len(text) > 50 {
				return text, nil
			}
		}
	}
	// Fallback: fetch as regular URL
	return FetchURL(url)
}

// extractTextFromHTML parses HTML and extracts readable text content.
func extractTextFromHTML(r io.Reader) (string, error) {
	doc, err := goquery.NewDocumentFromReader(r)
	if err != nil {
		return "", fmt.Errorf("parse HTML: %w", err)
	}

	// Remove non-content elements
	doc.Find("script, style, nav, footer, header, aside, iframe, noscript").Remove()

	var parts []string

	// Priority selectors: article > main > body
	var content *goquery.Selection
	if article := doc.Find("article"); article.Length() > 0 {
		content = article
	} else if main := doc.Find("main"); main.Length() > 0 {
		content = main
	} else {
		content = doc.Find("body")
	}

	// Extract text from content elements
	content.Find("h1, h2, h3, h4, h5, h6, p, li, pre, code, td, th, blockquote, figcaption").Each(func(_ int, s *goquery.Selection) {
		text := strings.TrimSpace(s.Text())
		if text != "" {
			// Add markdown-style headers
			tag := goquery.NodeName(s)
			switch tag {
			case "h1":
				parts = append(parts, "# "+text)
			case "h2":
				parts = append(parts, "## "+text)
			case "h3":
				parts = append(parts, "### "+text)
			case "h4", "h5", "h6":
				parts = append(parts, "#### "+text)
			default:
				parts = append(parts, text)
			}
		}
	})

	if len(parts) == 0 {
		// Fallback: just get all body text
		return strings.TrimSpace(content.Text()), nil
	}

	return strings.Join(parts, "\n\n"), nil
}

// FetchMultipleURLs fetches multiple URLs concurrently.
func FetchMultipleURLs(urls []string) map[string]string {
	results := make(map[string]string)
	ch := make(chan struct{ url, text string }, len(urls))

	for _, u := range urls {
		go func(url string) {
			var text string
			if IsGitHubURL(url) {
				text, _ = FetchGitHub(url)
			} else {
				text, _ = FetchURL(url)
			}
			ch <- struct{ url, text string }{url, text}
		}(u)
	}

	for range urls {
		r := <-ch
		if r.text != "" {
			results[r.url] = r.text
		}
	}
	return results
}
