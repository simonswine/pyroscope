package benchutil

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Fixture label names and values are SHA-256 mappings from the replay salt.
const (
	JobLabel         = "_df85ce6bea4f0cd5fa173d05e14696437102634ab5ed6caf8bb1dbda9fb87670"
	NamespaceLabel   = "_8eb433fd58159efe4524414528af7214e3da797b129b759b8ef7672feb39cf71"
	PodLabel         = "_90aa985b6909294a8b8446c1a3d1dd7cb262afd9a8ee71f25e823d2a3406b056"
	FireDevNamespace = "65f0566afcdcf946d39ee9e47ecdd7a0e3a19087f23d1d8193a4c40d6760cb8c"
)

// SeriesWorkload compares SelectSeries with and without individual exemplars.
func SeriesWorkload(b *testing.B, c Config, groupBy []string) {
	b.Helper()
	for _, test := range []struct{ name, exemplar string }{
		{"series", "EXEMPLAR_TYPE_NONE"},
		{"series-with-exemplars", "EXEMPLAR_TYPE_INDIVIDUAL"},
	} {
		b.Run(test.name, func(b *testing.B) {
			body := c.ProfileBody(b)
			body["step"] = 15
			if len(groupBy) > 0 {
				body["groupBy"] = groupBy
			}
			body["exemplarType"] = test.exemplar
			Run(b, c, "SelectSeries", body, "series", 0)
		})
	}
}

// QueryWorkload measures the same selection across series and heatmap APIs.
func QueryWorkload(b *testing.B, c Config) {
	b.Helper()
	queryWorkload(b, c, false)
}

// QueryWorkloadAllowEmptySpan also measures span queries on fixtures without span data.
func QueryWorkloadAllowEmptySpan(b *testing.B, c Config) {
	b.Helper()
	queryWorkload(b, c, true)
}

func queryWorkload(b *testing.B, c Config, allowEmptySpan bool) {
	b.Helper()
	for _, test := range []struct{ name, method, exemplar, queryType string }{
		{"series", "SelectSeries", "EXEMPLAR_TYPE_NONE", ""},
		{"series-with-exemplars", "SelectSeries", "EXEMPLAR_TYPE_INDIVIDUAL", ""},
		{"heatmap", "SelectHeatmap", "EXEMPLAR_TYPE_NONE", "HEATMAP_QUERY_TYPE_INDIVIDUAL"},
		{"span-heatmap", "SelectHeatmap", "EXEMPLAR_TYPE_NONE", "HEATMAP_QUERY_TYPE_SPAN"},
	} {
		b.Run(test.name, func(b *testing.B) {
			body := c.ProfileBody(b)
			body["step"] = 15
			body["groupBy"] = []string{JobLabel}
			body["exemplarType"] = test.exemplar
			if test.queryType != "" {
				body["queryType"] = test.queryType
			}
			requiredField := "series"
			if allowEmptySpan && test.name == "span-heatmap" {
				requiredField = ""
			}
			Run(b, c, test.method, body, requiredField, 0)
		})
	}
}

// SinglePod chooses the lexicographically first pod in the replay window.
// Discovery is untimed and sorted so both revisions select the same pod.
func SinglePod(b *testing.B, c Config) Config {
	b.Helper()
	body := c.MetadataBody()
	body["name"] = PodLabel
	payload, err := json.Marshal(body)
	if err != nil {
		b.Fatal(err)
	}
	req, err := http.NewRequestWithContext(b.Context(), http.MethodPost, strings.TrimRight(c.URL, "/")+"/querier.v1.QuerierService/LabelValues", bytes.NewReader(payload))
	if err != nil {
		b.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("X-Scope-OrgID", c.Tenant)
	client := &http.Client{Timeout: 2 * time.Minute}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		b.Fatal(err)
	}
	defer resp.Body.Close()
	const limit = 64 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		b.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || len(data) > limit {
		b.Fatalf("pod discovery: HTTP %d, %d bytes", resp.StatusCode, len(data))
	}
	var result struct {
		Names []string `json:"names"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		b.Fatal(err)
	}
	if len(result.Names) == 0 {
		b.Fatal("pod discovery returned no pods")
	}
	sort.Strings(result.Names)
	// Intersect with the configured selector rather than broadening the query.
	selector := strings.TrimSpace(c.Selector)
	if !strings.HasPrefix(selector, "{") || !strings.HasSuffix(selector, "}") {
		b.Fatal("LABEL_SELECTOR must be a brace-enclosed selector")
	}
	matchers := strings.TrimSpace(selector[1 : len(selector)-1])
	if matchers != "" {
		matchers += ","
	}
	c.Selector = "{" + matchers + PodLabel + "=" + strconv.Quote(result.Names[0]) + "}"
	return c
}
