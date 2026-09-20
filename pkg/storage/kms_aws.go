package storage

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/config"
	awskms "github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/aws/smithy-go/logging"
)

// AWSKMSConfig uses the official SDK credential chain unless Credentials is
// explicitly supplied (normally only in local protocol tests). Aliases are not
// key references: callers must select immutable KMS key ARNs.
type AWSKMSConfig struct {
	Region         string
	Endpoint       string
	Credentials    aws.CredentialsProvider
	Timeout        time.Duration
	AllowLocalHTTP bool
}

type AWSKMS struct {
	client  *awskms.Client
	timeout time.Duration
}

// Validate checks local configuration without resolving AWS credentials.
func (cfg AWSKMSConfig) Validate() error {
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.Timeout < time.Millisecond || cfg.Timeout > time.Minute {
		return errors.New("kms: timeout must be between 1ms and 1m")
	}
	if cfg.Endpoint != "" {
		u, err := url.Parse(cfg.Endpoint)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return errors.New("kms: invalid endpoint")
		}
		if u.Scheme != "https" && (!cfg.AllowLocalHTTP || u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "::1" && u.Hostname() != "localhost")) {
			return errors.New("kms: endpoint requires HTTPS")
		}
	}
	return nil
}

func NewAWSKMS(ctx context.Context, cfg AWSKMSConfig) (*AWSKMS, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	opts := []func(*config.LoadOptions) error{config.WithLogger(logging.Nop{}), config.WithClientLogMode(0), config.WithRetryMaxAttempts(2), config.WithHTTPClient(&http.Client{Timeout: cfg.Timeout, Transport: kmsResponseTransport{http.DefaultTransport}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }})}
	if cfg.Region != "" {
		opts = append(opts, config.WithRegion(cfg.Region))
	}
	if cfg.Credentials != nil {
		opts = append(opts, config.WithCredentialsProvider(cfg.Credentials))
	}
	sdk, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, kmsFailure(ctx, "configuration")
	}
	if sdk.Region == "" {
		return nil, errors.New("kms: AWS region is required")
	}
	client := awskms.NewFromConfig(sdk, func(o *awskms.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
		o.Logger = logging.Nop{}
		o.ClientLogMode = 0
	})
	return &AWSKMS{client: client, timeout: cfg.Timeout}, nil
}

func validKeyARN(key string) bool {
	a, err := arn.Parse(key)
	if err != nil || a.Service != "kms" || a.Region == "" || len(a.AccountID) != 12 || !strings.HasPrefix(a.Resource, "key/") {
		return false
	}
	for _, c := range a.AccountID {
		if c < '0' || c > '9' {
			return false
		}
	}
	id := strings.TrimPrefix(a.Resource, "key/")
	if id == "" || len(key) > 2048 || strings.ContainsAny(id, "/: \t\r\n") {
		return false
	}
	return a.Partition == "aws" || a.Partition == "aws-cn" || a.Partition == "aws-us-gov"
}

// Encryption context is visible in CloudTrail. Restrict it to nonsecret
// SHA-256 values; object names, credentials and plaintext never enter it.
func validKMSContext(values map[string]string) bool {
	if len(values) != 2 {
		return false
	}
	for _, name := range []string{"object_sha256", "format_sha256"} {
		v, ok := values[name]
		if !ok || len(v) != 64 {
			return false
		}
		if _, err := hex.DecodeString(v); err != nil {
			return false
		}
	}
	return true
}

type awsWrappedKey struct {
	Version    int    `json:"v"`
	KeyARN     string `json:"key_arn"`
	Ciphertext []byte `json:"ciphertext"`
}

