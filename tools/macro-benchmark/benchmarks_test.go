package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestBenchmarkNames(t *testing.T) {
	seen := map[string]bool{}
	for dataset, count := range map[string]int{"full-tenant": 6, "high-volume-service": 4} {
		names := benchmarkNames(dataset)
		if len(names) != count {
			t.Fatalf("%s: expected %d benchmarks, got %v", dataset, count, names)
		}
		for _, name := range names {
			if seen[name] {
				t.Fatalf("duplicate benchmark %s", name)
			}
			seen[name] = true
			files, err := filepath.Glob(filepath.Join("benchmark", name, "*_bench_test.go"))
			if err != nil || len(files) != 1 {
				t.Fatalf("%s: expected one Go benchmark file: %v", name, err)
			}
		}
	}
	entries, err := os.ReadDir("benchmark")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() != "internal" && !seen[entry.Name()] {
			t.Errorf("benchmark %s is not selected by any dataset", entry.Name())
		}
	}
	if !slices.Equal(benchmarkNames(""), benchmarkNames("full-tenant")) {
		t.Fatal("custom fixtures should default to full-tenant benchmarks")
	}
}
