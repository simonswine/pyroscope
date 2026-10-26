package label_names

import (
	"testing"

	"github.com/grafana/pyroscope/macro-benchmark/benchmark/internal/benchutil"
)

func BenchmarkLabelNames(b *testing.B) {
	c := benchutil.FromEnv(b)
	benchutil.Run(b, c, "LabelNames", c.MetadataBody(), "names", 0)
}
