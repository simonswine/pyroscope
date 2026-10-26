package series

import (
	"testing"

	"github.com/grafana/pyroscope/macro-benchmark/benchmark/internal/benchutil"
)

const (
	clusterLabel   = "_57c5c6697e5185ce4d47da5529e3c06ef71481244b305cfbf6b2fa72a84cef1d"
	namespaceLabel = "_8eb433fd58159efe4524414528af7214e3da797b129b759b8ef7672feb39cf71"
	podLabel       = "_90aa985b6909294a8b8446c1a3d1dd7cb262afd9a8ee71f25e823d2a3406b056"
	grafanaService = "2efc417b204360a91e2aabef7a7b3e99b28f7857f972703f4e8e9263611a7271" // hosted-grafana/grafana
)

func BenchmarkSeries(b *testing.B) {
	c := benchutil.FromEnv(b)
	b.Run("all-by-workload-labels", func(b *testing.B) {
		body := c.MetadataBody()
		body["labelNames"] = []string{clusterLabel, namespaceLabel, podLabel}
		benchutil.Run(b, c, "Series", body, "labelsSet", 512<<20)
	})
	b.Run("all-by-profile-type-service", func(b *testing.B) {
		body := c.MetadataBody()
		body["labelNames"] = []string{"__profile_type__", "service_name"}
		benchutil.Run(b, c, "Series", body, "labelsSet", 512<<20)
	})
	b.Run("single-service-by-workload-labels", func(b *testing.B) {
		body := c.MetadataBody()
		body["matchers"] = []string{`{service_name="` + grafanaService + `"}`}
		body["labelNames"] = []string{clusterLabel, namespaceLabel, podLabel}
		benchutil.Run(b, c, "Series", body, "labelsSet", 512<<20)
	})
}
