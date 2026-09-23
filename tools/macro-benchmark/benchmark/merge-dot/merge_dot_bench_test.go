package merge_dot

import (
	"testing"

	"github.com/grafana/pyroscope/macro-benchmark/benchmark/internal/benchutil"
)

func BenchmarkMergeDot(b *testing.B) {
	c := benchutil.FromEnv(b)
	body := c.ProfileBody(b)
	body["maxNodes"] = 2048
	body["format"] = "PROFILE_FORMAT_DOT"
	benchutil.Run(b, c, "SelectMergeStacktraces", body, "dot", 0)
}
