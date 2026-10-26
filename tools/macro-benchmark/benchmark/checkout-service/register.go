package checkout_service

import "github.com/grafana/pyroscope/macro-benchmark/benchmark/internal/registry"

func init() {
	registry.Register(registry.Benchmark{Name: "checkout-service", Dataset: "checkout-service"})
}
