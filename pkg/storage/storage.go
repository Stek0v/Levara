// Package storage provides a unified file storage interface with local filesystem
// and S3-compatible backends.
//
// Usage:
//
//	// Local (default):
//	fs := storage.NewLocalStorage("/data/uploads")
//	fs.Save(ctx, "docs/file.pdf", reader)
//
//	// S3:
//	s3 := storage.NewS3Storage("my-bucket", "us-east-1", "", accessKey, secretKey)
//	s3.Save(ctx, "docs/file.pdf", reader)
//
// Set STORAGE_BACKEND=s3 with S3_BUCKET, S3_REGION, S3_ENDPOINT, AWS_ACCESS_KEY_ID,
// AWS_SECRET_ACCESS_KEY env vars to use S3.
package storage

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Storage is the file storage interface.
// All paths are relative keys (e.g. "datasets/abc/file.txt").
type Storage interface {
	Save(ctx context.Context, path string, data io.Reader) error
	Load(ctx context.Context, path string) (io.ReadCloser, error)
	Delete(ctx context.Context, path string) error
	List(ctx context.Context, prefix string) ([]string, error)
	Exists(ctx context.Context, path string) (bool, error)
}

// Presigner is an optional extension for storage backends that can issue
// time-limited direct download URLs (for example S3-compatible backends).
type Presigner interface {
	PresignGet(ctx context.Context, path string, ttl time.Duration) (string, error)
}

// ---------------------------------------------------------------------------
// LocalStorage — filesystem backend
// ---------------------------------------------------------------------------

// LocalStorage stores files on the local filesystem under basePath.
type LocalStorage struct {
	basePath string
}

// NewLocalStorage creates a local filesystem storage rooted at basePath.
// The directory is created if it does not exist.
func NewLocalStorage(basePath string) *LocalStorage {
	os.MkdirAll(basePath, 0755)
	return &LocalStorage{basePath: basePath}
}

func (s *LocalStorage) fullPath(key string) string {
	return filepath.Join(s.basePath, filepath.Clean(key))
}

// Save writes data to the given path, creating parent directories as needed.
func (s *LocalStorage) Save(_ context.Context, path string, data io.Reader) error {
	fp := s.fullPath(path)
	if err := os.MkdirAll(filepath.Dir(fp), 0755); err != nil {
		return fmt.Errorf("storage: mkdir %s: %w", filepath.Dir(fp), err)
	}

	f, err := os.Create(fp)
	if err != nil {
		return fmt.Errorf("storage: create %s: %w", fp, err)
	}
	defer f.Close()

	if _, err := io.Copy(f, data); err != nil {
		return fmt.Errorf("storage: write %s: %w", fp, err)
	}
	return f.Sync()
}

// Load opens the file at path for reading.
func (s *LocalStorage) Load(_ context.Context, path string) (io.ReadCloser, error) {
	f, err := os.Open(s.fullPath(path))
	if err != nil {
		return nil, fmt.Errorf("storage: open %s: %w", path, err)
	}
	return f, nil
}

// Delete removes the file at path.
func (s *LocalStorage) Delete(_ context.Context, path string) error {
	err := os.Remove(s.fullPath(path))
	if os.IsNotExist(err) {
		return nil // idempotent
	}
	return err
}

// List returns all relative paths under the given prefix.
func (s *LocalStorage) List(_ context.Context, prefix string) ([]string, error) {
	root := s.fullPath(prefix)
	var paths []string

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !info.IsDir() {
			rel, _ := filepath.Rel(s.basePath, path)
			paths = append(paths, rel)
		}
		return nil
	})
	if os.IsNotExist(err) {
		return nil, nil
	}
	return paths, err
}

