// Package benchutil provides the HTTP harness shared by macro benchmarks.
package benchutil

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

type Config struct {
	URL, Tenant, Start, End, Selector string
}

// FromEnv skips benchmarks unless a target is explicitly supplied.
func FromEnv(b *testing.B) Config {
	b.Helper()
	endpoint := os.Getenv("PYROSCOPE_URL")
	if endpoint == "" {
		b.Skip("set PYROSCOPE_URL to run the macro benchmark")
	}
	required := func(key string) string {
		value := os.Getenv(key)
		if value == "" {
			b.Fatalf("%s is required", key)
		}
		return value
	}
	c := Config{URL: endpoint, Tenant: required("TENANT_ID"), Start: required("START_MS"), End: required("END_MS"), Selector: required("LABEL_SELECTOR")}
	start, err := strconv.ParseInt(c.Start, 10, 64)
	if err != nil {
		b.Fatalf("invalid START_MS: %v", err)
	}
	end, err := strconv.ParseInt(c.End, 10, 64)
	if err != nil || end <= start {
		b.Fatal("END_MS must be an integer greater than START_MS")
	}
	return c
}

func (c Config) MetadataBody() map[string]any {
	return map[string]any{"matchers": []string{c.Selector}, "start": c.Start, "end": c.End}
}

func (c Config) ProfileBody(b *testing.B) map[string]any {
	b.Helper()
	profileType := os.Getenv("PROFILE_TYPE")
	if profileType == "" {
		b.Fatal("PROFILE_TYPE is required")
	}
	return map[string]any{"profileTypeID": profileType, "labelSelector": c.Selector, "start": c.Start, "end": c.End}
}

// Run warms up once, then measures sequential requests including response validation.
// A zero response limit defaults to 64 MiB.
func Run(b *testing.B, c Config, method string, body map[string]any, requiredField string, maxResponseBytes int64) {
	b.Helper()
	if maxResponseBytes == 0 {
		maxResponseBytes = 64 << 20
	}
	payload, err := json.Marshal(body)
	if err != nil {
		b.Fatal(err)
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	b.Cleanup(client.CloseIdleConnections)
	var responseBytes int64
	call := func() {
		b.Helper()
		req, err := http.NewRequestWithContext(b.Context(), http.MethodPost, strings.TrimRight(c.URL, "/")+"/querier.v1.QuerierService/"+method, bytes.NewReader(payload))
		if err != nil {
			b.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Connect-Protocol-Version", "1")
		req.Header.Set("X-Scope-OrgID", c.Tenant)
		resp, err := client.Do(req)
		if err != nil {
			b.Fatal(err)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
		closeErr := resp.Body.Close()
		if err != nil {
			b.Fatal(err)
		}
		if closeErr != nil {
			b.Fatal(closeErr)
		}
		if int64(len(data)) > maxResponseBytes {
			b.Fatalf("response exceeds %d bytes", maxResponseBytes)
		}
		if resp.StatusCode != http.StatusOK {
			b.Fatalf("query HTTP status %d", resp.StatusCode)
		}
		if err := validateResponse(data, requiredField); err != nil {
			b.Fatal(err)
		}
		responseBytes += int64(len(data))
	}
	call()
	responseBytes = 0
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		call()
	}
	b.StopTimer()
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "queries/s")
	b.ReportMetric(float64(responseBytes)/float64(b.N), "response-B/op")
}

func validateResponse(body []byte, field string) error {
	var result map[string]json.RawMessage
	if err := json.Unmarshal(body, &result); err != nil {
		return err
	}
	switch strings.TrimSpace(string(result[field])) {
	case "", "null", "[]", "{}", `""`:
		return fmt.Errorf("empty response field %q", field)
	}
	return nil
}
