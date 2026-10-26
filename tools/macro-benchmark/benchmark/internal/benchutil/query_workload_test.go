package benchutil

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestQueryWorkload(t *testing.T) {
	t.Setenv("PROFILE_TYPE", "process_cpu:cpu:nanoseconds:cpu:nanoseconds")
	seen := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if r.Header.Get("X-Scope-OrgID") != "tenant" {
			t.Error("missing tenant")
		}
		if r.URL.Path == "/querier.v1.QuerierService/LabelValues" {
			if body["name"] != PodLabel {
				t.Error("wrong pod label")
			}
			_, _ = w.Write([]byte(`{"names":["z-pod","a-pod"]}`))
			return
		}
		if body["labelSelector"] != `{service_name="test",`+PodLabel+`="a-pod"}` {
			t.Errorf("selector: %v", body["labelSelector"])
		}
		groups, ok := body["groupBy"].([]any)
		if !ok || len(groups) != 1 || groups[0] != JobLabel {
			t.Errorf("groupBy: %v", body["groupBy"])
		}
		exemplar, _ := body["exemplarType"].(string)
		seen[r.URL.Path+"/"+exemplar] = true
		if r.URL.Path == "/querier.v1.QuerierService/SelectHeatmap" && body["queryType"] != "HEATMAP_QUERY_TYPE_INDIVIDUAL" {
			t.Error("wrong heatmap query type")
		}
		_, _ = w.Write([]byte(`{"series":[{"points":[{"value":1}]}]}`))
	}))
	defer server.Close()
	result := testing.Benchmark(func(b *testing.B) {
		c := Config{URL: server.URL, Tenant: "tenant", Start: "1000", End: "7201000", Selector: `{service_name="test"}`}
		c = SinglePod(b, c)
		QueryWorkload(b, c)
	})
	if result.N == 0 {
		t.Fatal("benchmark failed")
	}
	for _, key := range []string{
		"/querier.v1.QuerierService/SelectSeries/EXEMPLAR_TYPE_NONE",
		"/querier.v1.QuerierService/SelectSeries/EXEMPLAR_TYPE_INDIVIDUAL",
		"/querier.v1.QuerierService/SelectHeatmap/EXEMPLAR_TYPE_NONE",
	} {
		if !seen[key] {
			t.Errorf("missing request %s", key)
		}
	}
}
