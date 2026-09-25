package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if strings.Contains(reportJS, "process.env.NODE_ENV") || !strings.Contains(reportJS, "data:image/svg+xml") {
		t.Fatal("report viewer requires runtime globals or external icons")
	}
	view := struct {
		Benchmarks []benchmarkReport
		JSON       template.JS
		Script     template.JS
		CSS        template.CSS
	}{data.Benchmarks, template.JS(`{"benchmarks":[]}`), template.JS(reportJS), template.CSS(reportFontImport.ReplaceAllString(reportCSS, ""))}
	if err := reportHTML.Execute(&output, view); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "fonts.googleapis.com") || !strings.Contains(output.String(), "report-flamegraph") || !strings.Contains(output.String(), "benchmark-viewer") {
		t.Fatal("offline viewer not present")
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
