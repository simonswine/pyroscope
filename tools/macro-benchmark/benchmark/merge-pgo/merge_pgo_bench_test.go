package merge_pgo

import (
	"testing"

	"github.com/grafana/pyroscope/macro-benchmark/benchmark/internal/benchutil"
)

// BenchmarkMergePGO matches merge-pprof except for the Go PGO stack selector.
// These settings match profilecli query go-pgo's defaults.
func BenchmarkMergePGO(b *testing.B) {
	c := benchutil.FromEnv(b)
	body := c.ProfileBody(b)
	body["maxNodes"] = 2048
	body["format"] = "PROFILE_FORMAT_PPROF"
	body["stackTraceSelector"] = map[string]any{
		"goPgo": map[string]any{
			"keepLocations":    5,
			"aggregateCallees": true,
		},
	}
	benchutil.Run(b, c, "SelectMergeStacktraces", body, "pprof", 128<<20)
}
