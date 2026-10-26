package merge_tree

import (
	"testing"

	"github.com/grafana/pyroscope/macro-benchmark/benchmark/internal/benchutil"
)

func BenchmarkMergeTree(b *testing.B) {
	c := benchutil.FromEnv(b)
	body := c.ProfileBody(b)
	body["maxNodes"] = 2048
	body["format"] = "PROFILE_FORMAT_TREE"
	benchutil.Run(b, c, "SelectMergeStacktraces", body, "tree", 0)
}
