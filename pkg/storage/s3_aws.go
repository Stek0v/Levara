package storage

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsmiddleware "github.com/aws/aws-sdk-go-v2/aws/middleware"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go/logging"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

const maxS3ListPages = 1000
const maxS3ListPageBytes = 8 << 20

// S3Config uses the official AWS credential chain unless Credentials is provided.
// MultipartStateKey must be the same independent 32-byte secret after a restart
// to resume signed checkpoints. An omitted key is random and instance-local.
type S3Config struct {
	Bucket, Region, Endpoint      string
	Credentials                   aws.CredentialsProvider
	MultipartStateKey             []byte
	MaxObjectBytes, MaxSpoolBytes int64
	MaxInFlight, MaxWaiters       int
	Timeout                       time.Duration
	SpoolDirectory                string
	PartSize                      int64
	PartConcurrency               int
	MultipartThreshold            int64
}

type S3Storage struct {
	bucket, region, endpoint string
	api                      *s3.Client
	presigner                *s3.PresignClient
	cfg                      S3Config
	stateKey                 [32]byte
	slots, waiters           chan struct{}
	mu                       sync.Mutex
	spoolBytes               int64
	activeUploads            map[string]bool
}

func NewS3Storage(bucket, region, endpoint, accessKey, secretKey string) (*S3Storage, error) {
	if strings.TrimSpace(accessKey) == "" || strings.TrimSpace(secretKey) == "" || strings.ContainsAny(accessKey+secretKey, "\r\n") {
		return nil, errors.New("storage: S3 access key and secret key required")
	}
	return NewS3StorageWithConfig(context.Background(), S3Config{Bucket: bucket, Region: region, Endpoint: endpoint, Credentials: credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")})
}

func NewS3StorageWithConfig(ctx context.Context, c S3Config) (*S3Storage, error) {
	if !validS3Identifier(c.Bucket) || c.Bucket == "." || c.Bucket == ".." {
		return nil, errors.New("storage: invalid S3 bucket name")
	}
	if c.Region == "" {
		c.Region = "us-east-1"
	}
	if !validS3Identifier(c.Region) {
		return nil, errors.New("storage: invalid S3 region")
	}
	if c.Endpoint == "" {
		c.Endpoint = "https://s3." + c.Region + ".amazonaws.com"
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(c.Endpoint, "#") {
		return nil, errors.New("storage: invalid S3 endpoint")
	}
	if p := u.Port(); p != "" {
		n, e := strconv.Atoi(p)
		if e != nil || n < 1 || n > 65535 {
			return nil, errors.New("storage: invalid S3 endpoint port")
		}
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	c.Endpoint = u.String()
	if c.MaxObjectBytes == 0 {
		c.MaxObjectBytes = 1 << 30
	}
	if c.MaxSpoolBytes == 0 {
		c.MaxSpoolBytes = c.MaxObjectBytes
	}
	if c.MaxInFlight == 0 {
		c.MaxInFlight = 2
	}
	if c.MaxWaiters == 0 {
		c.MaxWaiters = 16
	}
	if c.Timeout == 0 {
		c.Timeout = 5 * time.Minute
	}
	if c.PartSize == 0 {
		c.PartSize = 8 << 20
	}
	if c.PartConcurrency == 0 {
		c.PartConcurrency = 2
	}
	if c.MultipartThreshold == 0 {
		c.MultipartThreshold = 32 << 20
	}
	if c.MaxObjectBytes < 1 || c.MaxObjectBytes > 64<<30 || c.MaxSpoolBytes < 1 || c.MaxSpoolBytes > 256<<30 || c.MaxInFlight < 1 || c.MaxInFlight > 16 || c.MaxWaiters < 0 || c.MaxWaiters > 256 || c.Timeout < time.Millisecond || c.Timeout > 24*time.Hour || c.PartSize < 5<<20 || c.PartSize > 64<<20 || c.PartConcurrency < 1 || c.PartConcurrency > 8 || int64(c.MaxInFlight*c.PartConcurrency)*c.PartSize > 256<<20 || c.MultipartThreshold < 5<<20 || c.MultipartThreshold > c.MaxObjectBytes && c.MaxObjectBytes >= 5<<20 {
		return nil, errors.New("storage: invalid S3 resource limits")
	}
	if len(c.MultipartStateKey) != 0 && len(c.MultipartStateKey) != 32 {
		return nil, errors.New("storage: multipart state key must contain 32 bytes")
	}
	s := &S3Storage{bucket: c.Bucket, region: c.Region, endpoint: c.Endpoint, cfg: c, slots: make(chan struct{}, c.MaxInFlight), waiters: make(chan struct{}, c.MaxWaiters), activeUploads: make(map[string]bool)}
	if len(c.MultipartStateKey) == 32 {
		copy(s.stateKey[:], c.MultipartStateKey)
	} else if _, err = rand.Read(s.stateKey[:]); err != nil {
		return nil, err
	}
	s.cfg.MultipartStateKey = nil
	client := &http.Client{Timeout: c.Timeout, Transport: s3Transport{base: http.DefaultTransport}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	opts := []func(*config.LoadOptions) error{config.WithRegion(c.Region), config.WithHTTPClient(client), config.WithLogger(logging.Nop{}), config.WithClientLogMode(0), config.WithRetryMaxAttempts(1)}
	if c.Credentials != nil {
		opts = append(opts, config.WithCredentialsProvider(c.Credentials))
	}
	ac, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, errors.New("storage: load AWS configuration failed")
	}
	ac.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
	ac.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
	s.api = s3.NewFromConfig(ac, func(o *s3.Options) { o.BaseEndpoint = aws.String(c.Endpoint); o.UsePathStyle = true })
	s.presigner = s3.NewPresignClient(s.api)
	return s, nil
}

func decodeMultipartStateKey(value string) ([]byte, error) {
	if value == "" {
		return nil, nil
	}
	if len(value) != 44 {
		return nil, errors.New("storage: S3_MULTIPART_STATE_KEY must be base64 encoding of 32 bytes")
	}
	key, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil || len(key) != 32 {
		return nil, errors.New("storage: S3_MULTIPART_STATE_KEY must be base64 encoding of 32 bytes")
	}
	return key, nil
}
func validS3Identifier(v string) bool {
	return v != "" && strings.IndexFunc(v, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._-", r))
	}) == -1
}
func validS3Key(k string) error {
	if k == "" || len(k) > 1024 || !utf8.ValidString(k) || strings.ContainsRune(k, 0) {
		return errors.New("storage/s3: invalid object key")
	}
	return nil
}

func (s *S3Storage) acquire(ctx context.Context) (context.Context, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	select {
	case s.slots <- struct{}{}:
	default:
		select {
		case s.waiters <- struct{}{}:
		default:
			cancel()
			return nil, nil, errors.New("storage/s3: operation queue full")
		}
		select {
		case s.slots <- struct{}{}:
			<-s.waiters
		case <-ctx.Done():
			<-s.waiters
			cancel()
			return nil, nil, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		cancel()
		<-s.slots
		return nil, nil, err
	}
	var once sync.Once
	return ctx, func() { once.Do(func() { cancel(); <-s.slots }) }, nil
}

// s3Transport bounds control responses, detects truncated successful responses,
// and rejects multiple XML documents before the SDK's permissive deserializer.
// Object GET bodies remain streaming. The SDK alone signs and decodes requests.
type s3Transport struct{ base http.RoundTripper }

func (t s3Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	res, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	op := awsmiddleware.GetOperationName(r.Context())
	if op == "GetObject" && res.StatusCode >= 200 && res.StatusCode < 300 || r.Method == http.MethodHead {
		return res, nil
	}
	body, readErr := io.ReadAll(io.LimitReader(res.Body, maxS3ListPageBytes+1))
	closeErr := res.Body.Close()
	if readErr != nil || closeErr != nil || len(body) > maxS3ListPageBytes {
		return nil, errors.New("storage/s3: invalid or oversized response body")
	}
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		root := ""
		switch op {
		case "ListObjectsV2":
			root = "ListBucketResult"
		case "ListParts":
			root = "ListPartsResult"
		case "CreateMultipartUpload":
			root = "InitiateMultipartUploadResult"
		case "CompleteMultipartUpload":
			root = "CompleteMultipartUploadResult"
		}
		if root != "" {
			if err := validateS3XML(body, root, op == "CompleteMultipartUpload"); err != nil {
				return nil, err
			}
		}
	}
	res.Body = io.NopCloser(bytes.NewReader(body))
	return res, nil
}
func validateS3XML(body []byte, want string, allowError bool) error {
	d := xml.NewDecoder(bytes.NewReader(body))
	depth := 0
	seen := false
	for {
		tok, err := d.Token()
		if err == io.EOF {
			if !seen || depth != 0 {
				return errors.New("storage/s3: missing XML response")
			}
			return nil
		}
		if err != nil {
			return errors.New("storage/s3: malformed XML response")
		}
		switch v := tok.(type) {
		case xml.StartElement:
			if depth == 0 {
				if seen || v.Name.Local != want && !(allowError && v.Name.Local == "Error") {
					return errors.New("storage/s3: unexpected XML response")
				}
				seen = true
			}
			depth++
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(v)) != "" {
				return errors.New("storage/s3: trailing XML data")
			}
		}
	}
}

type S3OperationError struct {
	Operation  string
	StatusCode int
	cause      error
}

func (e *S3OperationError) Error() string {
	if e.StatusCode == 404 {
		return fmt.Sprintf("storage/s3: %s: not found (HTTP 404)", e.Operation)
	}
	return fmt.Sprintf("storage/s3: %s failed (HTTP %d)", e.Operation, e.StatusCode)
}
func (e *S3OperationError) Unwrap() error { return e.cause }
func s3Failure(ctx context.Context, op string, err error) error {
	if err == nil {
		return nil
	}
	var re *smithyhttp.ResponseError
	status := 0
	if errors.As(err, &re) {
		status = re.HTTPStatusCode()
	}
	return &S3OperationError{Operation: op, StatusCode: status, cause: ctx.Err()}
}
func s3Status(err error) int {
	var re *smithyhttp.ResponseError
	if errors.As(err, &re) {
		return re.HTTPStatusCode()
	}
	return 0
}

func (s *S3Storage) Save(ctx context.Context, key string, data io.Reader) (retErr error) {
	if err := validS3Key(key); err != nil {
		return err
	}
	ctx, release, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	src, size, cleanup, err := s.prepareSource(ctx, data)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, cleanup()) }()
	if size >= s.cfg.MultipartThreshold {
		state, err := s.startMultipart(ctx, key, src, size, WriteConditions{})
		if err != nil {
			return err
		}
		state, err = s.resumeMultipart(ctx, key, state, src)
		if err != nil && state.Phase != "complete" {
			var abortErr error
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			state, abortErr = s.abortMultipart(cleanupCtx, key, state)
			cancel()
			if abortErr != nil {
				return &MultipartError{State: state, Err: errors.Join(err, abortErr)}
			}
			if state.Phase == "complete" {
				// Reconciliation is authoritative even when the Complete response
				// was lost. Keep cancellation observable with the confirmed state.
				if ctxErr := ctx.Err(); ctxErr != nil {
					return &MultipartError{State: state, Err: ctxErr}
				}
				return nil
			}
		}
		return err
	}
	h := sha256.New()
	if _, err = io.Copy(h, storageContextReader{ctx, io.NewSectionReader(src, 0, size)}); err != nil {
		return err
	}
	sum := h.Sum(nil)
	ctx = v4.SetPayloadHash(ctx, hex.EncodeToString(sum))
	_, err = s.api.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key), Body: io.NewSectionReader(src, 0, size), ContentLength: aws.Int64(size), ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(sum)), ContentType: aws.String("application/octet-stream")})
	return s3Failure(ctx, "put object", err)
}

