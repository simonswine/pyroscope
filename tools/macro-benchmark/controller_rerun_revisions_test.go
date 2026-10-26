package main

import (
	"context"
	"reflect"
	"testing"
)

func TestRerunNewReadRevisionsAndBenchmarks(t *testing.T) {
	repo := testRevisionRepo(t)
	t.Chdir(repo)
	store, err := openStateStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := defaultRunConfig()
	cfg.RunID = "owner"
	cfg.Inputs.defaults()
	cfg.Inputs.Benchmarks = "series"
	plan, err := makeRunPlan(context.Background(), repo, cfg.Inputs)
	if err != nil {
		t.Fatal(err)
	}
	owner := sessionState{Config: cfg, Plan: plan, Phase: "completed", InstanceID: "i-test"}
	if err := store.save(&owner); err != nil {
		t.Fatal(err)
	}
	original := *owner.Plan
	inputs := cfg.Inputs
	inputs.BaselineRef, inputs.ComparisonRef = "HEAD", "HEAD^"
	inputs.Benchmarks = "query-wide"
	inputs.Count = 3
	inputs.Benchtime = "2x"
	// Even a caller supplying a different ingest ref cannot change stored ingestion.
	inputs.IngestRef = "HEAD^"
	child, err := store.createRerunWithInputs(owner, inputs.Benchmarks, &inputs)
	if err != nil {
		t.Fatal(err)
	}
	if child.InstanceID != owner.InstanceID || child.StorageRunID != owner.Config.RunID {
		t.Fatal("storage owner changed")
	}
	if child.Plan.Baseline.Commit != original.Comparison.Commit || child.Plan.Comparison.Commit != original.Baseline.Commit {
		t.Fatal("new read revisions not resolved")
	}
	if child.Plan.Ingest != original.Ingest || !reflect.DeepEqual(child.Plan.Datasets, original.Datasets) {
		t.Fatal("replay state changed")
	}
	if child.Plan.Benchmarks[0].Name != "query-wide" || child.Config.Inputs.Count != 3 || child.Config.Inputs.Benchtime != "2x" {
		t.Fatal("new measurements not selected")
	}
	if err := child.Plan.validate(child.Config.Inputs); err != nil {
		t.Fatalf("invalid child plan: %v", err)
	}
	if !reflect.DeepEqual(*owner.Plan, original) {
		t.Fatal("owner mutated")
	}
	if _, err := store.createRerunWithInputs(owner, "query-pod", &inputs); err == nil {
		t.Fatal("accepted unreplayed dataset")
	}
	inputs.BaselineRef = "nonexistent-revision"
	if _, err := store.createRerunWithInputs(owner, "query-wide", &inputs); err == nil {
		t.Fatal("accepted invalid revision")
	}
}
