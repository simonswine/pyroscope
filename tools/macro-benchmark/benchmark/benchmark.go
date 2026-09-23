// Package benchmark loads the self-registering benchmark packages.
package benchmark

import (
	_ "github.com/grafana/pyroscope/macro-benchmark/benchmark/all-series"
	"github.com/grafana/pyroscope/macro-benchmark/benchmark/internal/registry"
	_ "github.com/grafana/pyroscope/macro-benchmark/benchmark/label-names"
	_ "github.com/grafana/pyroscope/macro-benchmark/benchmark/merge-dot"
	_ "github.com/grafana/pyroscope/macro-benchmark/benchmark/merge-flamegraph"
	_ "github.com/grafana/pyroscope/macro-benchmark/benchmark/merge-pprof"
	_ "github.com/grafana/pyroscope/macro-benchmark/benchmark/merge-tree"
	_ "github.com/grafana/pyroscope/macro-benchmark/benchmark/merged-flamegraph"
	_ "github.com/grafana/pyroscope/macro-benchmark/benchmark/series-by-service"
	_ "github.com/grafana/pyroscope/macro-benchmark/benchmark/series-total"
	_ "github.com/grafana/pyroscope/macro-benchmark/benchmark/service-label-values"
)

type Definition = registry.Benchmark

// All returns all registered benchmarks, sorted by name.
func All() []Definition { return registry.All() }

// Names returns the registered benchmarks for a dataset. Custom fixtures use
// the checkoutservice suite.
func Names(dataset string) []string {
	if dataset == "" {
		dataset = "checkoutservice"
	}
	return registry.Names(dataset)
}
