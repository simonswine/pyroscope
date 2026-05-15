package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

// Manifest types

type ManifestProfile struct {
	File        string            `json:"file"`
	ServiceName string            `json:"service_name"`
	ProfileType string            `json:"profile_type"`
	SampleType  string            `json:"sample_type"`
	Labels      map[string]string `json:"labels"`
}

type Manifest struct {
	Version  int               `json:"version"`
	Profiles []ManifestProfile `json:"profiles"`
}

// Response types for Pyroscope HTTP API

type profileTypesResponse struct {
	ProfileTypes []struct {
		ID string `json:"id"`
	} `json:"profileTypes"`
}

type labelValuesResponse struct {
	LabelValues []string `json:"label_values"`
	// Some endpoints return it nested differently; handle both shapes.
	Names []string `json:"names"`
}

type flamegraphResponse struct {
	MaxSelf     float64 `json:"maxSelf"`
	Flamebearer struct {
		Levels []interface{} `json:"levels"`
	} `json:"flamebearer"`
}

type seriesResponse struct {
	LabelsSet []interface{} `json:"labelsSet"`
}

// Report types

type CheckResult struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}

type Report struct {
	Passed bool          `json:"passed"`
	Checks []CheckResult `json:"checks"`
}

func main() {
	target := flag.String("target", "http://localhost:4040", "Pyroscope query-frontend URL")
	manifestPath := flag.String("manifest", "benchmarks/dataset/manifest.json", "Path to dataset manifest.json")
	output := flag.String("output", "-", "Output JSON report path (- for stdout)")
	timeout := flag.Duration("timeout", 30*time.Second, "Per-request timeout")
	flag.Parse()

	// Load manifest
	manifest, err := loadManifest(*manifestPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: failed to load manifest: %v\n", err)
		os.Exit(1)
	}

	client := &http.Client{Timeout: *timeout}
	var checks []CheckResult

	// --- Check 1: Profile types exist ---
	checks = append(checks, checkProfileTypes(client, *target, manifest))

	// --- Check 2: Services in label values ---
	checks = append(checks, checkLabelValuesServiceName(client, *target, manifest))

	// --- Check 3: Non-empty flamegraph per service+type ---
	checks = append(checks, checkFlamegraphs(client, *target, manifest)...)

	// --- Check 4: Series count ---
	checks = append(checks, checkSeriesCount(client, *target, manifest))

	// Build report
	allPassed := true
	for _, c := range checks {
		if !c.Passed {
			allPassed = false
		}
	}
	report := Report{
		Passed: allPassed,
		Checks: checks,
	}

	// Print human-readable summary to stderr
	fmt.Fprintf(os.Stderr, "\nCorrectness check summary:\n")
	for _, c := range checks {
		status := "PASS"
		if !c.Passed {
			status = "FAIL"
		}
		fmt.Fprintf(os.Stderr, "  [%s] %s: %s\n", status, c.Name, c.Detail)
	}
	if allPassed {
		fmt.Fprintf(os.Stderr, "\nAll checks passed.\n")
	} else {
		fmt.Fprintf(os.Stderr, "\nSome checks FAILED.\n")
	}

	// Write JSON report
	reportJSON, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: failed to marshal report: %v\n", err)
		os.Exit(1)
	}

	var outWriter io.Writer
	if *output == "-" {
		outWriter = os.Stdout
	} else {
		f, err := os.Create(*output)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: failed to open output file: %v\n", err)
			os.Exit(1)
		}
		defer f.Close()
		outWriter = f
	}
	fmt.Fprintf(outWriter, "%s\n", reportJSON)

	if !allPassed {
		os.Exit(1)
	}
}

// loadManifest reads and parses the manifest file.
func loadManifest(path string) (*Manifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	defer f.Close()
	var m Manifest
	if err := json.NewDecoder(f).Decode(&m); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return &m, nil
}

// uniqueProfileTypes returns the set of unique profile_type values in the manifest.
func uniqueProfileTypes(m *Manifest) []string {
	seen := make(map[string]struct{})
	var result []string
	for _, p := range m.Profiles {
		if _, ok := seen[p.ProfileType]; !ok {
			seen[p.ProfileType] = struct{}{}
			result = append(result, p.ProfileType)
		}
	}
	return result
}

// uniqueServiceNames returns the set of unique service_name values in the manifest.
func uniqueServiceNames(m *Manifest) []string {
	seen := make(map[string]struct{})
	var result []string
	for _, p := range m.Profiles {
		if _, ok := seen[p.ServiceName]; !ok {
			seen[p.ServiceName] = struct{}{}
			result = append(result, p.ServiceName)
		}
	}
	return result
}

// distinctLabelSets returns the count of distinct label set combinations (service_name + labels).
func distinctLabelSets(m *Manifest) int {
	seen := make(map[string]struct{})
	for _, p := range m.Profiles {
		// Build a canonical key from service_name + all labels
		key := "service_name=" + p.ServiceName
		for k, v := range p.Labels {
			key += "," + k + "=" + v
		}
		seen[key] = struct{}{}
	}
	return len(seen)
}

// doGet performs an HTTP GET and returns the body bytes. Returns an error on non-200.
func doGet(client *http.Client, rawURL string) ([]byte, error) {
	resp, err := client.Get(rawURL)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s returned %d: %s", rawURL, resp.StatusCode, string(body))
	}
	return body, nil
}

