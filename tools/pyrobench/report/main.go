// Package main implements a benchmark reporting tool that collects metrics from
// Prometheus at the end of a benchmark run and produces JSON and Markdown reports.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Report is the root JSON document produced in collect mode and consumed in render mode.
type Report struct {
	CollectedAt   time.Time        `json:"collected_at"`
	PrometheusURL string           `json:"prometheus_url"`
	Metrics       map[string]float64 `json:"metrics"`
	Resources     []ResourceEntry  `json:"resources"`
}

// ResourceEntry holds peak CPU and memory measurements for a single component.
type ResourceEntry struct {
	Component       string  `json:"component"`
	PeakCPUCores    float64 `json:"peak_cpu_cores"`
	PeakMemoryBytes float64 `json:"peak_memory_bytes"`
}

// promResponse mirrors the minimal subset of the Prometheus HTTP API JSON envelope
// needed to extract an instant-query scalar or vector result.
type promResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  [2]json.RawMessage `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

func main() {
	var (
		prometheusURL = flag.String("prometheus", "", "Prometheus URL for live collection (mutually exclusive with --input)")
		outputPath    = flag.String("output", "bench-results/metrics.json", "Output path for JSON report")
		inputPath     = flag.String("input", "", "Input JSON report path (for rendering)")
		format        = flag.String("format", "json", "Output format: \"json\" or \"markdown\"")
		lookback      = flag.Duration("lookback", time.Hour, "How far back to query max_over_time")
	)
	flag.Parse()

	if *prometheusURL != "" && *inputPath != "" {
		fmt.Fprintln(os.Stderr, "error: --prometheus and --input are mutually exclusive")
		os.Exit(1)
	}

	switch {
	case *prometheusURL != "":
		if err := collect(*prometheusURL, *outputPath, *lookback); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	case *inputPath != "":
		report, err := readReport(*inputPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		switch *format {
		case "markdown":
			printMarkdown(report)
		default:
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(report); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
		}
	default:
		fmt.Fprintln(os.Stderr, "error: one of --prometheus or --input is required")
		flag.Usage()
		os.Exit(1)
	}
}

// collect queries Prometheus and writes the JSON report to outputPath.
func collect(prometheusURL, outputPath string, lookback time.Duration) error {
	q := &querier{baseURL: prometheusURL, client: &http.Client{Timeout: 30 * time.Second}}

	report := Report{
		CollectedAt:   time.Now().UTC(),
		PrometheusURL: prometheusURL,
		Metrics:       make(map[string]float64),
	}

	// Scalar / rate metrics.
	scalarQueries := []struct {
		expr string
		key  string
	}{
		{"bench:ingest_rate:rate1m", "ingest_rate"},
		{"bench:push_p99:rate1m", "push_p99_seconds"},
		{"bench:query_p99:rate1m", "query_p99_seconds"},
		{"bench:error_rate:rate1m", "error_rate"},
		{"rate(bench_profiles_sent_total[5m])", "profiles_sent_rate"},
		{"rate(bench_profiles_failed_total[5m])", "profiles_failed_rate"},
	}

	for _, sq := range scalarQueries {
		val, err := q.queryScalar(sq.expr)
		if err != nil {
			return fmt.Errorf("querying %q: %w", sq.expr, err)
		}
		report.Metrics[sq.key] = val
	}

	// Peak resource queries – max by container over lookback window.
	lookbackStr := formatDuration(lookback)
	memExpr := fmt.Sprintf(
		`max by (container) (max_over_time(container_memory_working_set_bytes{namespace="pyroscope-bench"}[%s]))`,
		lookbackStr,
	)
	cpuExpr := fmt.Sprintf(
		`max by (container) (max_over_time(rate(container_cpu_usage_seconds_total{namespace="pyroscope-bench"}[1m])[%s:1m]))`,
		lookbackStr,
	)

	memResults, err := q.queryVector(memExpr)
	if err != nil {
		return fmt.Errorf("querying memory: %w", err)
	}
	cpuResults, err := q.queryVector(cpuExpr)
	if err != nil {
		return fmt.Errorf("querying cpu: %w", err)
	}

	// Build a map of component -> ResourceEntry.
	resources := make(map[string]*ResourceEntry)
	for container, val := range memResults {
		e := resourceEntry(resources, container)
		e.PeakMemoryBytes = val
	}
	for container, val := range cpuResults {
		e := resourceEntry(resources, container)
		e.PeakCPUCores = val
	}
	for _, e := range resources {
		report.Resources = append(report.Resources, *e)
	}

	// Write output.
	if err := writeReport(outputPath, &report); err != nil {
		return fmt.Errorf("writing report: %w", err)
	}
	fmt.Fprintf(os.Stderr, "report written to %s\n", outputPath)
	return nil
}

// resourceEntry returns an existing or newly-created *ResourceEntry for the given component.
func resourceEntry(m map[string]*ResourceEntry, component string) *ResourceEntry {
	if e, ok := m[component]; ok {
		return e
	}
	e := &ResourceEntry{Component: component}
	m[component] = e
	return e
}

// writeReport serialises the report as indented JSON to path, creating parent dirs as needed.
func writeReport(path string, report *Report) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

// readReport deserialises a JSON report from path.
func readReport(path string) (*Report, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var r Report
	if err := json.NewDecoder(f).Decode(&r); err != nil {
		return nil, err
	}
	return &r, nil
}

// printMarkdown renders the report as a Markdown document to stdout.
func printMarkdown(r *Report) {
	ts := r.CollectedAt.UTC().Format("2006-01-02T15:04Z")
	fmt.Printf("## Pyroscope Benchmark — %s\n\n", ts)

	fmt.Println("### Ingest")
	fmt.Println("| metric | value |")
	fmt.Println("|---|---|")
	fmt.Printf("| ingest rate | %.1f profiles/s |\n", r.Metrics["ingest_rate"])
	fmt.Printf("| push p99 latency | %s |\n", fmtLatency(r.Metrics["push_p99_seconds"]))
	fmt.Printf("| error rate | %.4f%% |\n", r.Metrics["error_rate"]*100)
	fmt.Println()

	fmt.Println("### Query latency")
	fmt.Println("| percentile | latency |")
	fmt.Println("|---|---|")
	fmt.Printf("| p99 SelectMergeProfile | %s |\n", fmtLatency(r.Metrics["query_p99_seconds"]))
	fmt.Println()

	if len(r.Resources) > 0 {
		fmt.Println("### Peak resource usage")
		fmt.Println("| component | CPU | memory |")
		fmt.Println("|---|---|---|")
		for _, res := range r.Resources {
			fmt.Printf("| %s | %s | %s |\n",
				res.Component,
				fmtCPU(res.PeakCPUCores),
				fmtMemory(res.PeakMemoryBytes),
			)
		}
		fmt.Println()
	}
}

// fmtLatency formats a duration in seconds as milliseconds when < 10 s,
// otherwise as seconds with one decimal place.
func fmtLatency(seconds float64) string {
	ms := seconds * 1000
	if ms < 10000 {
		return fmt.Sprintf("%dms", int(math.Round(ms)))
	}
	return fmt.Sprintf("%.1fs", seconds)
}

// fmtCPU formats a CPU value in cores as millicores when < 1 core,
// otherwise as cores with one decimal place.
func fmtCPU(cores float64) string {
	if cores < 1.0 {
		return fmt.Sprintf("%dm", int(math.Round(cores*1000)))
	}
	return fmt.Sprintf("%.1f", cores)
}

// fmtMemory formats bytes as MiB when < 1 GiB, otherwise as GiB.
func fmtMemory(bytes float64) string {
	const mib = 1048576
	const gib = 1073741824
	if bytes < gib {
		return fmt.Sprintf("%.0fMi", bytes/mib)
	}
	return fmt.Sprintf("%.1fGi", bytes/gib)
}

// formatDuration converts a time.Duration to a Prometheus duration string (e.g. "1h", "30m").
func formatDuration(d time.Duration) string {
	if d%time.Hour == 0 {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	if d%time.Minute == 0 {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

// querier wraps an HTTP client for the Prometheus instant-query API.
type querier struct {
	baseURL string
	client  *http.Client
}

// queryScalar executes an instant query and returns the first result value as float64.
// If the result is empty, 0 is returned without error.
func (q *querier) queryScalar(expr string) (float64, error) {
	results, err := q.queryVector(expr)
	if err != nil {
		return 0, err
	}
	// For scalar expressions there is typically one unlabelled result.
	for _, v := range results {
		return v, nil
	}
	return 0, nil
}

// queryVector executes an instant query and returns a map of container-label → value.
// For results without a "container" label the metric's __name__ or a synthetic key is used.
func (q *querier) queryVector(expr string) (map[string]float64, error) {
	apiURL := q.baseURL + "/api/v1/query"
	params := url.Values{"query": {expr}}
	resp, err := q.client.Get(apiURL + "?" + params.Encode())
	if err != nil {
		return nil, fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prometheus returned %d: %s", resp.StatusCode, body)
	}

	var pr promResponse
	if err := json.Unmarshal(body, &pr); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	if pr.Status != "success" {
		return nil, fmt.Errorf("prometheus status %q", pr.Status)
	}

	out := make(map[string]float64, len(pr.Data.Result))
	for i, res := range pr.Data.Result {
		// Prefer the "container" label; fall back to other label or index.
		key := res.Metric["container"]
		if key == "" {
			key = res.Metric["__name__"]
		}
		if key == "" {
			key = strconv.Itoa(i)
		}

		// Value is [timestamp, "stringValue"].
		var rawVal string
		if err := json.Unmarshal(res.Value[1], &rawVal); err != nil {
			return nil, fmt.Errorf("unmarshal value: %w", err)
		}
		val, err := strconv.ParseFloat(rawVal, 64)
		if err != nil {
			return nil, fmt.Errorf("parse float %q: %w", rawVal, err)
		}
		out[key] = val
	}
	return out, nil
}