func (s *S3Storage) prepareSource(ctx context.Context, data io.Reader) (io.ReaderAt, int64, func() error, error) {
	if data == nil {
		return nil, 0, nil, errors.New("storage/s3: source required")
	}
	if a, ok := data.(io.ReaderAt); ok {
		if seek, ok := data.(io.Seeker); ok {
			start, err := seek.Seek(0, io.SeekCurrent)
			if err != nil {
				return nil, 0, nil, err
			}
			end, err := seek.Seek(0, io.SeekEnd)
			if err == nil {
				_, err = seek.Seek(start, io.SeekStart)
			}
			if err != nil {
				return nil, 0, nil, err
			}
			if end < start || end-start > s.cfg.MaxObjectBytes {
				return nil, 0, nil, errors.New("storage/s3: source size exceeds limit")
			}
			return io.NewSectionReader(a, start, end-start), end - start, func() error { _, err := seek.Seek(end, io.SeekStart); return err }, nil
		}
	}
	f, err := os.CreateTemp(s.cfg.SpoolDirectory, "levara-s3-upload-*")
	if err != nil {
		return nil, 0, nil, err
	}
	_ = os.Remove(f.Name())
	var reserved int64
	cleanup := func() error {
		err := f.Close()
		_ = os.Remove(f.Name())
		s.mu.Lock()
		s.spoolBytes -= reserved
		s.mu.Unlock()
		return err
	}
	stop := func() bool { return true }
	if closer, ok := data.(io.Closer); ok {
		stop = context.AfterFunc(ctx, func() { _ = closer.Close() })
	}
	defer stop()
	buf := make([]byte, 64<<10)
	var size int64
	for {
		n, readErr := (storageContextReader{ctx, data}).Read(buf)
		if n > 0 {
			size += int64(n)
			s.mu.Lock()
			fits := size <= s.cfg.MaxObjectBytes && s.spoolBytes+int64(n) <= s.cfg.MaxSpoolBytes
			if fits {
				s.spoolBytes += int64(n)
				reserved += int64(n)
			}
			s.mu.Unlock()
			if !fits {
				_ = cleanup()
				return nil, 0, nil, errors.New("storage/s3: upload spool budget exceeded")
			}
			if _, err = f.Write(buf[:n]); err != nil {
				_ = cleanup()
				return nil, 0, nil, err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			_ = cleanup()
			return nil, 0, nil, readErr
		}
		if err = ctx.Err(); err != nil {
			_ = cleanup()
			return nil, 0, nil, err
		}
	}
	return f, size, cleanup, nil
}

type storageContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r storageContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func (s *S3Storage) Load(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := validS3Key(key); err != nil {
		return nil, err
	}
	ctx, release, err := s.acquire(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.api.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		failure := s3Failure(ctx, "get object", err)
		release()
		return nil, failure
	}
	size := int64(-1)
	if out.ContentLength != nil {
		size = *out.ContentLength
	}
	if size > s.cfg.MaxObjectBytes {
		_ = out.Body.Close()
		release()
		return nil, errors.New("storage/s3: object exceeds limit")
	}
	return newS3Body(ctx, out.Body, release, size, s.cfg.MaxObjectBytes), nil
}

type s3Body struct {
	ctx                 context.Context
	body                io.ReadCloser
	release             func()
	stop                func() bool
	once                sync.Once
	expected, remaining int64
	closeErr            error
}

func newS3Body(ctx context.Context, body io.ReadCloser, release func(), expected, limit int64) *s3Body {
	b := &s3Body{ctx: ctx, body: body, release: release, expected: expected, remaining: limit}
	b.stop = context.AfterFunc(ctx, func() { _ = b.Close() })
	return b
}
func (b *s3Body) Close() error {
	b.once.Do(func() { b.closeErr = b.body.Close(); b.release() })
	return b.closeErr
}
func (b *s3Body) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if err := b.ctx.Err(); err != nil {
		_ = b.Close()
		return 0, err
	}
	if int64(len(p)) > b.remaining+1 {
		p = p[:b.remaining+1]
	}
	n, err := b.body.Read(p)
	b.remaining -= int64(n)
	if b.expected >= 0 {
		b.expected -= int64(n)
		if b.expected < 0 {
			err = errors.New("storage/s3: response exceeded declared length")
		}
	}
	if b.remaining < 0 {
		n = 0
		err = errors.New("storage/s3: object exceeds limit")
	}
	if err == io.EOF && b.expected > 0 {
		err = io.ErrUnexpectedEOF
	}
	if err != nil {
		if b.stop != nil {
			b.stop()
		}
		if closeErr := b.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}
	return n, err
}

