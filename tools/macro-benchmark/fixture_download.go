package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Keep downloads atomic too: interruption must never publish an incomplete
// recording as the input to a subsequent replay.
func storeFixture(reader io.Reader, cfg inputsConfig, dest string) (map[string]any, error) {
	if cfg.FixtureSizeBytes <= 0 || cfg.FixtureSizeBytes > 1<<40 {
		return nil, errors.New("invalid fixture size")
	}
	file, err := os.CreateTemp(filepath.Dir(dest), ".fixture-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	sha, crc := sha256.New(), crc32.New(crc32.MakeTable(crc32.Castagnoli))
	n, err := io.Copy(io.MultiWriter(file, sha, crc), io.LimitReader(reader, cfg.FixtureSizeBytes+1))
	if err != nil {
		return nil, err
	}
	if n != cfg.FixtureSizeBytes || base64.StdEncoding.EncodeToString(crc.Sum(nil)) != cfg.FixtureCRC32C {
		return nil, errors.New("fixture size or CRC32C mismatch")
	}
	if err := file.Sync(); err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(file.Name(), dest); err != nil {
		return nil, err
	}
	return map[string]any{"dataset": cfg.Dataset, "url": cfg.FixtureURL, "size_bytes": n, "crc32c": cfg.FixtureCRC32C, "sha256": hex.EncodeToString(sha.Sum(nil)), "body_checksum_verified": true}, nil
}

func downloadFixture(ctx context.Context, cfg inputsConfig, dest string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Hour)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.FixtureURL, nil)
	if err != nil {
		return nil, err
	}
	if req.URL.Scheme != "https" || req.URL.User != nil {
		return nil, errors.New("fixture requires HTTPS without credentials")
	}
	req.Header.Set("Accept-Encoding", "identity")
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || req.URL.User != nil || len(via) >= 10 {
			return errors.New("unsafe fixture redirect")
		}
		return nil
	}}
	response, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fixture download: HTTP %d", response.StatusCode)
	}
	return storeFixture(response.Body, cfg, dest)
}
