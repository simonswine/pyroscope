package merged_flamegraph

import (
	"testing"

	"github.com/grafana/pyroscope/macro-benchmark/benchmark/internal/benchutil"
)

func BenchmarkMergedFlamegraph(b *testing.B) {
	c := benchutil.FromEnv(b)
	body := c.ProfileBody(b)
	body["maxNodes"] = 2048
	benchutil.Run(b, c, "SelectMergeStacktraces", body, "flamegraph", 0)
}
