package merge_pgo

import "github.com/grafana/pyroscope/macro-benchmark/benchmark/internal/registry"

func init() {
	registry.Register(registry.Benchmark{Name: "merge-pgo", Dataset: "high-volume-service"})
}
