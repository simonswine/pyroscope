package query_high_volume

import (
	"github.com/grafana/pyroscope/macro-benchmark/benchmark/internal/benchutil"
	"testing"
)

func BenchmarkQueryHighVolume(b *testing.B) {
	c := benchutil.FromEnv(b)
	benchutil.QueryWorkloadAllowEmptySpan(b, c)
}
