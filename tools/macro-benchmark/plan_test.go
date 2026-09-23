package main

import (
	"context"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func testRevisionRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init"}, {"-c", "user.name=Benchmark Test", "-c", "user.email=benchmark@example.invalid", "commit", "--allow-empty", "-m", "baseline"}, {"-c", "user.name=Benchmark Test", "-c", "user.email=benchmark@example.invalid", "commit", "--allow-empty", "-m", "comparison"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	return dir
}

func TestRunPlanDefaultAndOverride(t *testing.T) {
	repo := testRevisionRepo(t)
	inputs := defaultRunConfig().Inputs
	inputs.defaults()
	plan, err := makeRunPlan(context.Background(), repo, inputs)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Benchmarks) != 10 || len(plan.Datasets) != 3 {
		t.Fatalf("unexpected default plan: %+v", plan)
	}
	if plan.Ingest != plan.Comparison || plan.Baseline.Commit == plan.Comparison.Commit {
		t.Fatal("incorrect revision resolution")
	}
	if inputs.Count != 5 || inputs.Benchtime != "5x" {
		t.Fatalf("incorrect measurement defaults: %+v", inputs)
	}
	tenants := map[string]bool{}
	for _, d := range plan.Datasets {
		if tenants[d.Tenant] || !strings.HasPrefix(d.Tenant, d.Name+"-") || len(d.Tenant) != len(d.Name)+9 {
			t.Fatalf("invalid tenant %s", d.Tenant)
		}
		tenants[d.Tenant] = true
	}
	inputs.IngestRef = "HEAD^"
	inputs.Benchmarks = "series-total,series-by-service,merge-tree"
	plan, err = makeRunPlan(context.Background(), repo, inputs)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Ingest != plan.Baseline || len(plan.Datasets) != 2 || len(plan.Benchmarks) != 3 {
		t.Fatalf("incorrect override: %+v", plan)
	}
	inputs.ComparisonRef = "missing-ref"
	if _, err := makeRunPlan(context.Background(), repo, inputs); err == nil {
		t.Fatal("accepted unknown Git ref")
	}
}

func TestBenchmarkSelection(t *testing.T) {
	for _, selection := range []string{"not-a-benchmark", "all-series,all-series", ",", "all-series,"} {
		if _, err := selectedBenchmarks(inputsConfig{Benchmarks: selection}); err == nil {
			t.Fatalf("accepted %q", selection)
		}
	}
	selected, err := selectedBenchmarks(inputsConfig{Benchmarks: "merge-tree, label-names"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(selected, []plannedBenchmark{{Name: "label-names", Dataset: "full-tenant"}, {Name: "merge-tree", Dataset: "high-volume-service"}}) {
		t.Fatalf("unexpected selection: %v", selected)
	}
}

func TestBenchmarkExecutions(t *testing.T) {
	inputs := defaultRunConfig().Inputs
	plan := runPlan{Benchmarks: []plannedBenchmark{{Name: "label-names", Dataset: "full-tenant"}, {Name: "merge-tree", Dataset: "high-volume-service"}}}
	windows := map[string]replayWindow{
		"full-tenant":         {Tenant: "metadata-tenant", Start: 1000, End: 2000, Pushed: 1},
		"high-volume-service": {Tenant: "stacktrace-tenant", Start: 3000, End: 4000, Pushed: 1},
	}
	executions, err := benchmarkExecutions(plan, windows)
	if err != nil {
		t.Fatal(err)
	}
	if len(executions) != 4 {
		t.Fatalf("expected one cluster lifecycle per version per benchmark: %d", len(executions))
	}
	seen := map[string]bool{}
	for i, e := range executions {
		if seen[e.resultDir()] {
			t.Fatal("repetitions would overwrite results/profiles")
		}
		seen[e.resultDir()] = true
		want := windows[plan.Benchmarks[i/2].Dataset]
		if e.Window != want {
			t.Fatalf("incorrect dataset/tenant routing: %+v", e)
		}
		if !slices.Contains(e.queryEnv(inputs, "http://query"), "TENANT_ID="+want.Tenant) {
			t.Fatal("query uses wrong tenant")
		}
		if i%2 == 0 && (e.Role != "baseline" || executions[i+1].Role != "comparison" || e.Window != executions[i+1].Window) {
			t.Fatal("versions do not share a replay window")
		}
	}
	if !slices.Contains(benchmarkArgs(inputs), "-test.benchtime=5x") || !slices.Contains(benchmarkArgs(inputs), "-test.count=5") {
		t.Fatal("each fresh cluster must execute all five repetitions")
	}
	inputs.Count = 7
	if !slices.Contains(benchmarkArgs(inputs), "-test.count=7") {
		t.Fatal("ignored configured repetitions")
	}
	delete(windows, "full-tenant")
	if _, err := benchmarkExecutions(plan, windows); err == nil {
		t.Fatal("accepted a missing replay")
	}
}

func TestBenchtimeValidation(t *testing.T) {
	c := defaultRunConfig().Inputs
	c.defaults()
	for _, value := range []string{"1x", "5x", "1s", "30ms"} {
		c.Benchtime = value
		if err := c.validate(); err != nil {
			t.Fatalf("%s: %v", value, err)
		}
	}
	for _, value := range []string{"0x", "-1x", "0s", "oops", "1.5x", "18446744073709551615x"} {
		c.Benchtime = value
		if err := c.validate(); err == nil {
			t.Fatalf("accepted %s", value)
		}
	}
}
