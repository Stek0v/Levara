package storage

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// This test uses an independently running, explicitly owned local S3 server.
// The existing local KMS protocol fixture is not AWS vendor certification.
func TestS3ExternalSandbox(t *testing.T) {
	if os.Getenv("LEVARA_TEST_S3_SANDBOX") != "1" {
		t.Skip("set LEVARA_TEST_S3_SANDBOX=1 for the owned external S3 sandbox")
	}
	required := func(name string) string {
		t.Helper()
		value := os.Getenv(name)
		if value == "" || strings.TrimSpace(value) != value {
			t.Fatalf("%s must be explicit and exact", name)
		}
		return value
	}
	endpoint, bucket, prefix := required("LEVARA_TEST_S3_ENDPOINT"), required("LEVARA_TEST_S3_BUCKET"), required("LEVARA_TEST_S3_PREFIX")
	accessKey, secretKey := required("LEVARA_TEST_S3_ACCESS_KEY"), required("LEVARA_TEST_S3_SECRET_KEY")
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" || parsed.Port() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || (!strings.EqualFold(parsed.Hostname(), "localhost") && !net.ParseIP(parsed.Hostname()).IsLoopback()) {
		t.Fatal("external fixture requires an exact loopback endpoint with port and no path/query/userinfo")
	}
	if !strings.HasPrefix(bucket, "levara-sandbox-") || !strings.HasPrefix(prefix, "levara-sandbox-") || !strings.HasSuffix(prefix, "/") || strings.Contains(prefix, "..") || strings.ContainsAny(prefix, "\r\n") {
		t.Fatal("bucket and prefix must have explicit levara-sandbox- ownership names; prefix ends with /")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	stateKey := make([]byte, 32)
	if _, err := rand.Read(stateKey); err != nil {
		t.Fatal(err)
	}
	config := S3Config{Bucket: bucket, Region: "us-east-1", Endpoint: endpoint, Credentials: credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""), MultipartStateKey: stateKey, PartSize: 5 << 20, PartConcurrency: 1, MultipartThreshold: 5 << 20, MaxObjectBytes: 32 << 20, MaxSpoolBytes: 32 << 20, Timeout: 15 * time.Second, SpoolDirectory: t.TempDir()}
	makeStore := func(config S3Config) *S3Storage {
		t.Helper()
		store, err := NewS3StorageWithConfig(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		return store
	}
	direct := makeStore(config)
	// A successful new-bucket create is the cleanup authority. Never reuse an
	// existing bucket, even if it happens to have the same account owner.
	exists, err := direct.api.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)})
	_ = exists
	if err == nil || s3Status(err) != 404 {
		t.Fatalf("owned bucket must be absent before create (status %d)", s3Status(err))
	}
	if _, err := direct.api.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatal(err)
	}
	var uploads []MultipartCheckpoint
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		for _, checkpoint := range uploads {
			_, err := direct.api.AbortMultipartUpload(cleanupCtx, &s3.AbortMultipartUploadInput{Bucket: aws.String(bucket), Key: aws.String(checkpoint.Key), UploadId: aws.String(checkpoint.UploadID)})
			if err != nil && s3Status(err) != 404 {
				t.Errorf("owned multipart cleanup: %v", err)
			}
		}
		keys, err := direct.List(cleanupCtx, prefix)
		if err != nil {
			t.Errorf("owned prefix inventory cleanup: %v", err)
			return
		}
		for _, key := range keys {
			if !strings.HasPrefix(key, prefix) {
				t.Errorf("cleanup refused foreign key")
				continue
			}
			if err := direct.Delete(cleanupCtx, key); err != nil {
				t.Errorf("owned object cleanup: %v", err)
			}
		}
		if _, err := direct.api.DeleteBucket(cleanupCtx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)}); err != nil {
			t.Errorf("owned bucket cleanup (foreign content is never deleted): %v", err)
		}
	})
	readAll := func(store Storage, key string) []byte {
		t.Helper()
		reader, err := store.Load(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		data, readErr := io.ReadAll(reader)
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil {
			t.Fatalf("read/close: %v %v", readErr, closeErr)
		}
		return data
	}
	key := prefix + "док +?#%.bin"
	binary := []byte{0, 1, 255, 0, 128, '+', '?', '#', '%', 10}
	if err := direct.Save(ctx, key, bytes.NewReader(binary)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readAll(direct, key), binary) {
		t.Fatal("escaped binary roundtrip changed bytes")
	}
	listed, err := direct.List(ctx, prefix)
	if err != nil || len(listed) != 1 || listed[0] != key {
		t.Fatalf("escaped list: %v err=%v", listed, err)
	}
	snapshot, err := direct.Snapshot(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	ranged, err := direct.LoadRange(ctx, key, 2, 5, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	got, readErr := io.ReadAll(ranged)
	closeErr := ranged.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(got, binary[2:7]) {
		t.Fatalf("range bytes=%v read=%v close=%v", got, readErr, closeErr)
	}
	badConfig := config
	badConfig.Credentials = credentials.NewStaticCredentialsProvider("INVALID_LOCAL_ONLY_ACCESS_KEY", "INVALID_LOCAL_ONLY_SECRET", "")
	bad := makeStore(badConfig)
	deniedKey := prefix + "denied.bin"
	deniedErr := bad.Save(ctx, deniedKey, bytes.NewReader(binary))
	var denied *S3OperationError
	if !errors.As(deniedErr, &denied) || denied.StatusCode != 403 {
		t.Fatalf("invalid credentials must produce sanitized403: %v", deniedErr)
	}
	if present, err := direct.Exists(ctx, deniedKey); err != nil || present {
		t.Fatalf("denied write published object=%v err=%v", present, err)
	}

	// Reverse-proxy faults never synthesize S3 success; all successful operations
	// are forwarded to the independent server, preserving the signed Host.
	partialKey, lostKey := prefix+"multipart-resume.bin", prefix+"multipart-lost-ack.bin"
	var faultMu sync.Mutex
	failPart, dropCompletion, failPublishedHead := true, true, false
	droppedPart, droppedACK, droppedHEAD := 0, 0, 0
	reverse := httputil.NewSingleHostReverseProxy(parsed)
	reverse.ModifyResponse = func(response *http.Response) error {
		request := response.Request
		faultMu.Lock()
		drop := dropCompletion && request.Method == "POST" && request.URL.Query().Get("uploadId") != "" && request.URL.Path == "/"+bucket+"/"+lostKey && response.StatusCode >= 200 && response.StatusCode < 300
		if drop {
			dropCompletion = false
			failPublishedHead = true
			droppedACK++
		}
		faultMu.Unlock()
		if drop {
			_, err := io.Copy(io.Discard, response.Body)
			closeErr := response.Body.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
			return errors.New("owned fixture dropped committed completion acknowledgement")
		}
		return nil
	}
	reverse.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(502)
		_, _ = io.WriteString(w, "<Error><Code>InternalError</Code><Message>owned fixture acknowledgement loss</Message></Error>")
	}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		faultMu.Lock()
		partFault := failPart && r.Method == "PUT" && r.URL.Query().Get("partNumber") == "2" && r.URL.Query().Get("uploadId") != "" && r.URL.Path == "/"+bucket+"/"+partialKey
		headFault := failPublishedHead && r.Method == "HEAD" && r.URL.Path == "/"+bucket+"/"+lostKey
		if partFault {
			failPart = false
			droppedPart++
		}
		if headFault {
			failPublishedHead = false
			droppedHEAD++
		}
		faultMu.Unlock()
		if partFault || headFault {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(503)
			_, _ = io.WriteString(w, "<Error><Code>ServiceUnavailable</Code><Message>owned fixture transient failure</Message></Error>")
			return
		}
		reverse.ServeHTTP(w, r)
	}))
	t.Cleanup(proxy.Close)
	proxyConfig := config
	proxyConfig.Endpoint = proxy.URL
	proxied := makeStore(proxyConfig)
	payload := make([]byte, 11<<20)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	startUpload := func(store *S3Storage, key string) MultipartCheckpoint {
		t.Helper()
		checkpoint, err := store.StartMultipart(ctx, key, bytes.NewReader(payload), int64(len(payload)), WriteConditions{IfNoneMatch: "*"})
		if err != nil {
			t.Fatal(err)
		}
		uploads = append(uploads, checkpoint)
		return checkpoint
	}
	partial := startUpload(proxied, partialKey)
	partial, err = proxied.ResumeMultipart(ctx, partialKey, partial, bytes.NewReader(payload))
	if err == nil || partial.Phase == "complete" || len(partial.Parts) != 3 || partial.Parts[0].ETag == "" {
		t.Fatalf("partial checkpoint lost actual uploaded part: phase=%s err=%v", partial.Phase, err)
	}
	persisted, err := json.Marshal(partial)
	if err != nil {
		t.Fatal(err)
	}
	reconstructed := makeStore(proxyConfig)
	partial, err = reconstructed.ParseMultipartCheckpoint(partialKey, persisted)
	if err != nil {
		t.Fatal(err)
	}
	complete, err := reconstructed.ResumeMultipart(ctx, partialKey, partial, bytes.NewReader(payload))
	if err != nil || complete.Phase != "complete" {
		t.Fatalf("real resume: phase=%s err=%v", complete.Phase, err)
	}
	if !bytes.Equal(readAll(direct, partialKey), payload) {
		t.Fatal("resumed remote bytes changed")
	}

	lostStart := startUpload(proxied, lostKey)
	lost, err := proxied.ResumeMultipart(ctx, lostKey, lostStart, bytes.NewReader(payload))
	if err == nil || lost.Phase != "completing" {
		t.Fatalf("lost ACK must retain completing checkpoint: phase=%s err=%v", lost.Phase, err)
	}
	lostJSON, err := json.Marshal(lost)
	if err != nil {
		t.Fatal(err)
	}
	afterACK := makeStore(proxyConfig)
	lost, err = afterACK.ParseMultipartCheckpoint(lostKey, lostJSON)
	if err != nil {
		t.Fatal(err)
	}
	lost, err = afterACK.ResumeMultipart(ctx, lostKey, lost, bytes.NewReader(payload))
	if err != nil || lost.Phase != "complete" {
		t.Fatalf("published generation reconciliation: %s %v", lost.Phase, err)
	}
	// An older authenticated pre-completion checkpoint must reconcile too.
	lost, err = afterACK.ResumeMultipart(ctx, lostKey, lostStart, bytes.NewReader(payload))
	if err != nil || lost.Phase != "complete" {
		t.Fatalf("older checkpoint reconciliation: %s %v", lost.Phase, err)
	}
	if !bytes.Equal(readAll(direct, lostKey), payload) {
		t.Fatal("lost ACK remote bytes changed")
	}
	faultMu.Lock()
	parts, acks, heads := droppedPart, droppedACK, droppedHEAD
	faultMu.Unlock()
	if parts != 1 || acks != 1 || heads != 1 {
		t.Fatalf("selective faults not actually exercised: parts=%d ACKs=%d HEADs=%d", parts, acks, heads)
	}

	abortKey := prefix + "multipart-abort.bin"
	abortState := startUpload(direct, abortKey)
	aborted, err := direct.AbortMultipart(ctx, abortKey, abortState)
	if err != nil || aborted.Phase != "aborted" {
		t.Fatalf("abort: %s %v", aborted.Phase, err)
	}
	remoteUploads, err := direct.api.ListMultipartUploads(ctx, &s3.ListMultipartUploadsInput{Bucket: aws.String(bucket), Prefix: aws.String(prefix)})
	if err != nil {
		t.Fatal(err)
	}
	if aws.ToBool(remoteUploads.IsTruncated) || len(remoteUploads.Uploads) != 0 {
		t.Fatalf("owned multipart uploads remain: %d", len(remoteUploads.Uploads))
	}
	if present, err := direct.Exists(ctx, abortKey); err != nil || present {
		t.Fatalf("aborted upload published=%v err=%v", present, err)
	}

	kmsProtocol, kms := newKMSProtocol(t)
	encrypted, err := NewEncryptedStorage(direct, kms, EncryptedStorageConfig{WriteKeyARN: testKeyARN, AllowedReadKeyARNs: []string{testNewKeyARN}, MaxObjectBytes: 16 << 20, SpoolDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	encryptedKey := prefix + "encrypted/документ.bin"
	if err := encrypted.Save(ctx, encryptedKey, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	ciphertext := readAll(direct, encryptedKey)
	if !bytes.HasPrefix(ciphertext, []byte(encryptedMagic)) || !bytes.Contains(ciphertext, []byte(testKeyARN)) || bytes.Equal(ciphertext, payload) || bytes.Contains(ciphertext, payload) {
		t.Fatal("independent S3 did not persist the old-key ciphertext envelope")
	}
	if !bytes.Equal(readAll(encrypted, encryptedKey), payload) {
		t.Fatal("external ciphertext/KMS wrapper roundtrip changed bytes")
	}
	if err := encrypted.RotateWriteKey(ctx, testNewKeyARN); err != nil {
		t.Fatal(err)
	}
	newEncryptedKey := prefix + "encrypted/rotated.bin"
	newPayload := append([]byte("after rotation:"), payload...)
	if err := encrypted.Save(ctx, newEncryptedKey, bytes.NewReader(newPayload)); err != nil {
		t.Fatal(err)
	}
	newCiphertext := readAll(direct, newEncryptedKey)
	if !bytes.HasPrefix(newCiphertext, []byte(encryptedMagic)) || !bytes.Contains(newCiphertext, []byte(testNewKeyARN)) || bytes.Contains(newCiphertext, newPayload) {
		t.Fatal("rotated write did not persist the new-key ciphertext envelope")
	}
	if !bytes.Equal(readAll(encrypted, encryptedKey), payload) || !bytes.Equal(readAll(encrypted, newEncryptedKey), newPayload) || !bytes.Equal(readAll(direct, encryptedKey), ciphertext) {
		t.Fatal("rotation changed old evidence or prevented allowed old/new reads")
	}
	kmsRequests := func() int {
		kmsProtocol.mu.Lock()
		defer kmsProtocol.mu.Unlock()
		total := 0
		for _, count := range kmsProtocol.calls {
			total += count
		}
		return total
	}
	restricted, err := NewEncryptedStorage(direct, kms, EncryptedStorageConfig{WriteKeyARN: testNewKeyARN, MaxObjectBytes: 16 << 20, SpoolDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	beforeRestricted := kmsRequests()
	reader, deniedErr := restricted.Load(ctx, encryptedKey)
	if reader != nil {
		_ = reader.Close()
	}
	if deniedErr == nil || reader != nil || kmsRequests() != beforeRestricted {
		t.Fatal("old ARN read authority denial must return nil reader without any KMS request")
	}
	if !bytes.Equal(readAll(restricted, newEncryptedKey), newPayload) {
		t.Fatal("old ARN restriction also denied the authorized new ARN")
	}
	if !bytes.Equal(readAll(direct, encryptedKey), ciphertext) {
		t.Fatal("old ARN denial changed remote ciphertext")
	}

	tampered := append([]byte(nil), newCiphertext...)
	tampered[len(tampered)-1] ^= 1
	if err := direct.Save(ctx, newEncryptedKey, bytes.NewReader(tampered)); err != nil {
		t.Fatal(err)
	}
	reader, tamperErr := encrypted.Load(ctx, newEncryptedKey)
	if reader != nil {
		_ = reader.Close()
	}
	if tamperErr == nil || reader != nil {
		t.Fatal("tampered remote ciphertext returned a plaintext reader")
	}
	if !bytes.Equal(readAll(direct, newEncryptedKey), tampered) {
		t.Fatal("tamper rejection modified remote evidence")
	}
	if err := direct.Save(ctx, newEncryptedKey, bytes.NewReader(newCiphertext)); err != nil {
		t.Fatal(err)
	}

	// Provider AccessDenied (remote key denial/revocation) is distinct from
	// the local old-ARN allow-list denial: actual signed Decrypt is attempted.
	beforeFailure := kmsProtocol.count("Decrypt")
	kmsProtocol.mu.Lock()
	kmsProtocol.fail = true
	kmsProtocol.mu.Unlock()
	reader, providerErr := encrypted.Load(ctx, encryptedKey)
	kmsProtocol.mu.Lock()
	kmsProtocol.fail = false
	kmsProtocol.mu.Unlock()
	if reader != nil {
		_ = reader.Close()
	}
	if providerErr == nil || reader != nil || kmsProtocol.count("Decrypt") <= beforeFailure {
		t.Fatal("KMS provider denial must reach Decrypt and fail without a plaintext reader")
	}
	if !bytes.Equal(readAll(direct, encryptedKey), ciphertext) || !bytes.Equal(readAll(direct, newEncryptedKey), newCiphertext) {
		t.Fatal("KMS provider denial changed remote ciphertext")
	}
	// A delayed provider independently proves the actual composed read deadline.
	beforeDeadline := kmsProtocol.count("Decrypt")
	kmsProtocol.mu.Lock()
	kmsProtocol.delay = 500 * time.Millisecond
	kmsProtocol.mu.Unlock()
	deadlineCtx, deadlineCancel := context.WithTimeout(ctx, 100*time.Millisecond)
	started := time.Now()
	reader, deadlineErr := encrypted.Load(deadlineCtx, encryptedKey)
	elapsed := time.Since(started)
	deadlineCancel()
	kmsProtocol.mu.Lock()
	kmsProtocol.delay = 0
	kmsProtocol.mu.Unlock()
	if reader != nil {
		_ = reader.Close()
	}
	if deadlineErr == nil || reader != nil || kmsProtocol.count("Decrypt") <= beforeDeadline || elapsed > time.Second {
		t.Fatalf("delayed KMS read must attempt Decrypt and fail with nil reader within 1s: elapsed=%s error=%v", elapsed, deadlineErr)
	}
	if !bytes.Equal(readAll(direct, encryptedKey), ciphertext) || !bytes.Equal(readAll(direct, newEncryptedKey), newCiphertext) {
		t.Fatal("KMS deadline changed remote ciphertext")
	}
	if !bytes.Equal(readAll(encrypted, encryptedKey), payload) || !bytes.Equal(readAll(encrypted, newEncryptedKey), newPayload) {
		t.Fatal("restored provider did not recover unchanged old/new bytes")
	}
	if kmsProtocol.count("Encrypt") < 2 || kmsProtocol.count("Decrypt") < 1 || kmsProtocol.count("DescribeKey") < 1 {
		t.Fatal("local signed KMS rotation protocol was not exercised")
	}
	if err := direct.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if present, err := direct.Exists(ctx, key); err != nil || present {
		t.Fatalf("deleted object still present=%v err=%v", present, err)
	}
	t.Logf("Observed independent S3 endpoint %s: escaped binary/range, credential403, checkpoint reconstruction, real multipart completion acknowledgement loss/HEAD recovery, abort and ciphertext with local signed KMS protocol; no AWS vendor certification", endpoint)
}
