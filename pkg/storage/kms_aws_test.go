package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/credentials"
)

const testKeyARN = "arn:aws:kms:us-east-1:123456789012:key/11111111-1111-4111-8111-111111111111"
const testNewKeyARN = "arn:aws:kms:us-east-1:123456789012:key/22222222-2222-4222-8222-222222222222"

type kmsProtocolValue struct {
	key     []byte
	arn     string
	context map[string]string
}
type kmsProtocolServer struct {
	mu             sync.Mutex
	keys           map[string]kmsProtocolValue
	calls          map[string]int
	fail           bool
	delay          time.Duration
	lastContext    map[string]string
	lastPlaintexts [][]byte
}

func newKMSProtocol(t *testing.T) (*kmsProtocolServer, *AWSKMS) {
	t.Helper()
	p := &kmsProtocolServer{keys: map[string]kmsProtocolValue{}, calls: map[string]int{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action := strings.TrimPrefix(r.Header.Get("X-Amz-Target"), "TrentService.")
		if !strings.Contains(r.Header.Get("Authorization"), "Credential=LOCAL_ONLY_KEY/") || r.Header.Get("X-Amz-Security-Token") != "LOCAL_ONLY_TOKEN" {
			t.Error("official SDK did not sign request with test session credentials")
		}
		p.mu.Lock()
		p.calls[action]++
		fail, delay := p.fail, p.delay
		p.mu.Unlock()
		if delay > 0 {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(delay):
			}
		}
		if fail {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"__type":"AccessDeniedException","message":"SECRET_PROVIDER_RESPONSE"}`)
			return
		}
		var req struct {
			KeyID      string `json:"KeyId"`
			Plaintext  []byte
			Ciphertext []byte            `json:"CiphertextBlob"`
			Context    map[string]string `json:"EncryptionContext"`
			Algorithm  string            `json:"EncryptionAlgorithm"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		switch action {
		case "Encrypt":
			if len(req.Plaintext) != 32 || !validKMSContext(req.Context) || req.Algorithm != "SYMMETRIC_DEFAULT" {
				t.Error("invalid encrypt protocol request")
			}
			token := fmt.Sprintf("opaque-kms-ciphertext-%d", len(p.keys))
			p.keys[token] = kmsProtocolValue{append([]byte(nil), req.Plaintext...), req.KeyID, req.Context}
			p.lastContext = req.Context
			p.lastPlaintexts = append(p.lastPlaintexts, append([]byte(nil), req.Plaintext...))
			json.NewEncoder(w).Encode(map[string]any{"CiphertextBlob": []byte(token), "KeyId": req.KeyID, "EncryptionAlgorithm": "SYMMETRIC_DEFAULT"})
		case "Decrypt":
			value, ok := p.keys[string(req.Ciphertext)]
			if !ok || req.KeyID != value.arn || !reflect.DeepEqual(req.Context, value.context) || req.Algorithm != "SYMMETRIC_DEFAULT" {
				w.WriteHeader(400)
				fmt.Fprint(w, `{"__type":"InvalidCiphertextException"}`)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"Plaintext": value.key, "KeyId": value.arn, "EncryptionAlgorithm": "SYMMETRIC_DEFAULT"})
		case "DescribeKey":
			json.NewEncoder(w).Encode(map[string]any{"KeyMetadata": map[string]any{"Arn": req.KeyID, "Enabled": true, "KeyState": "Enabled", "KeySpec": "SYMMETRIC_DEFAULT", "KeyUsage": "ENCRYPT_DECRYPT", "CreationDate": 1700000000}})
		default:
			t.Errorf("unexpected AWS API %q", action)
			w.WriteHeader(400)
		}
	}))
	t.Cleanup(server.Close)
	client, err := NewAWSKMS(context.Background(), AWSKMSConfig{Region: "us-east-1", Endpoint: server.URL, AllowLocalHTTP: true, Credentials: credentials.NewStaticCredentialsProvider("LOCAL_ONLY_KEY", "LOCAL_ONLY_SECRET", "LOCAL_ONLY_TOKEN"), Timeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return p, client
}
func (p *kmsProtocolServer) count(name string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[name]
}
func TestAWSKMSProtocolContextRotationAndErrors(t *testing.T) {
	p, k := newKMSProtocol(t)
	ctx := context.Background()
	key := bytes.Repeat([]byte{7}, 32)
	wrapped, err := k.EncryptDataKey(ctx, EncryptDataKeyRequest{KeyRef: testKeyARN, Plaintext: key, Context: objectKMSContext("secret/customer.pdf")})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := k.DecryptDataKey(ctx, DecryptDataKeyRequest{CiphertextKeyRef: wrapped.CiphertextKeyRef, Context: objectKMSContext("secret/customer.pdf")})
	if err != nil || !bytes.Equal(plain.Plaintext, key) {
		t.Fatalf("decrypt=%v %v", plain, err)
	}
	if _, err := k.DecryptDataKey(ctx, DecryptDataKeyRequest{CiphertextKeyRef: wrapped.CiphertextKeyRef, Context: objectKMSContext("copied.pdf")}); err == nil {
		t.Fatal("KMS ignored context binding")
	}
	if _, err := k.RotateKeyRef(ctx, RotateKeyRefRequest{OldKeyRef: testKeyARN, NewKeyRef: testNewKeyARN}); err != nil {
		t.Fatal(err)
	}
	if p.count("RotateKeyOnDemand") != 0 {
		t.Fatal("reference rotation rotated AWS master key")
	}
	p.mu.Lock()
	raw, _ := json.Marshal(p.lastContext)
	p.fail = true
	p.mu.Unlock()
	if bytes.Contains(raw, []byte("secret")) || bytes.Contains(raw, []byte("customer")) {
		t.Fatal("context exposes object path")
	}
	if _, err := k.EncryptDataKey(ctx, EncryptDataKeyRequest{KeyRef: testKeyARN, Plaintext: key, Context: objectKMSContext("x")}); err == nil || strings.Contains(err.Error(), "SECRET_PROVIDER_RESPONSE") {
		t.Fatalf("provider error handling %v", err)
	}
}
func TestAWSKMSRejectsInvalidInputsBeforeRequest(t *testing.T) {
	p, k := newKMSProtocol(t)
	for _, key := range []string{"alias/current", "arn:aws:kms:us-east-1:123456789012:alias/current", "", "kms://key"} {
		if _, err := k.EncryptDataKey(context.Background(), EncryptDataKeyRequest{KeyRef: key, Plaintext: make([]byte, 32), Context: objectKMSContext("x")}); err == nil {
			t.Fatalf("invalid key accepted: %s", key)
		}
	}
	if _, err := k.EncryptDataKey(context.Background(), EncryptDataKeyRequest{KeyRef: testKeyARN, Plaintext: make([]byte, 32), Context: map[string]string{"path": "private/document"}}); err == nil {
		t.Fatal("plaintext context accepted")
	}
	if p.count("Encrypt") != 0 {
		t.Fatal("invalid request reached provider")
	}
	for _, endpoint := range []string{"http://kms.example.test", "https://name:secret@kms.example.test", "https://kms.example.test?secret=x"} {
		if _, err := NewAWSKMS(context.Background(), AWSKMSConfig{Region: "us-east-1", Endpoint: endpoint}); err == nil {
			t.Fatal("invalid endpoint accepted")
		}
	}
}
func TestAWSKMSTimeout(t *testing.T) {
	p, k := newKMSProtocol(t)
	p.mu.Lock()
	p.delay = time.Second
	p.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := k.EncryptDataKey(ctx, EncryptDataKeyRequest{KeyRef: testKeyARN, Plaintext: make([]byte, 32), Context: objectKMSContext("x")})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline lost: %v", err)
	}
}

func TestAWSKMSResponseBodyLimit(t *testing.T) {
	reader := &kmsResponseBody{ReadCloser: io.NopCloser(strings.NewReader(strings.Repeat("x", 128<<10))), remaining: 64 << 10}
	data, err := io.ReadAll(reader)
	if err == nil || len(data) != 64<<10 {
		t.Fatalf("unbounded KMS response: bytes=%d err=%v", len(data), err)
	}
}
