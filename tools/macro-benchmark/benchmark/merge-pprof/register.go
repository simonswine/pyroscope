package merge_pprof

import "github.com/grafana/pyroscope/macro-benchmark/benchmark/internal/registry"

func init() {
	registry.Register(registry.Benchmark{Name: "merge-pprof", Dataset: "high-volume-service"})
}
