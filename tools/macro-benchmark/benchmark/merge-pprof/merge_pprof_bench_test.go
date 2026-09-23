package merge_pprof

import (
	"testing"

	"github.com/grafana/pyroscope/macro-benchmark/benchmark/internal/benchutil"
)

func BenchmarkMergePprof(b *testing.B) {
	c := benchutil.FromEnv(b)
	body := c.ProfileBody(b)
	body["maxNodes"] = 2048
	body["format"] = "PROFILE_FORMAT_PPROF"
	benchutil.Run(b, c, "SelectMergeStacktraces", body, "pprof", 0)
}