func decodeAWSWrappedKey(ref string) (awsWrappedKey, error) {
	var key awsWrappedKey
	if len(ref) > 16<<10 {
		return key, errors.New("kms: wrapped key exceeds limit")
	}
	data, err := base64.StdEncoding.Strict().DecodeString(ref)
	if err != nil {
		return key, errors.New("kms: invalid wrapped key encoding")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err = d.Decode(&key); err != nil {
		return key, errors.New("kms: invalid wrapped key")
	}
	canonical, _ := json.Marshal(key)
	if !bytes.Equal(canonical, data) || key.Version != 1 || !validKeyARN(key.KeyARN) || len(key.Ciphertext) == 0 || len(key.Ciphertext) > 6144 {
		return key, errors.New("kms: invalid wrapped key fields")
	}
	return key, nil
}

func kmsFailure(ctx context.Context, operation string) error {
	// SDK errors may echo a provider response. Never include arbitrary provider
	// text or request payload in logs; cancellation remains machine-readable.
	return errors.Join(fmt.Errorf("kms: %s failed", operation), ctx.Err())
}
func (k *AWSKMS) EncryptDataKey(ctx context.Context, req EncryptDataKeyRequest) (EncryptDataKeyResponse, error) {
	if !validKeyARN(req.KeyRef) || len(req.Plaintext) != 32 || !validKMSContext(req.Context) || (req.Algorithm != "" && req.Algorithm != "SYMMETRIC_DEFAULT") {
		return EncryptDataKeyResponse{}, errors.New("kms: invalid encrypt request")
	}
	ctx, cancel := context.WithTimeout(ctx, k.timeout)
	defer cancel()
	result, err := k.client.Encrypt(ctx, &awskms.EncryptInput{KeyId: aws.String(req.KeyRef), Plaintext: req.Plaintext, EncryptionContext: req.Context, EncryptionAlgorithm: types.EncryptionAlgorithmSpecSymmetricDefault})
	if err != nil {
		return EncryptDataKeyResponse{}, kmsFailure(ctx, "encrypt")
	}
	if aws.ToString(result.KeyId) != req.KeyRef || result.EncryptionAlgorithm != types.EncryptionAlgorithmSpecSymmetricDefault || len(result.CiphertextBlob) == 0 || len(result.CiphertextBlob) > 6144 {
		return EncryptDataKeyResponse{}, errors.New("kms: invalid encrypt response")
	}
	raw, _ := json.Marshal(awsWrappedKey{Version: 1, KeyARN: req.KeyRef, Ciphertext: result.CiphertextBlob})
	return EncryptDataKeyResponse{CiphertextKeyRef: base64.StdEncoding.EncodeToString(raw), KeyRef: req.KeyRef, Algorithm: "SYMMETRIC_DEFAULT"}, nil
}
func (k *AWSKMS) DecryptDataKey(ctx context.Context, req DecryptDataKeyRequest) (DecryptDataKeyResponse, error) {
	wrapped, err := decodeAWSWrappedKey(req.CiphertextKeyRef)
	if err != nil {
		return DecryptDataKeyResponse{}, err
	}
	if !validKMSContext(req.Context) {
		return DecryptDataKeyResponse{}, errors.New("kms: invalid decrypt context")
	}
	ctx, cancel := context.WithTimeout(ctx, k.timeout)
	defer cancel()
	result, err := k.client.Decrypt(ctx, &awskms.DecryptInput{KeyId: aws.String(wrapped.KeyARN), CiphertextBlob: wrapped.Ciphertext, EncryptionContext: req.Context, EncryptionAlgorithm: types.EncryptionAlgorithmSpecSymmetricDefault})
	if err != nil {
		return DecryptDataKeyResponse{}, kmsFailure(ctx, "decrypt")
	}
	if aws.ToString(result.KeyId) != wrapped.KeyARN || len(result.Plaintext) != 32 || result.EncryptionAlgorithm != types.EncryptionAlgorithmSpecSymmetricDefault {
		clear(result.Plaintext)
		return DecryptDataKeyResponse{}, errors.New("kms: invalid decrypt response")
	}
	return DecryptDataKeyResponse{Plaintext: result.Plaintext, KeyRef: wrapped.KeyARN}, nil
}
func (k *AWSKMS) KeyMetadata(ctx context.Context, keyRef string) (KeyMetadata, error) {
	if !validKeyARN(keyRef) {
		return KeyMetadata{}, errors.New("kms: immutable key ARN required")
	}
	ctx, cancel := context.WithTimeout(ctx, k.timeout)
	defer cancel()
	result, err := k.client.DescribeKey(ctx, &awskms.DescribeKeyInput{KeyId: aws.String(keyRef)})
	if err != nil {
		return KeyMetadata{}, kmsFailure(ctx, "describe key")
	}
	meta := result.KeyMetadata
	if meta == nil || aws.ToString(meta.Arn) != keyRef || !meta.Enabled || meta.KeyState != types.KeyStateEnabled || meta.KeySpec != types.KeySpecSymmetricDefault || meta.KeyUsage != types.KeyUsageTypeEncryptDecrypt {
		return KeyMetadata{}, errors.New("kms: key must be enabled symmetric ENCRYPT_DECRYPT")
	}
	return KeyMetadata{KeyRef: keyRef, Provider: "aws-kms", Algorithm: "SYMMETRIC_DEFAULT", CreatedAt: aws.ToTime(meta.CreationDate)}, nil
}
func (k *AWSKMS) RotateKeyRef(ctx context.Context, req RotateKeyRefRequest) (RotateKeyRefResponse, error) {
	if req.ObjectPath != "" {
		return RotateKeyRefResponse{}, errors.New("kms: object re-encryption is not a key-reference rotation")
	}
	if !validKeyARN(req.OldKeyRef) {
		return RotateKeyRefResponse{}, errors.New("kms: immutable previous key ARN required")
	}
	if _, err := k.KeyMetadata(ctx, req.NewKeyRef); err != nil {
		return RotateKeyRefResponse{}, err
	}
	// Selects a reference only; never invokes AWS master-key rotation APIs.
	return RotateKeyRefResponse{KeyRef: req.NewKeyRef}, nil
}

// KMS responses are small (data-key plaintext is exactly 32 bytes). Bound
// provider/error bodies before the SDK decodes them to cap response allocation.
type kmsResponseTransport struct{ inner http.RoundTripper }

func (t kmsResponseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := t.inner.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if response.ContentLength > 64<<10 {
		response.Body.Close()
		return nil, errors.New("kms: response exceeds size limit")
	}
	response.Body = &kmsResponseBody{ReadCloser: response.Body, remaining: 64 << 10}
	return response, nil
}

type kmsResponseBody struct {
	io.ReadCloser
	remaining int64
}

func (r *kmsResponseBody) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, errors.New("kms: response exceeds size limit")
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.ReadCloser.Read(p)
	r.remaining -= int64(n)
	return n, err
}