// checkProfileTypes checks that all profile types from the manifest appear in /pyroscope/profile-types.
func checkProfileTypes(client *http.Client, target string, m *Manifest) CheckResult {
	name := "profile_types"
	endpoint := target + "/pyroscope/profile-types"

	body, err := doGet(client, endpoint)
	if err != nil {
		return CheckResult{Name: name, Passed: false, Detail: fmt.Sprintf("request failed: %v", err)}
	}

	var parsed profileTypesResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return CheckResult{Name: name, Passed: false, Detail: fmt.Sprintf("parse error: %v", err)}
	}

	// Build set of returned IDs
	found := make(map[string]struct{})
	for _, pt := range parsed.ProfileTypes {
		found[pt.ID] = struct{}{}
	}

	expected := uniqueProfileTypes(m)
	var missing []string
	for _, pt := range expected {
		if _, ok := found[pt]; !ok {
			missing = append(missing, pt)
		}
	}

	if len(missing) > 0 {
		return CheckResult{Name: name, Passed: false, Detail: fmt.Sprintf("missing: %v", missing)}
	}
	return CheckResult{Name: name, Passed: true, Detail: fmt.Sprintf("found: %v", expected)}
}

// checkLabelValuesServiceName checks that all service names appear in /pyroscope/label-values?label=service_name.
func checkLabelValuesServiceName(client *http.Client, target string, m *Manifest) CheckResult {
	name := "label_values_service_name"
	endpoint := target + "/pyroscope/label-values?label=service_name"

	body, err := doGet(client, endpoint)
	if err != nil {
		return CheckResult{Name: name, Passed: false, Detail: fmt.Sprintf("request failed: %v", err)}
	}

	// The response may be a plain JSON array or an object; try both shapes.
	var values []string
	// First try as a plain array
	if err2 := json.Unmarshal(body, &values); err2 != nil {
		// Try as object with "label_values" or "names" field
		var parsed labelValuesResponse
		if err3 := json.Unmarshal(body, &parsed); err3 != nil {
			return CheckResult{Name: name, Passed: false, Detail: fmt.Sprintf("parse error: %v", err3)}
		}
		if len(parsed.LabelValues) > 0 {
			values = parsed.LabelValues
		} else {
			values = parsed.Names
		}
	}

	found := make(map[string]struct{})
	for _, v := range values {
		found[v] = struct{}{}
	}

	expected := uniqueServiceNames(m)
	var missing []string
	for _, svc := range expected {
		if _, ok := found[svc]; !ok {
			missing = append(missing, svc)
		}
	}

	if len(missing) > 0 {
		return CheckResult{Name: name, Passed: false, Detail: fmt.Sprintf("missing: %v", missing)}
	}
	return CheckResult{Name: name, Passed: true, Detail: fmt.Sprintf("found: %v", expected)}
}

// checkFlamegraphs returns one CheckResult per unique service in the manifest.
func checkFlamegraphs(client *http.Client, target string, m *Manifest) []CheckResult {
	// Collect unique (service_name, profile_type) pairs
	type key struct{ service, profileType string }
	seen := make(map[key]struct{})
	var pairs []key
	for _, p := range m.Profiles {
		k := key{p.ServiceName, p.ProfileType}
		if _, ok := seen[k]; !ok {
			seen[k] = struct{}{}
			pairs = append(pairs, k)
		}
	}

	var results []CheckResult
	for _, pair := range pairs {
		checkName := "flamegraph_" + pair.service

		// Build query: {service_name="<svc>"}.<profileType>{}
		// The render endpoint expects query like: process_cpu:cpu:nanoseconds:cpu:nanoseconds{service_name="goapp-http"}
		query := pair.profileType + `{service_name="` + pair.service + `"}`

		params := url.Values{}
		params.Set("query", query)
		params.Set("from", "now-1h")
		params.Set("until", "now")
		params.Set("format", "json")
		endpoint := target + "/pyroscope/render?" + params.Encode()

		body, err := doGet(client, endpoint)
		if err != nil {
			results = append(results, CheckResult{
				Name:   checkName,
				Passed: false,
				Detail: fmt.Sprintf("request failed: %v", err),
			})
			continue
		}

		var fg flamegraphResponse
		if err := json.Unmarshal(body, &fg); err != nil {
			results = append(results, CheckResult{
				Name:   checkName,
				Passed: false,
				Detail: fmt.Sprintf("parse error: %v", err),
			})
			continue
		}

		nonEmpty := fg.MaxSelf != 0 || len(fg.Flamebearer.Levels) > 0
		if !nonEmpty {
			results = append(results, CheckResult{
				Name:   checkName,
				Passed: false,
				Detail: "empty flamegraph (maxSelf=0 and no levels)",
			})
			continue
		}

		results = append(results, CheckResult{
			Name:   checkName,
			Passed: true,
			Detail: "non-empty",
		})
	}
	return results
}

// checkSeriesCount checks that the total series count is at least the number of distinct label sets.
func checkSeriesCount(client *http.Client, target string, m *Manifest) CheckResult {
	name := "series_count"

	params := url.Values{}
	params.Set("matchers[]", `{__name__=~".+"}`)
	endpoint := target + "/pyroscope/series?" + params.Encode()

	body, err := doGet(client, endpoint)
	if err != nil {
		return CheckResult{Name: name, Passed: false, Detail: fmt.Sprintf("request failed: %v", err)}
	}

	var parsed seriesResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return CheckResult{Name: name, Passed: false, Detail: fmt.Sprintf("parse error: %v", err)}
	}

	got := len(parsed.LabelsSet)
	expected := distinctLabelSets(m)

	detail := fmt.Sprintf("got %d, expected >= %d", got, expected)
	if got < expected {
		return CheckResult{Name: name, Passed: false, Detail: detail}
	}
	return CheckResult{Name: name, Passed: true, Detail: detail}
}
