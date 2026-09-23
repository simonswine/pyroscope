package merge_tree

import "github.com/grafana/pyroscope/macro-benchmark/benchmark/internal/registry"

func init() {
	registry.Register(registry.Benchmark{Name: "merge-tree", Dataset: "high-volume-service"})
}
