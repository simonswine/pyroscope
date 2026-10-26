package service_label_values

import "github.com/grafana/pyroscope/macro-benchmark/benchmark/internal/registry"

func init() {
	registry.Register(registry.Benchmark{Name: "service-label-values", Dataset: "full-tenant"})
}