func (s *S3Storage) Delete(ctx context.Context, key string) error {
	if err := validS3Key(key); err != nil {
		return err
	}
	ctx, release, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	_, err = s.api.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if s3Status(err) == 404 {
		return nil
	}
	return s3Failure(ctx, "delete object", err)
}
func (s *S3Storage) Exists(ctx context.Context, key string) (bool, error) {
	if err := validS3Key(key); err != nil {
		return false, err
	}
	ctx, release, err := s.acquire(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	_, err = s.api.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if s3Status(err) == 404 {
		return false, nil
	}
	return err == nil, s3Failure(ctx, "head object", err)
}
func (s *S3Storage) List(ctx context.Context, prefix string) ([]string, error) {
	if len(prefix) > 1024 || !utf8.ValidString(prefix) {
		return nil, errors.New("storage/s3: invalid prefix")
	}
	ctx, release, err := s.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	keys := []string{}
	seen := map[string]bool{}
	var token *string
	totalBytes := 0
	for page := 0; page < maxS3ListPages; page++ {
		out, err := s.api.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String(prefix), ContinuationToken: token, MaxKeys: aws.Int32(1000)})
		if err != nil {
			return nil, s3Failure(ctx, "list objects", err)
		}
		if out.IsTruncated == nil || len(out.Contents) > 1000 {
			return nil, errors.New("storage/s3: malformed list page")
		}
		for _, v := range out.Contents {
			key := aws.ToString(v.Key)
			totalBytes += len(key)
			if validS3Key(key) != nil || !strings.HasPrefix(key, prefix) || totalBytes > 8<<20 || len(keys) >= 100000 {
				return nil, errors.New("storage/s3: invalid or oversized object list")
			}
			keys = append(keys, key)
		}
		if !*out.IsTruncated {
			return keys, nil
		}
		next := aws.ToString(out.NextContinuationToken)
		if next == "" || len(next) > 8192 || seen[next] {
			return nil, errors.New("storage/s3: nonprogress list pagination")
		}
		seen[next] = true
		token = aws.String(next)
	}
	return nil, errors.New("storage/s3: list page limit exceeded")
}
func (s *S3Storage) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if err := validS3Key(key); err != nil {
		return "", err
	}
	ctx, release, err := s.acquire(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	if ttl < time.Second {
		ttl = time.Second
	}
	if ttl > 7*24*time.Hour {
		ttl = 7 * 24 * time.Hour
	}
	out, err := s.presigner.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)}, func(o *s3.PresignOptions) { o.Expires = ttl })
	if err != nil {
		return "", s3Failure(ctx, "presign get", err)
	}
	return out.URL, nil
}