// Exists checks whether a file exists at path.
func (s *LocalStorage) Exists(_ context.Context, path string) (bool, error) {
	_, err := os.Stat(s.fullPath(path))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// ---------------------------------------------------------------------------
// S3Storage — AWS S3 / MinIO / DigitalOcean Spaces (AWS Signature V4)
// ---------------------------------------------------------------------------

// S3Storage uses an S3-compatible object store with AWS Signature V4 signing.
// No AWS SDK dependency — all requests are signed via minimal HMAC-SHA256 implementation.
type S3Storage struct {
	bucket    string
	region    string
	endpoint  string // custom endpoint for MinIO / DO Spaces / LocalStack
	accessKey string
	secretKey string
	client    *http.Client
}

// NewS3Storage creates an S3 storage backend with AWS Sig V4 authentication.
func NewS3Storage(bucket, region, endpoint, accessKey, secretKey string) (*S3Storage, error) {
	if bucket == "" {
		return nil, fmt.Errorf("storage: S3 bucket name required")
	}
	if region == "" {
		region = "us-east-1"
	}
	if endpoint == "" {
		endpoint = fmt.Sprintf("https://s3.%s.amazonaws.com", region)
	}
	endpoint = strings.TrimSuffix(endpoint, "/")

	return &S3Storage{
		bucket:    bucket,
		region:    region,
		endpoint:  endpoint,
		accessKey: accessKey,
		secretKey: secretKey,
		client:    &http.Client{Timeout: 60 * time.Second},
	}, nil
}

// Save uploads data to S3 via PUT.
func (s *S3Storage) Save(ctx context.Context, path string, data io.Reader) error {
	body, err := io.ReadAll(data)
	if err != nil {
		return fmt.Errorf("storage/s3: read data: %w", err)
	}

	payloadHash := sha256Hex(body)
	reqURL := s.objectURL(path)

	req, err := http.NewRequestWithContext(ctx, "PUT", reqURL, strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("storage/s3: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	s.signRequest(req, payloadHash)

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("storage/s3: PUT %s: %w", path, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 300 {
		return fmt.Errorf("storage/s3: PUT %s status %d", path, resp.StatusCode)
	}
	return nil
}

// Load downloads an object from S3 via GET.
func (s *S3Storage) Load(ctx context.Context, path string) (io.ReadCloser, error) {
	reqURL := s.objectURL(path)
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("storage/s3: create request: %w", err)
	}

	s.signRequest(req, "UNSIGNED-PAYLOAD")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("storage/s3: GET %s: %w", path, err)
	}

	if resp.StatusCode == 404 {
		resp.Body.Close()
		return nil, fmt.Errorf("storage/s3: %s not found", path)
	}
	if resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, fmt.Errorf("storage/s3: GET %s status %d", path, resp.StatusCode)
	}
	return resp.Body, nil
}

// Delete removes an object from S3 via DELETE.
func (s *S3Storage) Delete(ctx context.Context, path string) error {
	reqURL := s.objectURL(path)
	req, err := http.NewRequestWithContext(ctx, "DELETE", reqURL, nil)
	if err != nil {
		return fmt.Errorf("storage/s3: create request: %w", err)
	}

	s.signRequest(req, "UNSIGNED-PAYLOAD")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("storage/s3: DELETE %s: %w", path, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	// S3 returns 204 on successful delete; 404 is also acceptable (idempotent)
	if resp.StatusCode >= 300 && resp.StatusCode != 404 {
		return fmt.Errorf("storage/s3: DELETE %s status %d", path, resp.StatusCode)
	}
	return nil
}

// listBucketResult is the XML response for S3 ListObjectsV2.
type listBucketResult struct {
	XMLName  xml.Name `xml:"ListBucketResult"`
	Contents []struct {
		Key string `xml:"Key"`
	} `xml:"Contents"`
	IsTruncated   bool   `xml:"IsTruncated"`
	NextContToken string `xml:"NextContinuationToken"`
}

// List returns all keys under the given prefix using ListObjectsV2.
func (s *S3Storage) List(ctx context.Context, prefix string) ([]string, error) {
	var allKeys []string
	contToken := ""

	for {
		params := url.Values{}
		params.Set("list-type", "2")
		params.Set("prefix", prefix)
		if contToken != "" {
			params.Set("continuation-token", contToken)
		}

		reqURL := s.bucketURL() + "?" + params.Encode()
		req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
		if err != nil {
			return nil, fmt.Errorf("storage/s3: create list request: %w", err)
		}

		s.signRequest(req, "UNSIGNED-PAYLOAD")

		resp, err := s.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("storage/s3: LIST prefix=%s: %w", prefix, err)
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 300 {
			return nil, fmt.Errorf("storage/s3: LIST prefix=%s status %d", prefix, resp.StatusCode)
		}

		var result listBucketResult
		if err := xml.NewDecoder(resp.Body).Decode(&result); err != nil {
			return nil, fmt.Errorf("storage/s3: parse list response: %w", err)
		}

		for _, obj := range result.Contents {
			allKeys = append(allKeys, obj.Key)
		}

		if !result.IsTruncated {
			break
		}
		contToken = result.NextContToken
	}

	return allKeys, nil
}

// Exists checks whether an object exists via HEAD.
func (s *S3Storage) Exists(ctx context.Context, path string) (bool, error) {
	reqURL := s.objectURL(path)
	req, err := http.NewRequestWithContext(ctx, "HEAD", reqURL, nil)
	if err != nil {
		return false, fmt.Errorf("storage/s3: create request: %w", err)
	}

	s.signRequest(req, "UNSIGNED-PAYLOAD")

	resp, err := s.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("storage/s3: HEAD %s: %w", path, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode == 200 {
		return true, nil
	}
	if resp.StatusCode == 404 {
		return false, nil
	}
	return false, fmt.Errorf("storage/s3: HEAD %s status %d", path, resp.StatusCode)
}

// PresignGet returns a SigV4 query-signed URL for direct object download.
// ttl is clamped to AWS limits [1s, 7d], defaulting to 15m when zero/negative.
func (s *S3Storage) PresignGet(_ context.Context, path string, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	expires := int(ttl.Seconds())
	if expires < 1 {
		expires = 1
	}
	if expires > 7*24*60*60 {
		expires = 7 * 24 * 60 * 60
	}

	u, err := url.Parse(s.objectURL(path))
	if err != nil {
		return "", fmt.Errorf("storage/s3: parse URL: %w", err)
	}
	now := time.Now().UTC()
	datestamp := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")
	credentialScope := datestamp + "/" + s.region + "/s3/aws4_request"

	q := u.Query()
	q.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	q.Set("X-Amz-Credential", s.accessKey+"/"+credentialScope)
	q.Set("X-Amz-Date", amzDate)
	q.Set("X-Amz-Expires", strconv.Itoa(expires))
	q.Set("X-Amz-SignedHeaders", "host")
	u.RawQuery = q.Encode()

	canonicalURI := u.EscapedPath()
	if canonicalURI == "" {
		canonicalURI = "/"
	}
	canonicalQuery := u.Query().Encode()
	canonicalHeaders := "host:" + u.Host + "\n"
	canonicalRequest := strings.Join([]string{
		"GET",
		canonicalURI,
		canonicalQuery,
		canonicalHeaders,
		"host",
		"UNSIGNED-PAYLOAD",
	}, "\n")

	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")
	signingKey := deriveSigningKey(s.secretKey, datestamp, s.region, "s3")
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	q.Set("X-Amz-Signature", signature)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// ---------------------------------------------------------------------------
// AWS Signature V4 — minimal implementation (no SDK)
// ---------------------------------------------------------------------------

// objectURL returns the full URL for a key: endpoint/bucket/key
func (s *S3Storage) objectURL(key string) string {
	return s.endpoint + "/" + s.bucket + "/" + strings.TrimPrefix(key, "/")
}

// bucketURL returns the full URL for the bucket root: endpoint/bucket
func (s *S3Storage) bucketURL() string {
	return s.endpoint + "/" + s.bucket
}

// signRequest adds AWS Signature V4 headers to an HTTP request.
func (s *S3Storage) signRequest(req *http.Request, payloadHash string) {
	now := time.Now().UTC()
	datestamp := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")

	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadHash)
	if req.Header.Get("Host") == "" {
		req.Header.Set("Host", req.URL.Host)
	}

	// 1. Canonical request
	canonicalURI := req.URL.Path
	if canonicalURI == "" {
		canonicalURI = "/"
	}
	canonicalQueryString := req.URL.Query().Encode()

	// Signed headers (sorted lowercase)
	signedHeaderKeys := []string{}
	headerMap := map[string]string{}
	for key := range req.Header {
		lk := strings.ToLower(key)
		signedHeaderKeys = append(signedHeaderKeys, lk)
		headerMap[lk] = strings.TrimSpace(req.Header.Get(key))
	}
	sort.Strings(signedHeaderKeys)

	var canonicalHeaders strings.Builder
	for _, k := range signedHeaderKeys {
		canonicalHeaders.WriteString(k)
		canonicalHeaders.WriteString(":")
		canonicalHeaders.WriteString(headerMap[k])
		canonicalHeaders.WriteString("\n")
	}

	signedHeaders := strings.Join(signedHeaderKeys, ";")

	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI,
		canonicalQueryString,
		canonicalHeaders.String(),
		signedHeaders,
		payloadHash,
	}, "\n")

	// 2. String to sign
	credentialScope := datestamp + "/" + s.region + "/s3/aws4_request"
	canonicalRequestHash := sha256Hex([]byte(canonicalRequest))

	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		canonicalRequestHash,
	}, "\n")

	// 3. Signing key
	signingKey := deriveSigningKey(s.secretKey, datestamp, s.region, "s3")

	// 4. Signature
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	// 5. Authorization header
	authHeader := fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		s.accessKey, credentialScope, signedHeaders, signature,
	)
	req.Header.Set("Authorization", authHeader)
}

