package benchutil

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestValidateResponse(t *testing.T) {
	for _, body := range []string{`{}`, `{"series":null}`, `{"series":[]}`, `{"series":{}}`, `{"series":""}`, `invalid`} {
		if validateResponse([]byte(body), "series") == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	if err := validateResponse([]byte(`{"series":[{"points":[1]}]}`), "series"); err != nil {
		t.Fatal(err)
	}
}

func TestRun(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/querier.v1.QuerierService/SelectSeries" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-Scope-OrgID") != "test-tenant" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Connect-Protocol-Version") != "1" {
			t.Errorf("unexpected headers: %v", r.Header)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["labelSelector"] != `{service_name="quoted"}` || body["start"] != "1000" || body["end"] != "2000" {
			t.Errorf("unexpected body: %v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"series":[{"points":[1]}]}`)); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	c := Config{URL: server.URL, Tenant: "test-tenant", Start: "1000", End: "2000", Selector: `{service_name="quoted"}`}
	t.Setenv("PROFILE_TYPE", "process_cpu:cpu:nanoseconds:cpu:nanoseconds")
	result := testing.Benchmark(func(b *testing.B) {
		Run(b, c, "SelectSeries", c.ProfileBody(b), "series", 0)
	})
	if result.N == 0 || calls.Load() <= int64(result.N) {
		t.Fatal("expected warmup and measured requests")
	}
	if result.Extra["response-B/op"] <= 0 || result.Extra["queries/s"] <= 0 {
		t.Fatalf("missing benchmark metrics: %v", result.Extra)
	}
}

func TestMetadataBody(t *testing.T) {
	c := Config{Start: "1000", End: "2000", Selector: "{}"}
	body := c.MetadataBody()
	if len(body) != 3 || body["start"] != c.Start || body["end"] != c.End {
		t.Fatalf("unexpected metadata body: %v", body)
	}
	matchers := body["matchers"].([]string)
	if len(matchers) != 1 || matchers[0] != c.Selector {
		t.Fatalf("unexpected matchers: %v", matchers)
	}
}
