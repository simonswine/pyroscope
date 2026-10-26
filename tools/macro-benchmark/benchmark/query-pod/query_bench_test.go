package query_pod

import (
	"github.com/grafana/pyroscope/macro-benchmark/benchmark/internal/benchutil"
	"testing"
)

func BenchmarkQueryPod(b *testing.B) {
	c := benchutil.FromEnv(b)
	c = benchutil.SinglePod(b, c)
	benchutil.QueryWorkload(b, c)
}
