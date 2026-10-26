package merge_dot

import "github.com/grafana/pyroscope/macro-benchmark/benchmark/internal/registry"

func init() {
	registry.Register(registry.Benchmark{Name: "merge-dot", Dataset: "high-volume-service"})
}
