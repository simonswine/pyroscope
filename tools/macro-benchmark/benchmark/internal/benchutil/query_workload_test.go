package benchutil

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSeriesWorkload(t *testing.T) {
	t.Setenv("PROFILE_TYPE", "process_cpu:cpu:nanoseconds:cpu:nanoseconds")
	for _, grouped := range []bool{false, true} {
		seen := map[string]bool{}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if r.URL.Path != "/querier.v1.QuerierService/SelectSeries" || body["step"] != float64(15) {
				t.Errorf("unexpected request: %s %v", r.URL.Path, body)
			}
			groups, present := body["groupBy"]
			if grouped {
				values, ok := groups.([]any)
				if !ok || len(values) != 1 || values[0] != "service_name" {
					t.Errorf("groupBy: %v", groups)
				}
			} else if present {
				t.Errorf("unexpected groupBy: %v", groups)
			}
			exemplar, _ := body["exemplarType"].(string)
			seen[exemplar] = true
			_, _ = w.Write([]byte(`{"series":[{"points":[{"value":1}]}]}`))
		}))
		var groups []string
		if grouped {
			groups = []string{"service_name"}
		}
		result := testing.Benchmark(func(b *testing.B) {
			SeriesWorkload(b, Config{URL: server.URL, Tenant: "tenant", Start: "1000", End: "7201000", Selector: `{service_name="test"}`}, groups)
		})
		server.Close()
		if result.N == 0 || !seen["EXEMPLAR_TYPE_NONE"] || !seen["EXEMPLAR_TYPE_INDIVIDUAL"] {
			t.Errorf("grouped=%v: missing exemplar coverage: %v", grouped, seen)
		}
	}
}

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
		key := r.URL.Path + "/" + exemplar
		if r.URL.Path == "/querier.v1.QuerierService/SelectHeatmap" {
			queryType, _ := body["queryType"].(string)
			if queryType != "HEATMAP_QUERY_TYPE_INDIVIDUAL" && queryType != "HEATMAP_QUERY_TYPE_SPAN" {
				t.Errorf("wrong heatmap query type: %q", queryType)
			}
			key += "/" + queryType
		}
		seen[key] = true
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
		"/querier.v1.QuerierService/SelectHeatmap/EXEMPLAR_TYPE_NONE/HEATMAP_QUERY_TYPE_INDIVIDUAL",
		"/querier.v1.QuerierService/SelectHeatmap/EXEMPLAR_TYPE_NONE/HEATMAP_QUERY_TYPE_SPAN",
	} {
		if !seen[key] {
			t.Errorf("missing request %s", key)
		}
	}
}