// deriveSigningKey derives the AWS Sig V4 signing key via chained HMAC-SHA256.
func deriveSigningKey(secretKey, datestamp, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secretKey), []byte(datestamp))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	kSigning := hmacSHA256(kService, []byte("aws4_request"))
	return kSigning
}

// hmacSHA256 computes HMAC-SHA256.
func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

// sha256Hex returns the lowercase hex SHA-256 of data.
func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// ---------------------------------------------------------------------------
// NewFromEnv creates a Storage backend based on environment variables.
//
//	STORAGE_BACKEND: "local" (default) or "s3"
//	S3_BUCKET, S3_REGION, S3_ENDPOINT: S3 configuration
//	AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY: S3 credentials
// ---------------------------------------------------------------------------

// NewFromEnv creates a Storage backend from environment variables.
// Falls back to LocalStorage with the given default path.
func NewFromEnv(defaultLocalPath string) (Storage, error) {
	backend := strings.ToLower(os.Getenv("STORAGE_BACKEND"))
	switch backend {
	case "s3":
		bucket := os.Getenv("S3_BUCKET")
		region := os.Getenv("S3_REGION")
		endpoint := os.Getenv("S3_ENDPOINT")
		accessKey := os.Getenv("AWS_ACCESS_KEY_ID")
		secretKey := os.Getenv("AWS_SECRET_ACCESS_KEY")
		return NewS3Storage(bucket, region, endpoint, accessKey, secretKey)
	default:
		return NewLocalStorage(defaultLocalPath), nil
	}
}
