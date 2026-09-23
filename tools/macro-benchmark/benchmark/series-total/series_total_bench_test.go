package series_total

import (
	"testing"

	"github.com/grafana/pyroscope/macro-benchmark/benchmark/internal/benchutil"
)

func BenchmarkSeriesTotal(b *testing.B) {
	c := benchutil.FromEnv(b)
	body := c.ProfileBody(b)
	body["step"] = 15
	benchutil.Run(b, c, "SelectSeries", body, "series", 0)
}
