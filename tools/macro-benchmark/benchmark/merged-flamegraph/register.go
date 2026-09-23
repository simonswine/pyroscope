package merged_flamegraph

import "github.com/grafana/pyroscope/macro-benchmark/benchmark/internal/registry"

func init() {
	registry.Register(registry.Benchmark{Name: "merged-flamegraph", Dataset: "full-tenant"})
}
