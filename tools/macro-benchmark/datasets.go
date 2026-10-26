package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

type datasetPreset struct {
	URL       string `yaml:"url"`
	SizeBytes int64  `yaml:"size_bytes"`
}

func (c *inputsConfig) resolveDataset() error {
	if c.Dataset == "" {
		return nil
	}
	if c.Fixture != "" || c.FixtureURL != "" {
		return errors.New("dataset cannot be combined with fixture or fixture_url")
	}
	preset, ok := defaultDatasets()[c.Dataset]
	if !ok {
		return fmt.Errorf("unknown dataset %q", c.Dataset)
	}
	c.FixtureURL, c.FixtureSizeBytes = preset.URL, preset.SizeBytes
	return nil
}

// Check the pinned GCS object's metadata without reading its body. profilecli
// streams the GET response directly; the harness never spools it to disk or
// claims to have independently verified the complete body checksum.
func inspectFixture(ctx context.Context, cfg inputsConfig) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, cfg.FixtureURL, nil)
	if err != nil {
		return nil, err
	}
	generation := request.URL.Query().Get("generation")
	if generation == "" {
		return nil, errors.New("streamed fixture requires a pinned GCS generation")
	}
	request.Header.Set("Accept-Encoding", "identity")
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || len(via) >= 10 {
			return errors.New("unsafe fixture redirect")
		}
		return nil
	}}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fixture metadata: HTTP %d", response.StatusCode)
	}
	if response.Header.Get("X-Goog-Generation") != generation {
		return nil, errors.New("fixture generation metadata mismatch")
	}
	if response.ContentLength != cfg.FixtureSizeBytes {
		return nil, fmt.Errorf("fixture metadata size %d, expected %d", response.ContentLength, cfg.FixtureSizeBytes)
	}
	return map[string]any{
		"dataset": cfg.Dataset, "url": cfg.FixtureURL, "generation": generation,
		"size_bytes": response.ContentLength,
		"delivery": "direct-http-stream", "verification": "gcs-head-metadata",
		"body_checksum_verified": false,
	}, nil
}
