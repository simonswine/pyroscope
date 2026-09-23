package series_by_service

import (
	"testing"

	"github.com/grafana/pyroscope/macro-benchmark/benchmark/internal/benchutil"
)

func BenchmarkSeriesByService(b *testing.B) {
	c := benchutil.FromEnv(b)
	body := c.ProfileBody(b)
	body["step"] = 15
	body["groupBy"] = []string{"service_name"}
	benchutil.Run(b, c, "SelectSeries", body, "series", 0)
}
