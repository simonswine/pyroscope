package query_pod

import (
	"testing"

	"github.com/grafana/pyroscope/macro-benchmark/benchmark/internal/benchutil"
)

func BenchmarkQueryPod(b *testing.B) {
	c := benchutil.FromEnv(b)
	c = benchutil.SinglePod(b, c)
	benchutil.QueryWorkloadAllowEmptySpan(b, c)
}
