package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/pprof/profile"
)

func TestReportMetastoreProfiles(t *testing.T) {
	for _, prefix := range []string{"metastore", "ingest", "both"} {
		t.Run(prefix, func(t *testing.T) {
			root := t.TempDir()
			for _, side := range []string{"baseline", "comparison"} {
				dir := filepath.Join(root, side, "example")
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "bench.txt"), []byte("BenchmarkExample 1 1 ns/op\n"), 0600); err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"metastore", "ingest"} {
					if prefix != "both" && prefix != name {
						continue
					}
					value := int64(2000)
					if name == "ingest" {
						value = 1000
					}
					for _, kind := range []string{"cpu", "heap"} {
						unit, sampleType := "nanoseconds", "cpu"
						if kind == "heap" {
							unit, sampleType = "bytes", "alloc_space"
						}
						p := &profile.Profile{SampleType: []*profile.ValueType{{Type: sampleType, Unit: unit}}, Sample: []*profile.Sample{{Value: []int64{value}}}}
						var buf bytes.Buffer
						if err := p.Write(&buf); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filepath.Join(dir, name+"-"+kind+".pprof"), buf.Bytes(), 0600); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			archive := filepath.Join(t.TempDir(), "results.tar.gz")
			if err := archiveDirectory(root, archive); err != nil {
				t.Fatal(err)
			}
			files, err := readReportArchive(archive)
			if err != nil {
				t.Fatal(err)
			}
			r := buildReport(files)
			wantTotal := int64(2000)
			if prefix == "ingest" {
				wantTotal = 1000
			}
			for i, name := range []string{"metastore-cpu", "metastore-allocations"} {
				p := r.Benchmarks[0].Profiles[i+2]
				if p.Name != name || p.Baseline == nil || p.Comparison == nil || p.Baseline.Total != wantTotal || p.Comparison.Total != wantTotal {
					t.Fatalf("unexpected profile: %+v", p)
				}
			}
		})
	}
}
