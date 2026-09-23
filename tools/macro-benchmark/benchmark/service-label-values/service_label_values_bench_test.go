package service_label_values

import (
	"testing"

	"github.com/grafana/pyroscope/macro-benchmark/benchmark/internal/benchutil"
)

func BenchmarkServiceLabelValues(b *testing.B) {
	c := benchutil.FromEnv(b)
	body := c.MetadataBody()
	body["name"] = "service_name"
	benchutil.Run(b, c, "LabelValues", body, "names", 0)
}
