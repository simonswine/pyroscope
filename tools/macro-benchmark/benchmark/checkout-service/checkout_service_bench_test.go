package checkout_service

import (
	"testing"

	"github.com/grafana/pyroscope/macro-benchmark/benchmark/internal/benchutil"
)

// BenchmarkCheckoutService is a small happy-path benchmark using a single
// low-volume service. It runs quickly and is useful for smoke-testing the
// query path without the overhead of a large dataset.
func BenchmarkCheckoutService(b *testing.B) {
	c := benchutil.FromEnv(b)
	body := c.ProfileBody(b)
	body["maxNodes"] = 2048
	benchutil.Run(b, c, "SelectMergeStacktraces", body, "flamegraph", 0)
}
