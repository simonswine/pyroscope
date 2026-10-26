package query_wide

import "github.com/grafana/pyroscope/macro-benchmark/benchmark/internal/registry"

func init() {
	registry.Register(registry.Benchmark{Name: "query-wide", Dataset: "full-tenant"})
}
