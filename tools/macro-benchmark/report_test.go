package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/pprof/profile"
)

func TestReportBenchstat(t *testing.T) {
	baseline := []byte("goos: linux\nBenchmarkExample 5 100 ns/op\nBenchmarkExample 5 110 ns/op\nBenchmarkExample 5 105 ns/op\nBenchmarkExample 5 102 ns/op\nBenchmarkExample 5 108 ns/op\n")
	comparison := []byte("goos: linux\nBenchmarkExample 5 200 ns/op\nBenchmarkExample 5 210 ns/op\nBenchmarkExample 5 205 ns/op\nBenchmarkExample 5 202 ns/op\nBenchmarkExample 5 208 ns/op\n")
	r := buildReport(map[string][]byte{"baseline/example/bench.txt": baseline, "comparison/example/bench.txt": comparison})
	if len(r.Benchmarks) != 1 || !strings.Contains(r.Benchmarks[0].Stats, "time/op") || !strings.Contains(r.Benchmarks[0].Stats, "+95") {
		t.Fatalf("unexpected benchstat: %+v", r.Benchmarks)
	}
}
func TestReportIncludesOfflineViewer(t *testing.T) {
	var output bytes.Buffer
	data := buildReport(map[string][]byte{
		"baseline/x/bench.txt":   []byte("BenchmarkX 1 1 ns/op\n"),
		"comparison/x/bench.txt": []byte("BenchmarkX 1 2 ns/op\n"),
	})
	if len(reportJS) < 1000 || len(reportCSS) < 1000 {
		t.Fatal("report assets not embedded")
	}
	if strings.Contains(reportJS, "process.env.NODE_ENV") || strings.Contains(reportJS, "fonts.googleapis.com") {
		t.Fatal("report viewer requires runtime globals or external fonts")
	}
	view := struct {
		Benchmarks []benchmarkReport
		Summary    reportSummary
		JSON       template.JS
		Script     template.JS
		CSS        template.CSS
	}{data.Benchmarks, data.Summary, template.JS(`{"benchmarks":[]}`), template.JS(reportJS), template.CSS(reportCSS)}
	if err := reportHTML.Execute(&output, view); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "fonts.googleapis.com") || !strings.Contains(output.String(), "report-flamegraph") || !strings.Contains(output.String(), "background:var(--bg-canvas)") || !strings.Contains(output.String(), `<div class="overview-viewer">`) || !strings.Contains(output.String(), `<section class="benchmark"`) {
		t.Fatal("offline viewer not present")
	}
}

func TestReportOverviewMetrics(t *testing.T) {
	baseline := []byte("BenchmarkExample 5 100 ns/op 20 queries/s\nBenchmarkExample 5 110 ns/op 22 queries/s\nBenchmarkExample 5 105 ns/op 21 queries/s\n")
	comparison := []byte("BenchmarkExample 5 200 ns/op 30 queries/s\nBenchmarkExample 5 210 ns/op 32 queries/s\nBenchmarkExample 5 205 ns/op 31 queries/s\n")
	r := buildReport(map[string][]byte{"baseline/example/bench.txt": baseline, "comparison/example/bench.txt": comparison})
	metrics := r.Benchmarks[0].Metrics
	if len(metrics) != 6 {
		t.Fatalf("expected 2 benchmark and 4 profile metrics: %+v", metrics)
	}
	if metrics[0].Baseline == "—" || metrics[0].Comparison == "—" || metrics[0].Key != "time/op" || metrics[0].BaselineValue == nil || *metrics[0].BaselineValue != 105 || metrics[0].ComparisonValue == nil || *metrics[0].ComparisonValue != 205 {
		t.Fatalf("missing raw time/op values: %+v", metrics)
	}
	if metrics[2].Baseline != "—" || metrics[2].Change != "—" || metrics[2].BaselineValue != nil || metrics[2].ChangeValue != nil {
		t.Fatalf("missing profile shown as a value: %+v", metrics[2])
	}
	cpu := profileMetric(reportProfile{Name: "cpu", Unit: "nanoseconds", Baseline: &reportTree{Total: 5e9}, Comparison: &reportTree{Total: 6e9}, BaselineDuration: 10e9, ComparisonDuration: 10e9})
	if cpu.Key != "profile:cpu" || cpu.Baseline != "5.00 s" || cpu.Comparison != "6.00 s" || cpu.Change != "+20.0%" || cpu.BaselineValue == nil || *cpu.BaselineValue != 5e9 || cpu.ChangeValue == nil || (*cpu.ChangeValue < 19.9 || *cpu.ChangeValue > 20.1) || !strings.Contains(cpu.Note, "10.00 s / 10.00 s") {
		t.Fatalf("wrong sampled CPU time: %+v", cpu)
	}
	alloc := profileMetric(reportProfile{Name: "allocations", Unit: "bytes", Baseline: &reportTree{Total: 10 << 20}, Comparison: &reportTree{Total: 20 << 20}})
	if alloc.Key != "profile:allocations" || alloc.Baseline != "10.00 MiB" || alloc.Comparison != "20.00 MiB" || alloc.ComparisonValue == nil || *alloc.ComparisonValue != 20<<20 {
		t.Fatalf("wrong allocation measurement: %+v", alloc)
	}
}

