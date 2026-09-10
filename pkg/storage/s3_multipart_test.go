package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/credentials"
)

type multipartHTTPObject struct {
	data                           []byte
	etag, marker, digest, checksum string
}
type multipartHTTPUpload struct {
	key, marker, digest string
	parts               map[int]multipartHTTPObject
}
type multipartHTTPFixture struct {
	mu                                                                                    sync.Mutex
	objects                                                                               map[string]multipartHTTPObject
	uploads                                                                               map[string]*multipartHTTPUpload
	calls, creates, partPuts, aborts, completes                                           int
	failPart                                                                              int
	failPartOnce, completeError, lostComplete, abortFailure, listFailure, listNonprogress bool
	pageSize                                                                              int
	lastIfMatch, lastIfNoneMatch                                                          string
}

func newMultipartHTTP(t *testing.T) (*multipartHTTPFixture, *httptest.Server) {
	t.Helper()
	f := &multipartHTTPFixture{objects: map[string]multipartHTTPObject{}, uploads: map[string]*multipartHTTPUpload{}, pageSize: 1000}
	server := httptest.NewServer(http.HandlerFunc(f.serveHTTP))
	t.Cleanup(server.Close)
	return f, server
}
func xmlText(v string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(v))
	return b.String()
}
func (f *multipartHTTPFixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if !strings.Contains(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256") {
		w.WriteHeader(401)
		return
	}
	key := strings.TrimPrefix(r.URL.Path, "/bucket/")
	q := r.URL.Query()
	id := q.Get("uploadId")
	notFound := func() { w.WriteHeader(404); fmt.Fprint(w, "<Error><Code>NoSuchUpload</Code></Error>") }
	if r.Method == http.MethodHead {
		obj, ok := f.objects[key]
		if !ok {
			notFound()
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(obj.data)))
		w.Header().Set("ETag", obj.etag)
		w.Header().Set("x-amz-meta-levara-upload", obj.marker)
		w.Header().Set("x-amz-meta-levara-source-sha256", obj.digest)
		w.Header().Set("x-amz-checksum-sha256", obj.checksum)
		return
	}
	if r.Method == http.MethodGet && id == "" {
		obj, ok := f.objects[key]
		if !ok {
			notFound()
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(obj.data)))
		w.Header().Set("ETag", obj.etag)
		_, _ = w.Write(obj.data)
		return
	}
	if r.Method == http.MethodPost && q.Has("uploads") {
		f.creates++
		id = "upload-" + strconv.Itoa(f.creates)
		f.uploads[id] = &multipartHTTPUpload{key: key, marker: r.Header.Get("x-amz-meta-levara-upload"), digest: r.Header.Get("x-amz-meta-levara-source-sha256"), parts: map[int]multipartHTTPObject{}}
		fmt.Fprintf(w, "<InitiateMultipartUploadResult><Bucket>bucket</Bucket><Key>%s</Key><UploadId>%s</UploadId></InitiateMultipartUploadResult>", xmlText(key), id)
		return
	}
	up := f.uploads[id]
	if up == nil || up.key != key {
		notFound()
		return
	}
	if r.Method == http.MethodPut {
		f.partPuts++
		number, _ := strconv.Atoi(q.Get("partNumber"))
		if number == f.failPart && f.failPartOnce {
			f.failPartOnce = false
			w.WriteHeader(503)
			fmt.Fprint(w, "<Error><Code>SlowDown</Code></Error>")
			return
		}
		data, err := io.ReadAll(r.Body)
		sum := sha256.Sum256(data)
		checksum := base64.StdEncoding.EncodeToString(sum[:])
		if err != nil || int64(len(data)) != r.ContentLength || r.Header.Get("x-amz-checksum-sha256") != checksum || r.Header.Get("x-amz-content-sha256") != hex.EncodeToString(sum[:]) {
			w.WriteHeader(400)
			fmt.Fprint(w, "<Error><Code>BadDigest</Code></Error>")
			return
		}
		etag := fmt.Sprintf(`"part-%d"`, number)
		up.parts[number] = multipartHTTPObject{data: data, etag: etag, checksum: checksum}
		w.Header().Set("ETag", etag)
		w.Header().Set("x-amz-checksum-sha256", checksum)
		return
	}
	if r.Method == http.MethodGet {
		if f.listFailure {
			w.WriteHeader(503)
			fmt.Fprint(w, "<Error><Code>SlowDown</Code></Error>")
			return
		}
		marker, _ := strconv.Atoi(q.Get("part-number-marker"))
		numbers := []int{}
		for n := range up.parts {
			if n > marker {
				numbers = append(numbers, n)
			}
		}
		sort.Ints(numbers)
		truncated := len(numbers) > f.pageSize
		if truncated {
			numbers = numbers[:f.pageSize]
		}
		fmt.Fprintf(w, "<ListPartsResult><Bucket>bucket</Bucket><Key>%s</Key><UploadId>%s</UploadId>", xmlText(key), id)
		for _, n := range numbers {
			p := up.parts[n]
			fmt.Fprintf(w, "<Part><PartNumber>%d</PartNumber><ETag>%s</ETag><Size>%d</Size><ChecksumSHA256>%s</ChecksumSHA256></Part>", n, xmlText(p.etag), len(p.data), p.checksum)
		}
		if f.listNonprogress {
			fmt.Fprint(w, "<IsTruncated>true</IsTruncated><NextPartNumberMarker>0</NextPartNumberMarker>")
		} else {
			fmt.Fprintf(w, "<IsTruncated>%t</IsTruncated>", truncated)
			if truncated {
				fmt.Fprintf(w, "<NextPartNumberMarker>%d</NextPartNumberMarker>", numbers[len(numbers)-1])
			}
		}
		fmt.Fprint(w, "</ListPartsResult>")
		return
	}
	if r.Method == http.MethodDelete {
		f.aborts++
		if f.abortFailure {
			w.WriteHeader(503)
			fmt.Fprint(w, "<Error><Code>SlowDown</Code></Error>")
			return
		}
		delete(f.uploads, id)
		w.WriteHeader(204)
		return
	}
	if r.Method == http.MethodPost {
		f.completes++
		f.lastIfMatch = r.Header.Get("If-Match")
		f.lastIfNoneMatch = r.Header.Get("If-None-Match")
		if f.completeError {
			fmt.Fprint(w, "<Error><Code>InternalError</Code><Message>injected failure</Message></Error>")
			return
		}
		existing, exists := f.objects[key]
		if f.lastIfNoneMatch == "*" && exists || f.lastIfMatch != "" && (!exists || existing.etag != f.lastIfMatch) {
			w.WriteHeader(412)
			fmt.Fprint(w, "<Error><Code>PreconditionFailed</Code></Error>")
			return
		}
		var request struct {
			Parts []struct {
				Number   int    `xml:"PartNumber"`
				ETag     string `xml:"ETag"`
				Checksum string `xml:"ChecksumSHA256"`
			} `xml:"Part"`
		}
		if err := xml.NewDecoder(r.Body).Decode(&request); err != nil {
			w.WriteHeader(400)
			return
		}
		var data []byte
		h := sha256.New()
		for i, p := range request.Parts {
			part, ok := up.parts[p.Number]
			if !ok || p.Number != i+1 || p.ETag != part.etag || p.Checksum != part.checksum {
				w.WriteHeader(400)
				return
			}
			raw, _ := base64.StdEncoding.DecodeString(part.checksum)
			_, _ = h.Write(raw)
			data = append(data, part.data...)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != up.digest {
			w.WriteHeader(400)
			return
		}
		checksum := base64.StdEncoding.EncodeToString(h.Sum(nil)) + "-" + strconv.Itoa(len(request.Parts))
		etag := `"object-` + id + `"`
		f.objects[key] = multipartHTTPObject{data: data, etag: etag, marker: up.marker, digest: up.digest, checksum: checksum}
		delete(f.uploads, id)
		if f.lostComplete {
			f.lostComplete = false
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		fmt.Fprintf(w, "<CompleteMultipartUploadResult><Bucket>bucket</Bucket><Key>%s</Key><ETag>%s</ETag><ChecksumSHA256>%s</ChecksumSHA256><ChecksumType>COMPOSITE</ChecksumType></CompleteMultipartUploadResult>", xmlText(key), xmlText(etag), checksum)
		return
	}
	w.WriteHeader(405)
}
func multipartClient(t *testing.T, server *httptest.Server, stateKey []byte) *S3Storage {
	t.Helper()
	s, err := NewS3StorageWithConfig(context.Background(), S3Config{Bucket: "bucket", Endpoint: server.URL, Credentials: credentials.NewStaticCredentialsProvider("key", "secret", "session"), MultipartStateKey: stateKey, PartSize: 5 << 20, PartConcurrency: 1, MultipartThreshold: 5 << 20, MaxObjectBytes: 32 << 20, MaxSpoolBytes: 32 << 20, Timeout: 5 * time.Second, SpoolDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func multipartBytes() []byte { return bytes.Repeat([]byte{0, 0xff, 2, 3, 0xfe}, (12<<20)/5) }

func TestS3MultipartResumeRestartAndRotatedCredentials(t *testing.T) {
	f, server := newMultipartHTTP(t)
	stateKey := bytes.Repeat([]byte{7}, 32)
	s := multipartClient(t, server, stateKey)
	data := multipartBytes()
	key := "документы/a +?#%.bin"
	c, err := s.StartMultipart(context.Background(), key, bytes.NewReader(data), int64(len(data)), WriteConditions{IfNoneMatch: "*"})
	if err != nil {
		t.Fatal(err)
	}
	old := c
	f.failPart = 2
	f.failPartOnce = true
	c, err = s.ResumeMultipart(context.Background(), key, c, bytes.NewReader(data))
	if err == nil || c.Parts[0].ETag == "" {
		t.Fatalf("interruption not recorded: phase=%s err=%v", c.Phase, err)
	}
	encoded, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewS3StorageWithConfig(context.Background(), S3Config{Bucket: "bucket", Endpoint: server.URL, Credentials: credentials.NewStaticCredentialsProvider("rotated", "new-secret", "new-session"), MultipartStateKey: stateKey, PartSize: 5 << 20, PartConcurrency: 1, MultipartThreshold: 5 << 20, MaxObjectBytes: 32 << 20})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := restarted.ParseMultipartCheckpoint(key, encoded)
	if err != nil {
		t.Fatal(err)
	}
	f.pageSize = 1
	c, err = restarted.ResumeMultipart(context.Background(), key, parsed, bytes.NewReader(data))
	if err != nil || c.Phase != "complete" {
		t.Fatalf("restart recovery: %s %v", c.Phase, err)
	}
	f.mu.Lock()
	got := append([]byte(nil), f.objects[key].data...)
	puts := f.partPuts
	f.mu.Unlock()
	if !bytes.Equal(got, data) || puts != 4 {
		t.Fatalf("resume duplicated valid parts or bytes: puts=%d", puts)
	}
	c, err = restarted.ResumeMultipart(context.Background(), key, old, nil)
	if err != nil || c.Phase != "complete" {
		t.Fatalf("older checkpoint could not reconcile: %s %v", c.Phase, err)
	}
	c, err = restarted.AbortMultipart(context.Background(), key, old)
	if err != nil || c.Phase != "complete" {
		t.Fatalf("replay aborted completed generation: %s %v", c.Phase, err)
	}
	if f.aborts != 0 {
		t.Fatal("completed upload received abort")
	}
}

func TestS3MultipartCheckpointAuthenticationBeforeNetwork(t *testing.T) {
	f, server := newMultipartHTTP(t)
	stateKey := bytes.Repeat([]byte{3}, 32)
	s := multipartClient(t, server, stateKey)
	data := []byte("source")
	c, err := s.StartMultipart(context.Background(), "key", bytes.NewReader(data), int64(len(data)), WriteConditions{IfNoneMatch: "*"})
	if err != nil {
		t.Fatal(err)
	}
	before := f.calls
	for _, change := range []func(*MultipartCheckpoint){func(c *MultipartCheckpoint) { c.Key = "other" }, func(c *MultipartCheckpoint) { c.UploadID = "foreign" }, func(c *MultipartCheckpoint) { c.SourceSHA256 = strings.Repeat("0", 64) }, func(c *MultipartCheckpoint) { c.Conditions = WriteConditions{IfMatch: "foreign"} }, func(c *MultipartCheckpoint) { c.EndpointDigest = strings.Repeat("0", 64) }, func(c *MultipartCheckpoint) { c.Phase = "complete" }, func(c *MultipartCheckpoint) { c.Parts = make([]MultipartPart, 10001) }, func(c *MultipartCheckpoint) {
		c.Parts = append([]MultipartPart(nil), c.Parts...)
		c.Parts[0].ETag = "forged"
	}} {
		tampered := c
		change(&tampered)
		if _, err = s.ResumeMultipart(context.Background(), "key", tampered, bytes.NewReader(data)); err == nil {
			t.Fatal("tampered resume accepted")
		}
		if _, err = s.AbortMultipart(context.Background(), "key", tampered); err == nil {
			t.Fatal("tampered abort accepted")
		}
	}
	other := multipartClient(t, server, nil)
	if _, err = other.ResumeMultipart(context.Background(), "key", c, bytes.NewReader(data)); err == nil {
		t.Fatal("cross-instance state accepted without key")
	}
	encoded, _ := json.Marshal(c)
	for _, raw := range [][]byte{bytes.Repeat([]byte("x"), MaxMultipartCheckpointBytes+1), append(encoded, []byte(" {}")...), bytes.Replace(encoded, []byte(`"version":1`), []byte(`"unknown":1,"version":1`), 1)} {
		if _, err = s.ParseMultipartCheckpoint("key", raw); err == nil {
			t.Fatal("malformed state parsed")
		}
	}
	if f.calls != before {
		t.Fatal("invalid checkpoint reached network")
	}
}

func TestS3MultipartSourceMutationBeforeEffects(t *testing.T) {
	f, server := newMultipartHTTP(t)
	s := multipartClient(t, server, bytes.Repeat([]byte{1}, 32))
	data := []byte("source")
	c, err := s.StartMultipart(context.Background(), "key", bytes.NewReader(data), int64(len(data)), WriteConditions{IfNoneMatch: "*"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ResumeMultipart(context.Background(), "key", c, bytes.NewReader([]byte("change"))); err == nil {
		t.Fatal("changed source accepted")
	}
	if f.partPuts != 0 || f.completes != 0 {
		t.Fatal("changed source produced effects")
	}
}

func TestS3MultipartLostCompletionAndCAS(t *testing.T) {
	for _, scenario := range []string{"lost-response", "http200-error", "concurrent-create", "concurrent-update"} {
		t.Run(scenario, func(t *testing.T) {
			f, server := newMultipartHTTP(t)
			s := multipartClient(t, server, bytes.Repeat([]byte{2}, 32))
			data := []byte("source")
			if scenario == "concurrent-update" {
				f.objects["key"] = multipartHTTPObject{data: []byte("old"), etag: `"old"`}
			}
			c, err := s.StartMultipart(context.Background(), "key", bytes.NewReader(data), int64(len(data)), WriteConditions{})
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "lost-response" {
				f.lostComplete = true
			}
			if scenario == "http200-error" {
				f.completeError = true
			}
			if strings.HasPrefix(scenario, "concurrent-") {
				f.objects["key"] = multipartHTTPObject{data: []byte("other"), etag: `"other"`}
			}
			c, err = s.ResumeMultipart(context.Background(), "key", c, bytes.NewReader(data))
			if scenario == "lost-response" {
				if err != nil || c.Phase != "complete" {
					t.Fatalf("lost completion not reconciled: %s %v", c.Phase, err)
				}
			} else {
				if err == nil || c.Phase == "complete" {
					t.Fatalf("false completion: %s %v", c.Phase, err)
				}
				if strings.HasPrefix(scenario, "concurrent-") && string(f.objects["key"].data) != "other" {
					t.Fatal("concurrent write overwritten")
				}
			}
			if scenario == "http200-error" {
				f.completeError = false
				c, err = s.ResumeMultipart(context.Background(), "key", c, bytes.NewReader(data))
				if err != nil || c.Phase != "complete" {
					t.Fatalf("retry after embedded error: %s %v", c.Phase, err)
				}
			}
		})
	}
}

func TestS3MultipartAbortPendingAndRecovery(t *testing.T) {
	f, server := newMultipartHTTP(t)
	s := multipartClient(t, server, bytes.Repeat([]byte{1}, 32))
	data := []byte("source")
	c, err := s.StartMultipart(context.Background(), "key", bytes.NewReader(data), int64(len(data)), WriteConditions{IfNoneMatch: "*"})
	if err != nil {
		t.Fatal(err)
	}
	f.abortFailure = true
	c, err = s.AbortMultipart(context.Background(), "key", c)
	if err == nil || c.Phase != "abort_pending" {
		t.Fatalf("abort failure hidden: %s %v", c.Phase, err)
	}
	if _, err = s.ResumeMultipart(context.Background(), "key", c, bytes.NewReader(data)); err == nil {
		t.Fatal("aborting upload resumed")
	}
	f.abortFailure = false
	c, err = s.AbortMultipart(context.Background(), "key", c)
	if err != nil || c.Phase != "aborted" || len(f.uploads) != 0 {
		t.Fatalf("abort not verified: %s %v", c.Phase, err)
	}
}

func TestS3MultipartListErrorsAndNonprogress(t *testing.T) {
	for _, failure := range []string{"status", "nonprogress"} {
		t.Run(failure, func(t *testing.T) {
			f, server := newMultipartHTTP(t)
			s := multipartClient(t, server, bytes.Repeat([]byte{1}, 32))
			data := []byte("source")
			c, err := s.StartMultipart(context.Background(), "key", bytes.NewReader(data), int64(len(data)), WriteConditions{IfNoneMatch: "*"})
			if err != nil {
				t.Fatal(err)
			}
			f.listFailure = failure == "status"
			f.listNonprogress = failure == "nonprogress"
			before := f.calls
			if _, err = s.ResumeMultipart(context.Background(), "key", c, bytes.NewReader(data)); err == nil {
				t.Fatal("invalid part listing accepted")
			}
			if f.calls-before > 2 || f.partPuts != 0 {
				t.Fatal("bad list caused retry loop or writes")
			}
		})
	}
}

func TestS3MultipartSaveLargeAndCleanupFailure(t *testing.T) {
	for _, scenario := range []string{"roundtrip", "abort", "abort-pending"} {
		t.Run(scenario, func(t *testing.T) {
			f, server := newMultipartHTTP(t)
			s := multipartClient(t, server, bytes.Repeat([]byte{1}, 32))
			data := multipartBytes()
			if scenario != "roundtrip" {
				f.failPart = 2
				f.failPartOnce = true
			}
			if scenario == "abort-pending" {
				f.abortFailure = true
			}
			err := s.Save(context.Background(), "large/文件", struct{ io.Reader }{bytes.NewReader(data)})
			if scenario == "roundtrip" {
				if err != nil {
					t.Fatal(err)
				}
				body, err := s.Load(context.Background(), "large/文件")
				if err != nil {
					t.Fatal(err)
				}
				got, err := io.ReadAll(body)
				_ = body.Close()
				if err != nil || !bytes.Equal(data, got) {
					t.Fatal("large multipart roundtrip failed")
				}
			} else {
				if err == nil {
					t.Fatal("failed multipart save succeeded")
				}
				if scenario == "abort-pending" {
					var failure *MultipartError
					if !errors.As(err, &failure) || failure.State.Phase != "abort_pending" {
						t.Fatalf("missing abort checkpoint: %v", err)
					}
				} else if len(f.uploads) != 0 {
					t.Fatal("failed save leaked upload")
				}
			}
			if s.spoolBytes != 0 || len(s.slots) != 0 {
				t.Fatal("upload leaked resource reservations")
			}
		})
	}
}

func TestS3MultipartCancelledAbortRetainsPendingState(t *testing.T) {
	f, server := newMultipartHTTP(t)
	s := multipartClient(t, server, bytes.Repeat([]byte{1}, 32))
	data := []byte("source")
	c, err := s.StartMultipart(context.Background(), "key", bytes.NewReader(data), int64(len(data)), WriteConditions{IfNoneMatch: "*"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, err = s.AbortMultipart(ctx, "key", c)
	if err == nil || !errors.Is(err, context.Canceled) || c.Phase != "abort_pending" {
		t.Fatalf("cancelled abort loses cleanup state: phase=%s err=%v", c.Phase, err)
	}
	if f.aborts != 0 {
		t.Fatal("cancelled abort made request")
	}
}

func TestS3MultipartConcurrentCompletionCAS(t *testing.T) {
	f, server := newMultipartHTTP(t)
	s := multipartClient(t, server, bytes.Repeat([]byte{6}, 32))
	inputs := [][]byte{[]byte("first"), []byte("second")}
	states := make([]MultipartCheckpoint, 2)
	for i := range inputs {
		var err error
		states[i], err = s.StartMultipart(context.Background(), "shared", bytes.NewReader(inputs[i]), int64(len(inputs[i])), WriteConditions{IfNoneMatch: "*"})
		if err != nil {
			t.Fatal(err)
		}
	}
	type result struct {
		i   int
		c   MultipartCheckpoint
		err error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for i := range inputs {
		go func(i int) {
			<-start
			c, err := s.ResumeMultipart(context.Background(), "shared", states[i], bytes.NewReader(inputs[i]))
			results <- result{i, c, err}
		}(i)
	}
	close(start)
	winner := -1
	var loser MultipartCheckpoint
	for range inputs {
		r := <-results
		if r.err == nil {
			if winner != -1 {
				t.Fatal("both concurrent conditional writes succeeded")
			}
			winner = r.i
		} else {
			loser = r.c
		}
	}
	if winner == -1 {
		t.Fatal("neither conditional write succeeded")
	}
	f.mu.Lock()
	got := append([]byte(nil), f.objects["shared"].data...)
	f.mu.Unlock()
	if !bytes.Equal(got, inputs[winner]) {
		t.Fatal("winner bytes were overwritten")
	}
	aborted, err := s.AbortMultipart(context.Background(), "shared", loser)
	if err != nil || aborted.Phase != "aborted" {
		t.Fatalf("loser cleanup: %s %v", aborted.Phase, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !bytes.Equal(f.objects["shared"].data, inputs[winner]) {
		t.Fatal("loser cleanup changed winner")
	}
}

func TestS3MultipartBoundedParallelPartsAndCancellation(t *testing.T) {
	for _, cancelUpload := range []bool{false, true} {
		t.Run(strconv.FormatBool(cancelUpload), func(t *testing.T) {
			f := &multipartHTTPFixture{objects: map[string]multipartHTTPObject{}, uploads: map[string]*multipartHTTPUpload{}, pageSize: 1000}
			started := make(chan struct{}, 10)
			release := make(chan struct{})
			var active, maximum atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPut {
					n := active.Add(1)
					defer active.Add(-1)
					for {
						prior := maximum.Load()
						if prior >= n || maximum.CompareAndSwap(prior, n) {
							break
						}
					}
					started <- struct{}{}
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
				}
				f.serveHTTP(w, r)
			}))
			defer server.Close()
			s := multipartClient(t, server, bytes.Repeat([]byte{4}, 32))
			s.cfg.PartConcurrency = 2
			data := multipartBytes()
			c, err := s.StartMultipart(context.Background(), "parallel", bytes.NewReader(data), int64(len(data)), WriteConditions{IfNoneMatch: "*"})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				var err error
				c, err = s.ResumeMultipart(ctx, "parallel", c, bytes.NewReader(data))
				done <- err
			}()
			for i := 0; i < 2; i++ {
				select {
				case <-started:
				case <-time.After(time.Second):
					close(release)
					t.Fatal("configured workers did not start")
				}
			}
			select {
			case <-started:
				close(release)
				t.Fatal("part concurrency cap exceeded")
			case <-time.After(20 * time.Millisecond):
			}
			if cancelUpload {
				cancel()
			} else {
				close(release)
			}
			select {
			case err = <-done:
			case <-time.After(time.Second):
				if cancelUpload {
					close(release)
				}
				t.Fatal("upload workers did not finish")
			}
			if cancelUpload {
				close(release)
				if err == nil || !errors.Is(err, context.Canceled) {
					t.Fatalf("cancelled upload: %v", err)
				}
				c, err = s.AbortMultipart(context.Background(), "parallel", c)
				if err != nil || c.Phase != "aborted" {
					t.Fatalf("cancelled upload cleanup: %s %v", c.Phase, err)
				}
			} else if err != nil || c.Phase != "complete" {
				t.Fatalf("parallel upload: %s %v", c.Phase, err)
			}
			if maximum.Load() != 2 {
				t.Fatalf("max workers=%d", maximum.Load())
			}
		})
	}
}

func TestS3MultipartEncryptedKMSRoundtrip(t *testing.T) {
	f, server := newMultipartHTTP(t)
	inner := multipartClient(t, server, bytes.Repeat([]byte{4}, 32))
	_, kms := newKMSProtocol(t)
	enc, err := NewEncryptedStorage(inner, kms, EncryptedStorageConfig{WriteKeyARN: testKeyARN, MaxObjectBytes: 16 << 20, SpoolDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	data := multipartBytes()
	key := "private/документ.bin"
	if err = enc.Save(context.Background(), key, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	cipher := append([]byte(nil), f.objects[key].data...)
	creates := f.creates
	f.mu.Unlock()
	if creates != 1 || !bytes.HasPrefix(cipher, []byte(encryptedMagic)) || bytes.Contains(cipher, data) {
		t.Fatal("multipart did not store a ciphertext envelope")
	}
	if got := encryptedReadAll(t, enc, key); !bytes.Equal(got, data) {
		t.Fatal("encrypted multipart roundtrip changed bytes")
	}
	if _, ok := any(enc).(interface {
		LoadRange(context.Context, string, int64, int64, ObjectSnapshot) (io.ReadCloser, error)
	}); ok {
		t.Fatal("encrypted storage exposes range bypass")
	}
	if _, ok := any(enc).(Presigner); ok {
		t.Fatal("encrypted storage exposes presign bypass")
	}
	if _, ok := any(enc).(interface {
		ResumeMultipart(context.Context, string, MultipartCheckpoint, io.ReaderAt) (MultipartCheckpoint, error)
	}); ok {
		t.Fatal("encrypted storage exposes unauthenticated direct multipart resume")
	}
	f.mu.Lock()
	obj := f.objects[key]
	obj.data[len(obj.data)-1] ^= 1
	f.objects[key] = obj
	f.mu.Unlock()
	if r, err := enc.Load(context.Background(), key); err == nil || r != nil {
		if r != nil {
			_ = r.Close()
		}
		t.Fatal("corrupt multipart ciphertext yielded plaintext reader")
	}
}

func TestS3MultipartRemoteMismatchPreservesRecoverableCheckpoint(t *testing.T) {
	for _, kind := range []string{"checksum", "size"} {
		t.Run(kind, func(t *testing.T) {
			f, server := newMultipartHTTP(t)
			s := multipartClient(t, server, bytes.Repeat([]byte{9}, 32))
			data := multipartBytes()
			c, err := s.StartMultipart(context.Background(), "recoverable", bytes.NewReader(data), int64(len(data)), WriteConditions{IfNoneMatch: "*"})
			if err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			f.failPart = 2
			f.failPartOnce = true
			f.mu.Unlock()
			c, err = s.ResumeMultipart(context.Background(), "recoverable", c, bytes.NewReader(data))
			if err == nil || c.Parts[0].ETag == "" {
				t.Fatalf("partial checkpoint missing: %v", err)
			}
			before, _ := json.Marshal(c)
			f.mu.Lock()
			part := f.uploads[c.UploadID].parts[1]
			if kind == "checksum" {
				part.checksum = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xab}, 32))
			} else {
				part.data = part.data[:len(part.data)-1]
			}
			f.uploads[c.UploadID].parts[1] = part
			puts := f.partPuts
			f.mu.Unlock()
			failed, err := s.ResumeMultipart(context.Background(), "recoverable", c, bytes.NewReader(data))
			if err == nil || !strings.Contains(err.Error(), "remote part does not match source") {
				t.Fatalf("remote mismatch error: %v", err)
			}
			encoded, err := json.Marshal(failed)
			if err != nil {
				t.Fatal(err)
			}
			restored, err := s.ParseMultipartCheckpoint("recoverable", encoded)
			if err != nil {
				t.Fatalf("error result lost checkpoint authentication: %v", err)
			}
			if !bytes.Equal(before, encoded) {
				t.Fatal("invalid remote state changed the authenticated checkpoint")
			}
			f.mu.Lock()
			unexpected := f.partPuts != puts || f.completes != 0
			f.mu.Unlock()
			if unexpected {
				t.Fatal("invalid remote state triggered writes")
			}
			aborted, err := s.AbortMultipart(context.Background(), "recoverable", restored)
			if err != nil || aborted.Phase != "aborted" {
				t.Fatalf("failed checkpoint cannot abort: %s %v", aborted.Phase, err)
			}
		})
	}
}

func TestS3MultipartSaveRetainsCleanupConfirmedCompletion(t *testing.T) {
	for _, scenario := range []string{"success", "cancelled", "source-cleanup"} {
		t.Run(scenario, func(t *testing.T) {
			cancelOriginal := scenario == "cancelled"
			f := &multipartHTTPFixture{objects: map[string]multipartHTTPObject{}, uploads: map[string]*multipartHTTPUpload{}, pageSize: 1000, lostComplete: true}
			var afterCompleteHeads atomic.Int32
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				f.mu.Lock()
				completed := f.completes > 0
				f.mu.Unlock()
				if r.Method == http.MethodHead && completed && afterCompleteHeads.Add(1) == 1 {
					if cancelOriginal {
						cancel()
					}
					w.WriteHeader(503)
					return
				}
				f.serveHTTP(w, r)
			}))
			defer server.Close()
			s := multipartClient(t, server, bytes.Repeat([]byte{5}, 32))
			data := multipartBytes()
			var source io.Reader = bytes.NewReader(data)
			cleanupErr := errors.New("injected source cleanup failure")
			if scenario == "source-cleanup" {
				source = multipartCleanupFailSource{Reader: bytes.NewReader(data), size: int64(len(data)), err: cleanupErr}
			}
			err := s.Save(ctx, "confirmed", source)
			if cancelOriginal {
				var stateErr *MultipartError
				if !errors.As(err, &stateErr) || stateErr.State.Phase != "complete" || !errors.Is(err, context.Canceled) {
					t.Fatalf("cancelled confirmed save lost completion state: %v", err)
				}
			} else if scenario == "source-cleanup" {
				if !errors.Is(err, cleanupErr) {
					t.Fatalf("source cleanup error lost: %v", err)
				}
			} else if err != nil {
				t.Fatalf("confirmed publication still reported as failed: %v", err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if !bytes.Equal(f.objects["confirmed"].data, data) || f.aborts != 0 || afterCompleteHeads.Load() != 2 {
				t.Fatalf("invalid confirmation evidence: aborts=%d heads=%d", f.aborts, afterCompleteHeads.Load())
			}
		})
	}
}

// prepareSource restores the caller's consumed offset after upload. This reader
// injects an error only in that final cleanup, after publication is confirmed.
type multipartCleanupFailSource struct {
	*bytes.Reader
	size int64
	err  error
}

func (r multipartCleanupFailSource) Seek(offset int64, whence int) (int64, error) {
	if whence == io.SeekStart && offset == r.size {
		return 0, r.err
	}
	return r.Reader.Seek(offset, whence)
}
