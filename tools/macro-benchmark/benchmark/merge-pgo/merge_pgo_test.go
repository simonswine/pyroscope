package merge_pgo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestMergePGORequest(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/querier.v1.QuerierService/SelectMergeStacktraces" {
			t.Errorf("unexpected endpoint: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-Scope-OrgID") != "test-tenant" {
			t.Errorf("unexpected tenant: %s", r.Header.Get("X-Scope-OrgID"))
		}
		var request struct {
			ProfileTypeID      string `json:"profileTypeID"`
			LabelSelector      string `json:"labelSelector"`
			Start              string `json:"start"`
			End                string `json:"end"`
			MaxNodes           int    `json:"maxNodes"`
			Format             string `json:"format"`
			StackTraceSelector struct {
				GoPGO *struct {
					KeepLocations    int  `json:"keepLocations"`
					AggregateCallees bool `json:"aggregateCallees"`
				} `json:"goPgo"`
			} `json:"stackTraceSelector"`
		}
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			t.Error(err)
		}
		pgo := request.StackTraceSelector.GoPGO
		if pgo == nil || pgo.KeepLocations != 5 || !pgo.AggregateCallees {
			t.Errorf("missing PGO settings: %v", pgo)
		}
		if request.Format != "PROFILE_FORMAT_PPROF" || request.MaxNodes != 2048 {
			t.Errorf("unexpected output settings: %+v", request)
		}
		if request.Start != "1000" || request.End != "2000" || request.LabelSelector != `{service_name="example"}` || request.ProfileTypeID != "process_cpu:cpu:nanoseconds:cpu:nanoseconds" {
			t.Errorf("unexpected query selection: %+v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"pprof":"cHJvZmlsZQ=="}`))
	}))
	defer server.Close()
	for key, value := range map[string]string{
		"PYROSCOPE_URL": server.URL, "TENANT_ID": "test-tenant", "START_MS": "1000", "END_MS": "2000",
		"LABEL_SELECTOR": `{service_name="example"}`, "PROFILE_TYPE": "process_cpu:cpu:nanoseconds:cpu:nanoseconds",
	} {
		t.Setenv(key, value)
	}
	result := testing.Benchmark(BenchmarkMergePGO)
	if result.N == 0 || calls.Load() <= int64(result.N) {
		t.Fatal("expected warmup and measured requests")
	}
	if result.Extra["queries/s"] <= 0 || result.Extra["response-B/op"] <= 0 {
		t.Fatalf("missing comparable latency/response metrics: %v", result.Extra)
	}
}
