// Package main implements a load generator that replays pprof profile files
// against a live Pyroscope instance as part of an e2e benchmark suite.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/time/rate"
)

// Manifest describes the dataset directory layout.
type Manifest struct {
	Version  int             `json:"version"`
	Profiles []ProfileEntry  `json:"profiles"`
}

// ProfileEntry is a single profile descriptor from manifest.json.
type ProfileEntry struct {
	File        string            `json:"file"`
	ServiceName string            `json:"service_name"`
	ProfileType string            `json:"profile_type"`
	SampleType  string            `json:"sample_type"`
	Labels      map[string]string `json:"labels"`
}

// profileRecord is the in-memory representation of a loaded profile.
type profileRecord struct {
	entry ProfileEntry
	data  []byte
}

func main() {
	var (
		target         = flag.String("target", "http://localhost:4040", "Pyroscope URL")
		tenantID       = flag.String("tenant-id", "bench", "X-Scope-OrgID header value")
		dataset        = flag.String("dataset", "benchmarks/dataset", "Path to dataset directory containing manifest.json")
		ratePerSec     = flag.Int("rate", 20, "Target profiles per second")
		workers        = flag.Int("workers", 4, "Concurrent sender goroutines")
		duration       = flag.Duration("duration", 10*time.Minute, "Total run duration")
		errorThreshold = flag.Float64("error-threshold", 0.01, "Fail if error rate exceeds this fraction")
		metricsPort    = flag.Int("metrics-port", 2112, "Prometheus metrics port")
	)
	flag.Parse()

	// Load manifest.
	profiles, err := loadProfiles(*dataset)
	if err != nil {
		log.Fatalf("failed to load dataset: %v", err)
	}
	log.Printf("loaded %d profiles from %s", len(profiles), *dataset)

	// Set up Prometheus metrics.
	reg := prometheus.NewRegistry()
	sentTotal := promauto.With(reg).NewCounter(prometheus.CounterOpts{
		Name: "bench_profiles_sent_total",
		Help: "Total number of profiles successfully sent.",
	})
	failedTotal := promauto.With(reg).NewCounter(prometheus.CounterOpts{
		Name: "bench_profiles_failed_total",
		Help: "Total number of profiles that failed to send.",
	})
	pushDuration := promauto.With(reg).NewHistogram(prometheus.HistogramOpts{
		Name:    "bench_push_duration_seconds",
		Help:    "Duration of profile push requests.",
		Buckets: []float64{0.010, 0.025, 0.050, 0.100, 0.250, 0.500, 1.0, 2.5},
	})

	// Start metrics server.
	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	metricsServer := &http.Server{
		Addr:    fmt.Sprintf(":%d", *metricsPort),
		Handler: metricsMux,
	}
	go func() {
		if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("metrics server error: %v", err)
		}
	}()
	log.Printf("metrics available at http://localhost:%d/metrics", *metricsPort)

	// Shuffle profiles so workers see a varied sequence.
	shuffled := make([]profileRecord, len(profiles))
	copy(shuffled, profiles)
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })

	// Rate limiter shared across workers.
	limiter := rate.NewLimiter(rate.Limit(*ratePerSec), *ratePerSec)

	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()

	// Work channel: index into shuffled slice (round-robin, wrapping).
	work := make(chan int, *workers*2)

	// Producer: feed indices to workers until duration expires.
	go func() {
		defer close(work)
		idx := 0
		for {
			if err := limiter.Wait(ctx); err != nil {
				// Context cancelled or deadline exceeded — stop producing.
				return
			}
			select {
			case work <- idx % len(shuffled):
				idx++
			case <-ctx.Done():
				return
			}
		}
	}()

	var sent, failed int64
	var wg sync.WaitGroup
	client := &http.Client{Timeout: 30 * time.Second}

	for w := 0; w < *workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range work {
				rec := shuffled[idx]
				start := time.Now()
				pushErr := sendProfile(client, *target, *tenantID, rec)
				elapsed := time.Since(start).Seconds()
				pushDuration.Observe(elapsed)
				if pushErr != nil {
					log.Printf("push error: %v", pushErr)
					failedTotal.Inc()
					atomic.AddInt64(&failed, 1)
				} else {
					sentTotal.Inc()
					atomic.AddInt64(&sent, 1)
				}
			}
		}()
	}

	wg.Wait()
	metricsServer.Close()

	totalSent := atomic.LoadInt64(&sent)
	totalFailed := atomic.LoadInt64(&failed)
	total := totalSent + totalFailed

	log.Printf("done: sent=%d failed=%d total=%d", totalSent, totalFailed, total)

	if total == 0 {
		log.Println("no profiles were attempted")
		os.Exit(1)
	}

	errorRate := float64(totalFailed) / float64(total)
	log.Printf("error rate: %.4f (threshold: %.4f)", errorRate, *errorThreshold)
	if errorRate > *errorThreshold {
		log.Fatalf("error rate %.4f exceeds threshold %.4f", errorRate, *errorThreshold)
	}
}

// loadProfiles reads manifest.json from datasetDir and loads each referenced
// pprof file into memory.
func loadProfiles(datasetDir string) ([]profileRecord, error) {
	manifestPath := filepath.Join(datasetDir, "manifest.json")
	f, err := os.Open(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("open manifest: %w", err)
	}
	defer f.Close()

	var m Manifest
	if err := json.NewDecoder(f).Decode(&m); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}

	if len(m.Profiles) == 0 {
		return nil, fmt.Errorf("manifest contains no profiles")
	}

	records := make([]profileRecord, 0, len(m.Profiles))
	for _, entry := range m.Profiles {
		path := filepath.Join(datasetDir, entry.File)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read profile %s: %w", path, err)
		}
		records = append(records, profileRecord{entry: entry, data: data})
	}
	return records, nil
}

// encodedLabels returns the Pyroscope label string appended to the service
// name, e.g. "goapp-http{namespace=bench,version=v1}".
func encodedLabels(entry ProfileEntry) string {
	if len(entry.Labels) == 0 {
		return entry.ServiceName
	}
	parts := make([]string, 0, len(entry.Labels))
	for k, v := range entry.Labels {
		parts = append(parts, k+"="+v)
	}
	return entry.ServiceName + "{" + strings.Join(parts, ",") + "}"
}

// sendProfile posts a single pprof profile to the Pyroscope /ingest endpoint.
func sendProfile(client *http.Client, target, tenantID string, rec profileRecord) error {
	now := time.Now().Unix()
	from := now - 10
	until := now

	appName := encodedLabels(rec.entry)
	ingestURL := fmt.Sprintf(
		"%s/ingest?name=%s&sampleRate=100&spyName=gospy&format=pprof&from=%d&until=%d",
		target,
		appName,
		from,
		until,
	)

	// Build multipart body with the raw pprof bytes.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("profile", "profile.pprof")
	if err != nil {
		return fmt.Errorf("create form file: %w", err)
	}
	if _, err := io.Copy(fw, bytes.NewReader(rec.data)); err != nil {
		return fmt.Errorf("write profile data: %w", err)
	}
	if err := mw.Close(); err != nil {
		return fmt.Errorf("close multipart writer: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, ingestURL, &buf)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Scope-OrgID", tenantID)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()
	// Drain body to allow connection reuse.
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
}
