package query_wide

import (
	"github.com/grafana/pyroscope/macro-benchmark/benchmark/internal/benchutil"
	"testing"
)

func BenchmarkQueryWide(b *testing.B) {
	c := benchutil.FromEnv(b)
	c.Selector = `{` + benchutil.NamespaceLabel + `="` + benchutil.FireDevNamespace + `"}`
	benchutil.QueryWorkload(b, c)
}
