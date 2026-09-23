package series_by_service

import "github.com/grafana/pyroscope/macro-benchmark/benchmark/internal/registry"

func init() {
	registry.Register(registry.Benchmark{Name: "series-by-service", Dataset: "checkoutservice"})
}
