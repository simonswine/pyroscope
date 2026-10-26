package query_pod

import "github.com/grafana/pyroscope/macro-benchmark/benchmark/internal/registry"

func init() {
	registry.Register(registry.Benchmark{Name: "query-pod", Dataset: "high-volume-service"})
}