func TestReportProfileUnits(t *testing.T) {
	for _, tc := range []struct {
		value int64
		want  string
	}{
		{value: 5, want: "5 ns"},
		{value: 2500, want: "2.50 µs"},
		{value: 3e6, want: "3.00 ms"},
		{value: 4e9, want: "4.00 s"},
	} {
		if got := formatSampledTime(tc.value); got != tc.want {
			t.Errorf("formatSampledTime(%d) = %q, want %q", tc.value, got, tc.want)
		}
	}
	for _, tc := range []struct {
		value int64
		want  string
	}{
		{value: 512, want: "512 B"},
		{value: 1536, want: "1.50 KiB"},
		{value: 2 << 20, want: "2.00 MiB"},
		{value: 3 << 30, want: "3.00 GiB"},
	} {
		if got := formatBinaryBytes(tc.value); got != tc.want {
			t.Errorf("formatBinaryBytes(%d) = %q, want %q", tc.value, got, tc.want)
		}
	}
}

func TestReportUsesAllocatedBytes(t *testing.T) {
	p := &profile.Profile{
		SampleType: []*profile.ValueType{{Type: "inuse_space", Unit: "bytes"}, {Type: "alloc_space", Unit: "bytes"}},
		Sample:     []*profile.Sample{{Value: []int64{100, 2000}}},
	}
	var buf bytes.Buffer
	if err := p.Write(&buf); err != nil {
		t.Fatal(err)
	}
	r := buildReport(map[string][]byte{
		"baseline/example/bench.txt":    []byte("BenchmarkExample 1 1 ns/op\n"),
		"comparison/example/bench.txt":  []byte("BenchmarkExample 1 1 ns/op\n"),
		"baseline/example/heap.pprof":   buf.Bytes(),
		"comparison/example/heap.pprof": buf.Bytes(),
	})
	heap := r.Benchmarks[0].Profiles[1]
	if heap.Name != "allocations" || heap.Baseline == nil || heap.Baseline.Total != 2000 || heap.Comparison == nil || heap.Comparison.Total != 2000 {
		t.Fatalf("expected alloc_space, not inuse_space: %+v", heap)
	}
}

func TestReportSubBenchmarksShareMetricKey(t *testing.T) {
	baseline := []byte("BenchmarkSeries/all-by-workload-labels 5 100 ns/op 10 queries/s\nBenchmarkSeries/one-service 5 200 ns/op 5 queries/s\n")
	comparison := []byte("BenchmarkSeries/all-by-workload-labels 5 110 ns/op 9 queries/s\nBenchmarkSeries/one-service 5 180 ns/op 6 queries/s\n")
	r := buildReport(map[string][]byte{"baseline/series/bench.txt": baseline, "comparison/series/bench.txt": comparison})
	metrics := r.Benchmarks[0].Metrics
	var scopes []string
	for _, metric := range metrics {
		if metric.Key == "time/op" {
			scopes = append(scopes, metric.Scope)
			if metric.Baseline == "—" || metric.Comparison == "—" {
				t.Fatalf("missing sub-benchmark result: %+v", metric)
			}
		}
	}
	if len(scopes) != 2 || scopes[0] == scopes[1] || !strings.Contains(strings.Join(scopes, ","), "Series/all-by-workload-labels") {
		t.Fatalf("expected two sub-benchmarks with time/op: %v", scopes)
	}
}

