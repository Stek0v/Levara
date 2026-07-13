// Package fetch provides URL fetching and HTML text extraction.
// Supports HTTP/HTTPS URLs, GitHub repos, and raw content URLs.
package fetch

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

const maxURLResponseBytes int64 = 10 << 20

var carrierGradeNAT = net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

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
	return fetchURLWithClient(url, safeHTTPClient(), false)
}

func safeHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, fmt.Errorf("invalid upstream address: %w", err)
			}
			ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
			if err != nil {
				return nil, fmt.Errorf("resolve %s: %w", host, err)
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("resolve %s: no addresses", host)
			}
			for _, ip := range ips {
				if !publicIP(ip) {
					return nil, fmt.Errorf("refusing non-public address for %s", host)
				}
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
		},
	}
	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return validatePublicURL(req.URL)
		},
	}
}

func publicIP(ip net.IP) bool {
	return ip != nil && ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() &&
		!ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() && !carrierGradeNAT.Contains(ip)
}

func validatePublicURL(parsed *url.URL) error {
	if parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return fmt.Errorf("only absolute HTTP(S) URLs are allowed")
	}
	if parsed.User != nil {
		return fmt.Errorf("URL credentials are not allowed")
	}
	if ip := net.ParseIP(parsed.Hostname()); ip != nil && !publicIP(ip) {
		return fmt.Errorf("refusing non-public URL address")
	}
	return nil
}

func fetchURLWithClient(rawURL string, client *http.Client, allowPrivate bool) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", fmt.Errorf("parse URL: %w", err)
	}
	if !allowPrivate {
		if err := validatePublicURL(parsed); err != nil {
			return "", err
		}
	}
	resp, err := client.Get(parsed.String())
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", parsed.Redacted(), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("fetch %s: status %d", parsed.Redacted(), resp.StatusCode)
	}
	if resp.ContentLength > maxURLResponseBytes {
		return "", fmt.Errorf("response exceeds %d bytes", maxURLResponseBytes)
	}

	ct := resp.Header.Get("Content-Type")
	body, err := readLimited(resp.Body, maxURLResponseBytes)
	if err != nil {
		return "", err
	}

	// Plain text / JSON / XML — return as-is
	if strings.Contains(ct, "text/plain") || strings.Contains(ct, "application/json") ||
		strings.Contains(ct, "text/xml") || strings.Contains(ct, "text/markdown") {
		return string(body), nil
	}

	// HTML — extract text
	return extractTextFromHTML(strings.NewReader(string(body)))
}

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	return body, nil
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
	return fetchMultipleURLs(urls, func(rawURL string) (string, error) {
		if IsGitHubURL(rawURL) {
			return FetchGitHub(rawURL)
		}
		return FetchURL(rawURL)
	})
}

func fetchMultipleURLs(urls []string, fetcher func(string) (string, error)) map[string]string {
	results := make(map[string]string)
	ch := make(chan struct{ url, text string }, len(urls))

	for _, u := range urls {
		go func(url string) {
			text, _ := fetcher(url)
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
