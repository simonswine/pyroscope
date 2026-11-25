package series_by_service

import (
	"testing"

	"github.com/grafana/pyroscope/macro-benchmark/benchmark/internal/benchutil"
)

func BenchmarkSeriesByService(b *testing.B) {
	c := benchutil.FromEnv(b)
	benchutil.SeriesWorkload(b, c, []string{"service_name"})
}