// ObjectSnapshot is an immutable read target; ETag is mandatory even when a
// version ID is available. Range readers never silently switch object versions.
type ObjectSnapshot struct {
	ETag      string `json:"etag"`
	VersionID string `json:"version_id,omitempty"`
	Size      int64  `json:"size"`
}

func (s *S3Storage) Snapshot(ctx context.Context, key string) (ObjectSnapshot, error) {
	if err := validS3Key(key); err != nil {
		return ObjectSnapshot{}, err
	}
	ctx, release, err := s.acquire(ctx)
	if err != nil {
		return ObjectSnapshot{}, err
	}
	defer release()
	out, err := s.api.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return ObjectSnapshot{}, s3Failure(ctx, "snapshot", err)
	}
	v := ObjectSnapshot{aws.ToString(out.ETag), aws.ToString(out.VersionId), aws.ToInt64(out.ContentLength)}
	if out.ContentLength == nil || v.ETag == "" || v.Size < 0 || v.Size > s.cfg.MaxObjectBytes {
		return ObjectSnapshot{}, errors.New("storage/s3: incomplete snapshot response")
	}
	return v, nil
}
func (s *S3Storage) LoadRange(ctx context.Context, key string, offset, length int64, snapshot ObjectSnapshot) (io.ReadCloser, error) {
	if err := validS3Key(key); err != nil {
		return nil, err
	}
	if snapshot.ETag == "" || len(snapshot.ETag) > 1024 || len(snapshot.VersionID) > 1024 || strings.ContainsAny(snapshot.ETag+snapshot.VersionID, "\r\n") || snapshot.Size < 0 || snapshot.Size > s.cfg.MaxObjectBytes || offset < 0 || length <= 0 || offset >= snapshot.Size || length > snapshot.Size-offset {
		return nil, errors.New("storage/s3: invalid pinned range")
	}
	ctx, release, err := s.acquire(ctx)
	if err != nil {
		return nil, err
	}
	input := &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key), Range: aws.String(fmt.Sprintf("bytes=%d-%d", offset, offset+length-1)), IfMatch: aws.String(snapshot.ETag)}
	if snapshot.VersionID != "" {
		input.VersionId = aws.String(snapshot.VersionID)
	}
	out, err := s.api.GetObject(ctx, input)
	if err != nil {
		failure := s3Failure(ctx, "get range", err)
		release()
		return nil, failure
	}
	raw, _ := awsmiddleware.GetRawResponse(out.ResultMetadata).(*smithyhttp.Response)
	expectedRange := fmt.Sprintf("bytes %d-%d/%d", offset, offset+length-1, snapshot.Size)
	if raw == nil || raw.StatusCode != 206 || aws.ToString(out.ContentRange) != expectedRange || out.ContentLength == nil || *out.ContentLength != length || aws.ToString(out.ETag) != snapshot.ETag || snapshot.VersionID != "" && aws.ToString(out.VersionId) != snapshot.VersionID {
		_ = out.Body.Close()
		release()
		return nil, errors.New("storage/s3: range response does not match pinned snapshot")
	}
	return newS3Body(ctx, out.Body, release, length, length), nil
}
