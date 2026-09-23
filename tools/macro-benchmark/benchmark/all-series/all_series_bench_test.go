package all_series

import (
	"testing"

	"github.com/grafana/pyroscope/macro-benchmark/benchmark/internal/benchutil"
)

func BenchmarkAllSeries(b *testing.B) {
	c := benchutil.FromEnv(b)
	benchutil.Run(b, c, "Series", c.MetadataBody(), "labelsSet", 512<<20)
}
