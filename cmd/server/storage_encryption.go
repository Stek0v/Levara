package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stek0v/levara/pkg/storage"
)

type storageEncryptionConfig struct {
	enabled bool
	kms     storage.AWSKMSConfig
	cache   storage.KMSCacheConfig
	objects storage.EncryptedStorageConfig
}

func storageEncryptionFromEnv(env func(string) string) (storageEncryptionConfig, error) {
	var cfg storageEncryptionConfig
	mode := strings.TrimSpace(env("STORAGE_ENCRYPTION"))
	if mode != "" && mode != "aws-kms" {
		return cfg, errors.New("STORAGE_ENCRYPTION must be aws-kms or empty")
	}
	cfg.enabled = mode == "aws-kms"
	keys := []string{"KMS_KEY_ARN", "KMS_READ_KEY_ARNS", "KMS_REGION", "KMS_ENDPOINT", "KMS_TIMEOUT", "KMS_CACHE_TTL", "STORAGE_ENCRYPTION_MAX_OBJECT_BYTES", "STORAGE_ENCRYPTION_MAX_SPOOL_BYTES", "STORAGE_ENCRYPTION_MAX_IN_FLIGHT", "STORAGE_ENCRYPTION_MAX_WAITERS", "STORAGE_ENCRYPTION_TIMEOUT", "STORAGE_ENCRYPTION_SPOOL_DIRECTORY"}
	if !cfg.enabled {
		for _, key := range keys {
			if env(key) != "" {
				return cfg, fmt.Errorf("%s requires STORAGE_ENCRYPTION=aws-kms", key)
			}
		}
		return cfg, nil
	}
	cfg.objects.WriteKeyARN = strings.TrimSpace(env("KMS_KEY_ARN"))
	if cfg.objects.WriteKeyARN == "" {
		return cfg, errors.New("KMS_KEY_ARN is required")
	}
	if value := env("KMS_READ_KEY_ARNS"); value != "" {
		for _, key := range strings.Split(value, ",") {
			key = strings.TrimSpace(key)
			if key == "" {
				return cfg, errors.New("KMS_READ_KEY_ARNS contains an empty key")
			}
			cfg.objects.AllowedReadKeyARNs = append(cfg.objects.AllowedReadKeyARNs, key)
		}
	}
	cfg.kms.Region, cfg.kms.Endpoint = env("KMS_REGION"), env("KMS_ENDPOINT")
	cfg.objects.SpoolDirectory = env("STORAGE_ENCRYPTION_SPOOL_DIRECTORY")
	for key, target := range map[string]*time.Duration{"KMS_TIMEOUT": &cfg.kms.Timeout, "KMS_CACHE_TTL": &cfg.cache.TTL, "STORAGE_ENCRYPTION_TIMEOUT": &cfg.objects.Timeout} {
		if value := env(key); value != "" {
			d, err := time.ParseDuration(value)
			if err != nil || d < 0 || (d == 0 && key != "KMS_CACHE_TTL") {
				return cfg, fmt.Errorf("invalid %s duration", key)
			}
			*target = d
		}
	}
	for key, target := range map[string]*int64{"STORAGE_ENCRYPTION_MAX_OBJECT_BYTES": &cfg.objects.MaxObjectBytes, "STORAGE_ENCRYPTION_MAX_SPOOL_BYTES": &cfg.objects.MaxSpoolBytes} {
		if value := env(key); value != "" {
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil || n < 1 {
				return cfg, fmt.Errorf("invalid %s byte limit", key)
			}
			*target = n
		}
	}
	for key, target := range map[string]*int{"STORAGE_ENCRYPTION_MAX_IN_FLIGHT": &cfg.objects.MaxInFlight, "STORAGE_ENCRYPTION_MAX_WAITERS": &cfg.objects.MaxWaiters} {
		if value := env(key); value != "" {
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 {
				return cfg, fmt.Errorf("invalid %s count limit", key)
			}
			*target = n
		}
	}
	for _, err := range []error{cfg.kms.Validate(), cfg.cache.Validate(), cfg.objects.Validate()} {
		if err != nil {
			return cfg, err
		}
	}
	return cfg, nil
}

func configureStorageEncryption(ctx context.Context, backend storage.Storage) (storage.Storage, error) {
	cfg, err := storageEncryptionFromEnv(os.Getenv)
	if err != nil || !cfg.enabled {
		return backend, err
	}
	kms, err := storage.NewAWSKMS(ctx, cfg.kms)
	if err != nil {
		return nil, err
	}
	// Validate the write key at startup. An unavailable old read key must not
	// prevent writes with the selected key; reads still fail closed per object.
	if _, err = kms.KeyMetadata(ctx, cfg.objects.WriteKeyARN); err != nil {
		return nil, err
	}
	return wrapEncryptedStorage(backend, kms, cfg, prometheus.DefaultRegisterer)
}

func wrapEncryptedStorage(backend storage.Storage, kms storage.KMS, cfg storageEncryptionConfig, registry prometheus.Registerer) (storage.Storage, error) {
	cache, err := storage.NewCachedKMS(kms, cfg.cache)
	if err != nil {
		return nil, err
	}
	encrypted, err := storage.NewEncryptedStorage(backend, cache, cfg.objects)
	if err != nil {
		return nil, err
	}
	metrics := []prometheus.Collector{
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "levara_storage_encryption_in_flight", Help: "Active encrypted object operations."}, func() float64 { return float64(encrypted.Stats().InFlight) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "levara_storage_encryption_waiters", Help: "Encrypted object operations waiting for capacity."}, func() float64 { return float64(encrypted.Stats().Waiters) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "levara_storage_encryption_reserved_spool_bytes", Help: "Ciphertext spool capacity reserved by active operations."}, func() float64 { return float64(encrypted.Stats().ReservedSpoolBytes) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "levara_storage_kms_cache_entries", Help: "Live decrypted key cache entries."}, func() float64 { return float64(cache.Stats().Entries) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "levara_storage_kms_cache_bytes", Help: "Bytes accounted to decrypted key cache entries."}, func() float64 { return float64(cache.Stats().Bytes) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{Name: "levara_storage_kms_cache_hits_total", Help: "Decrypted key cache hits."}, func() float64 { return float64(cache.Stats().Hits) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{Name: "levara_storage_kms_cache_misses_total", Help: "Decrypted key cache misses."}, func() float64 { return float64(cache.Stats().Misses) }),
	}
	for i, metric := range metrics {
		if err := registry.Register(metric); err != nil {
			for _, registered := range metrics[:i] {
				registry.Unregister(registered)
			}
			cache.Clear()
			return nil, fmt.Errorf("storage encryption metrics: %w", err)
		}
	}
	return encrypted, nil
}
