package merge_flamegraph

import "github.com/grafana/pyroscope/macro-benchmark/benchmark/internal/registry"

func init() {
	registry.Register(registry.Benchmark{Name: "merge-flamegraph", Dataset: "high-volume-service"})
}
