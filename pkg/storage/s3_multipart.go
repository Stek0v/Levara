package storage

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

const MaxMultipartCheckpointBytes = 2 << 20
const maxMultipartParts = 10000

// WriteConditions prevent completion from overwriting a concurrent generation.
// Empty conditions snapshot the current object before creating the upload.
type WriteConditions struct {
	IfMatch     string `json:"if_match,omitempty"`
	IfNoneMatch string `json:"if_none_match,omitempty"`
}
type MultipartPart struct {
	Number int32  `json:"number"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	ETag   string `json:"etag,omitempty"`
}

// MultipartCheckpoint is a signed capability for this endpoint, bucket, key and
// upload. Persist its JSON atomically outside the source artifact. It contains
// object identifiers and digests, never AWS credentials or the checkpoint key.
// Restart recovery requires the original source and MultipartStateKey.
type MultipartCheckpoint struct {
	Version        int             `json:"version"`
	EndpointDigest string          `json:"endpoint_digest"`
	Bucket         string          `json:"bucket"`
	Key            string          `json:"key"`
	UploadID       string          `json:"upload_id"`
	Marker         string          `json:"marker"`
	SourceSHA256   string          `json:"source_sha256"`
	Size           int64           `json:"size"`
	PartSize       int64           `json:"part_size"`
	Parts          []MultipartPart `json:"parts"`
	Conditions     WriteConditions `json:"conditions"`
	Phase          string          `json:"phase"`
	Signature      string          `json:"signature"`
}
type MultipartError struct {
	State MultipartCheckpoint
	Err   error
}

func (e *MultipartError) Error() string {
	if e.State.Phase == "complete" {
		return "storage/s3: multipart object confirmed complete; operation ended with error: " + e.Err.Error()
	}
	return "storage/s3: multipart operation needs recovery: " + e.Err.Error()
}
func (e *MultipartError) Unwrap() error { return e.Err }

func (s *S3Storage) endpointDigest() string {
	h := sha256.Sum256([]byte(s.endpoint + "\n" + s.region))
	return hex.EncodeToString(h[:])
}
func (s *S3Storage) signCheckpoint(c MultipartCheckpoint) MultipartCheckpoint {
	c.Signature = ""
	payload, _ := json.Marshal(c)
	h := hmac.New(sha256.New, s.stateKey[:])
	_, _ = h.Write(payload)
	c.Signature = hex.EncodeToString(h.Sum(nil))
	return c
}
func validSHA256(v string) bool {
	if len(v) != 64 {
		return false
	}
	raw, err := hex.DecodeString(v)
	return err == nil && len(raw) == 32 && strings.ToLower(v) == v
}
func validConditions(c WriteConditions) bool {
	return (c.IfMatch == "" || c.IfNoneMatch == "") && (c.IfNoneMatch == "" || c.IfNoneMatch == "*") && len(c.IfMatch) <= 1024 && !strings.ContainsAny(c.IfMatch, "\r\n")
}
func (s *S3Storage) validateCheckpoint(key string, c MultipartCheckpoint) error {
	if validS3Key(key) != nil || c.Version != 1 || len(c.Signature) != 64 || c.Key != key || c.Bucket != s.bucket || c.EndpointDigest != s.endpointDigest() || c.UploadID == "" || len(c.UploadID) > 2048 || strings.ContainsAny(c.UploadID, "\r\n") || len(c.Marker) != 32 || !validSHA256(c.SourceSHA256) || c.Size <= 0 || c.Size > s.cfg.MaxObjectBytes || c.PartSize < 5<<20 || c.PartSize > 64<<20 || c.PartSize > s.cfg.PartSize || len(c.Parts) < 1 || len(c.Parts) > maxMultipartParts || int64(len(c.Parts)) != (c.Size+c.PartSize-1)/c.PartSize || !validConditions(c.Conditions) || c.Conditions == (WriteConditions{}) {
		return errors.New("storage/s3: invalid multipart checkpoint")
	}
	if _, err := hex.DecodeString(c.Marker); err != nil {
		return errors.New("storage/s3: invalid multipart marker")
	}
	switch c.Phase {
	case "uploading", "completing", "complete", "abort_pending", "aborted":
	default:
		return errors.New("storage/s3: invalid multipart phase")
	}
	checkpointBudget := 8192
	for i, p := range c.Parts {
		checkpointBudget += 160 + 6*len(p.ETag)
		if checkpointBudget > MaxMultipartCheckpointBytes {
			return errors.New("storage/s3: checkpoint size limit exceeded")
		}
		size := c.PartSize
		if left := c.Size - int64(i)*c.PartSize; left < size {
			size = left
		}
		if p.Number != int32(i+1) || p.Size != size || !validSHA256(p.SHA256) || len(p.ETag) > 1024 || strings.ContainsAny(p.ETag, "\r\n") {
			return errors.New("storage/s3: invalid multipart part")
		}
	}
	encoded, err := json.Marshal(c)
	if err != nil || len(encoded) > MaxMultipartCheckpointBytes {
		return errors.New("storage/s3: checkpoint size limit exceeded")
	}
	expected := s.signCheckpoint(c).Signature
	if !hmac.Equal([]byte(expected), []byte(c.Signature)) {
		return errors.New("storage/s3: checkpoint authentication failed")
	}
	return nil
}
func (s *S3Storage) ParseMultipartCheckpoint(expectedKey string, data []byte) (MultipartCheckpoint, error) {
	var c MultipartCheckpoint
	if len(data) > MaxMultipartCheckpointBytes {
		return c, errors.New("storage/s3: checkpoint size limit exceeded")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return MultipartCheckpoint{}, errors.New("storage/s3: malformed checkpoint")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return MultipartCheckpoint{}, errors.New("storage/s3: trailing checkpoint data")
	}
	if err := s.validateCheckpoint(expectedKey, c); err != nil {
		return MultipartCheckpoint{}, err
	}
	return c, nil
}

func (s *S3Storage) StartMultipart(ctx context.Context, key string, source io.ReaderAt, size int64, conditions WriteConditions) (MultipartCheckpoint, error) {
	if validS3Key(key) != nil || size <= 0 || size > s.cfg.MaxObjectBytes || !validConditions(conditions) {
		return MultipartCheckpoint{}, errors.New("storage/s3: invalid multipart input")
	}
	ctx, release, err := s.acquire(ctx)
	if err != nil {
		return MultipartCheckpoint{}, err
	}
	defer release()
	return s.startMultipart(ctx, key, source, size, conditions)
}
func (s *S3Storage) startMultipart(ctx context.Context, key string, source io.ReaderAt, size int64, conditions WriteConditions) (MultipartCheckpoint, error) {
	parts, digest, err := s.hashMultipartSource(ctx, source, size, s.cfg.PartSize)
	if err != nil {
		return MultipartCheckpoint{}, err
	}
	if conditions == (WriteConditions{}) {
		head, err := s.api.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
		if s3Status(err) == 404 {
			conditions.IfNoneMatch = "*"
		} else if err != nil {
			return MultipartCheckpoint{}, s3Failure(ctx, "snapshot upload destination", err)
		} else {
			conditions.IfMatch = aws.ToString(head.ETag)
			if conditions.IfMatch == "" {
				return MultipartCheckpoint{}, errors.New("storage/s3: upload destination has no ETag")
			}
		}
	}
	if !validConditions(conditions) {
		return MultipartCheckpoint{}, errors.New("storage/s3: invalid destination ETag")
	}
	var marker [16]byte
	if _, err = rand.Read(marker[:]); err != nil {
		return MultipartCheckpoint{}, err
	}
	c := MultipartCheckpoint{Version: 1, EndpointDigest: s.endpointDigest(), Bucket: s.bucket, Key: key, Marker: hex.EncodeToString(marker[:]), SourceSHA256: digest, Size: size, PartSize: s.cfg.PartSize, Parts: parts, Conditions: conditions, Phase: "uploading"}
	out, err := s.api.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String(s.bucket), Key: aws.String(key), ContentType: aws.String("application/octet-stream"), ChecksumAlgorithm: types.ChecksumAlgorithmSha256, ChecksumType: types.ChecksumTypeComposite, Metadata: map[string]string{"levara-upload": c.Marker, "levara-source-sha256": digest}})
	if err != nil {
		return MultipartCheckpoint{}, s3Failure(ctx, "create multipart upload", err)
	}
	c.UploadID = aws.ToString(out.UploadId)
	c = s.signCheckpoint(c)
	if err = s.validateCheckpoint(key, c); err != nil {
		return c, err
	}
	return c, nil
}
func (s *S3Storage) hashMultipartSource(ctx context.Context, source io.ReaderAt, size, partSize int64) ([]MultipartPart, string, error) {
	if source == nil || size <= 0 || size > s.cfg.MaxObjectBytes || partSize < 5<<20 || partSize > 64<<20 || (size+partSize-1)/partSize > maxMultipartParts {
		return nil, "", errors.New("storage/s3: invalid multipart source")
	}
	full := sha256.New()
	parts := make([]MultipartPart, 0, (size+partSize-1)/partSize)
	buf := make([]byte, 64<<10)
	for offset := int64(0); offset < size; offset += partSize {
		n := min(partSize, size-offset)
		h := sha256.New()
		got, err := io.CopyBuffer(io.MultiWriter(full, h), storageContextReader{ctx, io.NewSectionReader(source, offset, n)}, buf)
		if err != nil {
			return nil, "", err
		}
		if got != n {
			return nil, "", io.ErrUnexpectedEOF
		}
		parts = append(parts, MultipartPart{Number: int32(len(parts) + 1), Size: n, SHA256: hex.EncodeToString(h.Sum(nil))})
	}
	return parts, hex.EncodeToString(full.Sum(nil)), nil
}
func (s *S3Storage) lockUpload(id string) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeUploads[id] {
		return nil, errors.New("storage/s3: multipart upload already active")
	}
	s.activeUploads[id] = true
	return func() { s.mu.Lock(); delete(s.activeUploads, id); s.mu.Unlock() }, nil
}
func (s *S3Storage) ResumeMultipart(ctx context.Context, expectedKey string, c MultipartCheckpoint, source io.ReaderAt) (MultipartCheckpoint, error) {
	if err := s.validateCheckpoint(expectedKey, c); err != nil {
		return c, err
	}
	ctx, release, err := s.acquire(ctx)
	if err != nil {
		return c, err
	}
	defer release()
	return s.resumeMultipart(ctx, expectedKey, c, source)
}
func (s *S3Storage) resumeMultipart(ctx context.Context, key string, c MultipartCheckpoint, source io.ReaderAt) (MultipartCheckpoint, error) {
	if err := s.validateCheckpoint(key, c); err != nil {
		return c, err
	}
	unlock, err := s.lockUpload(c.UploadID)
	if err != nil {
		return c, err
	}
	defer unlock()
	c.Parts = append([]MultipartPart(nil), c.Parts...)
	if c.Phase == "abort_pending" || c.Phase == "aborted" {
		return c, errors.New("storage/s3: upload is being aborted")
	}
	done, err := s.multipartPublished(ctx, c)
	if err != nil {
		return c, err
	}
	if done {
		c.Phase = "complete"
		return s.signCheckpoint(c), nil
	}
	if c.Phase == "complete" {
		return c, errors.New("storage/s3: completed generation is no longer current")
	}
	parts, digest, err := s.hashMultipartSource(ctx, source, c.Size, c.PartSize)
	if err != nil {
		return c, err
	}
	if digest != c.SourceSHA256 {
		return c, errors.New("storage/s3: multipart source changed")
	}
	for i, p := range parts {
		if p.SHA256 != c.Parts[i].SHA256 {
			return c, errors.New("storage/s3: multipart source part changed")
		}
	}
	remote, err := s.listMultipartParts(ctx, c)
	if err != nil {
		return c, err
	}
	// Validate the entire remote inventory before changing authenticated state.
	// An error must return the original, still usable checkpoint.
	for _, p := range remote {
		want := c.Parts[int(p.Number)-1]
		if want.Size != p.Size || want.SHA256 != p.SHA256 {
			return c, errors.New("storage/s3: remote part does not match source")
		}
	}
	for i := range c.Parts {
		c.Parts[i].ETag = ""
	}
	for _, p := range remote {
		c.Parts[int(p.Number)-1].ETag = p.ETag
	}
	if err = s.uploadMissingParts(ctx, &c, source); err != nil {
		return s.signCheckpoint(c), err
	}
	c.Phase = "completing"
	c = s.signCheckpoint(c)
	completed := make([]types.CompletedPart, len(c.Parts))
	for i, p := range c.Parts {
		raw, _ := hex.DecodeString(p.SHA256)
		completed[i] = types.CompletedPart{PartNumber: aws.Int32(p.Number), ETag: aws.String(p.ETag), ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(raw))}
	}
	in := &s3.CompleteMultipartUploadInput{Bucket: aws.String(s.bucket), Key: aws.String(key), UploadId: aws.String(c.UploadID), MpuObjectSize: aws.Int64(c.Size), ChecksumType: types.ChecksumTypeComposite, MultipartUpload: &types.CompletedMultipartUpload{Parts: completed}}
	if c.Conditions.IfMatch != "" {
		in.IfMatch = aws.String(c.Conditions.IfMatch)
	} else {
		in.IfNoneMatch = aws.String(c.Conditions.IfNoneMatch)
	}
	out, completeErr := s.api.CompleteMultipartUpload(ctx, in)
	if completeErr == nil && (aws.ToString(out.ETag) == "" || aws.ToString(out.ChecksumSHA256) != multipartCompositeChecksum(c)) {
		completeErr = errors.New("incomplete multipart completion response")
	}
	// Even successful completion is observed through the same marker and size.
	// An interrupted response can be recovered by an older signed checkpoint.
	done, headErr := s.multipartPublished(ctx, c)
	if done {
		c.Phase = "complete"
		return s.signCheckpoint(c), nil
	}
	if completeErr != nil {
		return c, s3Failure(ctx, "complete multipart upload", completeErr)
	}
	if headErr != nil {
		return c, headErr
	}
	return c, errors.New("storage/s3: completed object was not confirmed")
}
func multipartCompositeChecksum(c MultipartCheckpoint) string {
	h := sha256.New()
	for _, p := range c.Parts {
		raw, _ := hex.DecodeString(p.SHA256)
		_, _ = h.Write(raw)
	}
	return base64.StdEncoding.EncodeToString(h.Sum(nil)) + "-" + strconv.Itoa(len(c.Parts))
}
func (s *S3Storage) multipartPublished(ctx context.Context, c MultipartCheckpoint) (bool, error) {
	out, err := s.api.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(c.Key), ChecksumMode: types.ChecksumModeEnabled})
	if s3Status(err) == 404 {
		return false, nil
	}
	if err != nil {
		return false, s3Failure(ctx, "reconcile multipart object", err)
	}
	if out.Metadata["levara-upload"] != c.Marker {
		return false, nil
	}
	if out.Metadata["levara-source-sha256"] != c.SourceSHA256 || out.ContentLength == nil || *out.ContentLength != c.Size || aws.ToString(out.ETag) == "" || aws.ToString(out.ChecksumSHA256) != multipartCompositeChecksum(c) {
		return false, errors.New("storage/s3: uploaded generation metadata or checksum mismatch")
	}
	return true, nil
}
func (s *S3Storage) listMultipartParts(ctx context.Context, c MultipartCheckpoint) ([]MultipartPart, error) {
	var result []MultipartPart
	var marker *string
	last := int32(0)
	for page := 0; page < maxMultipartParts; page++ {
		out, err := s.api.ListParts(ctx, &s3.ListPartsInput{Bucket: aws.String(s.bucket), Key: aws.String(c.Key), UploadId: aws.String(c.UploadID), MaxParts: aws.Int32(1000), PartNumberMarker: marker})
		if err != nil {
			return nil, s3Failure(ctx, "list multipart parts", err)
		}
		if out.IsTruncated == nil || len(out.Parts) > 1000 || len(result)+len(out.Parts) > len(c.Parts) || out.UploadId != nil && *out.UploadId != c.UploadID || out.Key != nil && *out.Key != c.Key || out.Bucket != nil && *out.Bucket != c.Bucket {
			return nil, errors.New("storage/s3: malformed multipart part list")
		}
		for _, p := range out.Parts {
			number := aws.ToInt32(p.PartNumber)
			raw, err := base64.StdEncoding.Strict().DecodeString(aws.ToString(p.ChecksumSHA256))
			if number <= last || number > int32(len(c.Parts)) || p.Size == nil || *p.Size <= 0 || aws.ToString(p.ETag) == "" || len(aws.ToString(p.ETag)) > 1024 || err != nil || len(raw) != 32 {
				return nil, errors.New("storage/s3: invalid remote multipart part")
			}
			last = number
			result = append(result, MultipartPart{Number: number, Size: *p.Size, ETag: *p.ETag, SHA256: hex.EncodeToString(raw)})
		}
		if !*out.IsTruncated {
			return result, nil
		}
		next := aws.ToString(out.NextPartNumberMarker)
		n, err := strconv.ParseInt(next, 10, 32)
		if err != nil || n <= 0 || int32(n) != last || marker != nil && next == *marker || len(out.Parts) == 0 {
			return nil, errors.New("storage/s3: nonprogress multipart pagination")
		}
		marker = aws.String(next)
	}
	return nil, errors.New("storage/s3: multipart pagination limit exceeded")
}
func (s *S3Storage) uploadMissingParts(ctx context.Context, c *MultipartCheckpoint, source io.ReaderAt) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var mu sync.Mutex
	next := 0
	var first error
	var wg sync.WaitGroup
	for worker := 0; worker < s.cfg.PartConcurrency; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buffer := make([]byte, c.PartSize)
			for {
				mu.Lock()
				for next < len(c.Parts) && c.Parts[next].ETag != "" {
					next++
				}
				if next == len(c.Parts) || first != nil {
					mu.Unlock()
					return
				}
				i := next
				next++
				mu.Unlock()
				p := c.Parts[i]
				data := buffer[:p.Size]
				n, err := source.ReadAt(data, int64(i)*c.PartSize)
				if err == io.EOF && n == len(data) {
					err = nil
				}
				if err == nil && n != len(data) {
					err = io.ErrUnexpectedEOF
				}
				sum := sha256.Sum256(data)
				if err == nil && hex.EncodeToString(sum[:]) != p.SHA256 {
					err = errors.New("storage/s3: source changed before part upload")
				}
				if err == nil {
					out, e := s.api.UploadPart(v4.SetPayloadHash(ctx, p.SHA256), &s3.UploadPartInput{Bucket: aws.String(s.bucket), Key: aws.String(c.Key), UploadId: aws.String(c.UploadID), PartNumber: aws.Int32(p.Number), Body: bytes.NewReader(data), ContentLength: aws.Int64(p.Size), ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(sum[:]))})
					err = s3Failure(ctx, "upload part", e)
					if err == nil {
						if aws.ToString(out.ETag) == "" || len(aws.ToString(out.ETag)) > 1024 || aws.ToString(out.ChecksumSHA256) != base64.StdEncoding.EncodeToString(sum[:]) {
							err = errors.New("storage/s3: part acknowledgement checksum mismatch")
						} else {
							c.Parts[i].ETag = *out.ETag
						}
					}
				}
				if err != nil {
					mu.Lock()
					if first == nil {
						first = err
						cancel()
					}
					mu.Unlock()
					return
				}
			}
		}()
	}
	wg.Wait()
	return first
}
func (s *S3Storage) AbortMultipart(ctx context.Context, expectedKey string, c MultipartCheckpoint) (MultipartCheckpoint, error) {
	if err := s.validateCheckpoint(expectedKey, c); err != nil {
		return c, err
	}
	if c.Phase != "complete" && c.Phase != "aborted" {
		c.Phase = "abort_pending"
		c = s.signCheckpoint(c)
	}
	ctx, release, err := s.acquire(ctx)
	if err != nil {
		return c, err
	}
	defer release()
	return s.abortMultipart(ctx, expectedKey, c)
}
func (s *S3Storage) abortMultipart(ctx context.Context, key string, c MultipartCheckpoint) (MultipartCheckpoint, error) {
	if err := s.validateCheckpoint(key, c); err != nil {
		return c, err
	}
	unlock, err := s.lockUpload(c.UploadID)
	if err != nil {
		return c, err
	}
	defer unlock()
	if c.Phase != "complete" && c.Phase != "aborted" {
		c.Phase = "abort_pending"
		c = s.signCheckpoint(c)
	}
	done, err := s.multipartPublished(ctx, c)
	if err != nil {
		return c, err
	}
	if done {
		c.Phase = "complete"
		return s.signCheckpoint(c), nil
	}
	if c.Phase == "complete" {
		return c, errors.New("storage/s3: refusing to abort a completed checkpoint")
	}
	if c.Phase == "aborted" {
		return c, nil
	}
	c.Phase = "abort_pending"
	c = s.signCheckpoint(c)
	for attempt := 0; attempt < 3; attempt++ {
		_, err = s.api.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{Bucket: aws.String(s.bucket), Key: aws.String(key), UploadId: aws.String(c.UploadID)})
		if err != nil && s3Status(err) != 404 {
			return c, s3Failure(ctx, "abort multipart upload", err)
		}
		parts, err := s.listMultipartParts(ctx, c)
		var failure *S3OperationError
		if errors.As(err, &failure) && failure.StatusCode == 404 {
			c.Phase = "aborted"
			return s.signCheckpoint(c), nil
		}
		if err != nil {
			return c, err
		}
		if len(parts) == 0 { // Empty parts alone still leaves an upload alive; verify NoSuchUpload.
			continue
		}
	}
	return c, errors.New("storage/s3: multipart abort remains unconfirmed")
}
