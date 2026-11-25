package series_total

import (
	"testing"

	"github.com/grafana/pyroscope/macro-benchmark/benchmark/internal/benchutil"
)

func BenchmarkSeriesTotal(b *testing.B) {
	c := benchutil.FromEnv(b)
	benchutil.SeriesWorkload(b, c, nil)
}