func TestReportSummary(t *testing.T) {
	data := buildReport(map[string][]byte{
		"plan.yaml": []byte("baseline:\n  ref: v1\n  commit: aaaa\ncomparison:\n  ref: v2\n  commit: bbbb\n"),
		"cpu.txt":   []byte("Model name: Example CPU\nCPU(s): 16\n"),
	})
	if data.Summary.Baseline.Commit != "aaaa" || data.Summary.Comparison.Commit != "bbbb" || data.Summary.Cores != 16 || data.Summary.CPU != "Example CPU" || data.Summary.MemoryGiB != 64 {
		t.Fatalf("unexpected summary: %+v", data.Summary)
	}
}

func TestReportRunTiming(t *testing.T) {
	for _, tc := range []struct {
		name, status, log, started, finished, duration string
	}{
		{
			name: "explicit start", status: "started_at: 2026-09-25T15:05:59Z\nfinished_at: 2026-09-25T17:56:35Z\n",
			log: "2026/09/25 10:00:00 an earlier event\n", started: "2026-09-25 15:05:59 UTC",
			finished: "2026-09-25 17:56:35 UTC", duration: "2h 50m 36s",
		},
		{
			name: "legacy log start", status: "finished_at: 2026-09-25T10:54:04Z\n",
			log:     "2026/09/25 10:47:51 taskset\n2026/09/25 10:50:00 work\n",
			started: "2026-09-25 10:47:51 UTC", finished: "2026-09-25 10:54:04 UTC", duration: "6m 13s",
		},
		{name: "missing start", status: "finished_at: 2026-09-25T10:54:04Z\n", finished: "2026-09-25 10:54:04 UTC"},
		{name: "invalid order", status: "started_at: 2026-09-25T11:00:00Z\nfinished_at: 2026-09-25T10:54:04Z\n", started: "2026-09-25 11:00:00 UTC", finished: "2026-09-25 10:54:04 UTC"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			summary := buildReportSummary(map[string][]byte{"status.yaml": []byte(tc.status), "run.log": []byte(tc.log)})
			if summary.Started != tc.started || summary.Finished != tc.finished || summary.Duration != tc.duration {
				t.Fatalf("unexpected timing: %+v", summary)
			}
		})
	}
}

func TestReportWritesLazyProfiles(t *testing.T) {
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	bench := []byte("BenchmarkX 1 1 ns/op\n")
	for name, content := range map[string][]byte{
		"baseline/x/bench.txt": bench, "comparison/x/bench.txt": bench,
		"status.yaml": []byte("finished_at: 2026-09-25T10:54:04Z\n"),
		"run.log":     []byte("2026/09/25 10:47:51 taskset\n"),
	} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	input, output := filepath.Join(dir, "results.tar.gz"), filepath.Join(dir, "index.html")
	if err := os.WriteFile(input, archive.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := generateReport(input, output); err != nil {
		t.Fatal(err)
	}
	html, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(html), "index.assets/profile-0-0.json") || strings.Contains(string(html), `"baseline":{"name"`) {
		t.Fatal("profile payload not split from HTML")
	}
	if !strings.Contains(string(html), "2026-09-25 10:47:51 UTC") || !strings.Contains(string(html), "6m 13s") {
		t.Fatal("archive run timing missing from HTML")
	}
	profile, err := os.ReadFile(filepath.Join(dir, "index.assets", "profile-0-0.json"))
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(profile, &payload); err != nil {
		t.Fatal(err)
	}
	if string(payload["unit"]) != `"nanoseconds"` {
		t.Fatalf("unexpected profile: %s", profile)
	}
}

func TestReportArchiveRejectsTraversal(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "../baseline/x/bench.txt", Mode: 0600, Size: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(t.TempDir(), "results.tar.gz")
	if err := os.WriteFile(filename, buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := readReportArchive(filename)
	if err == nil || !strings.Contains(err.Error(), "invalid archive path") {
		t.Fatalf("expected invalid path error, got %v", err)
	}
}
