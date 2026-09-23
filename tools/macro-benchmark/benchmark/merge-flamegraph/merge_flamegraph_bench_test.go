package merge_flamegraph

import (
	"testing"

	"github.com/grafana/pyroscope/macro-benchmark/benchmark/internal/benchutil"
)

func BenchmarkMergeFlamegraph(b *testing.B) {
	c := benchutil.FromEnv(b)
	body := c.ProfileBody(b)
	body["maxNodes"] = 2048
	body["format"] = "PROFILE_FORMAT_FLAMEGRAPH"
	benchutil.Run(b, c, "SelectMergeStacktraces", body, "flamegraph", 0)
}
