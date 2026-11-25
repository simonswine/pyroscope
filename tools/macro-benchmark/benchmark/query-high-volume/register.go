package query_high_volume

import "github.com/grafana/pyroscope/macro-benchmark/benchmark/internal/registry"

func init() {
	registry.Register(registry.Benchmark{Name: "query-high-volume", Dataset: "high-volume-service"})
}
