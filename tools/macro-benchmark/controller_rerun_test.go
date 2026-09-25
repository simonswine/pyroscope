package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCreateRerunPersistsOwnerAndNewDeadline(t *testing.T) {
	store, err := openStateStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := defaultRunConfig()
	cfg.RunID = "owner"
	cfg.TimeoutMinutes = 120
	cfg.Inputs.Benchmarks = "series"
	cfg.Inputs.BaselineRef = "a"
	cfg.Inputs.ComparisonRef = "b"
	cfg.Inputs.MinioURL = "https://example.com/minio"
	cfg.Inputs.MinioSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	owner := sessionState{Config: cfg, Phase: "completed", InstanceID: "i-test", ExpiresAt: time.Now().Add(-time.Hour), Plan: &runPlan{Datasets: []datasetReplay{{Name: "full", Tenant: "full-tenant"}}, Benchmarks: []plannedBenchmark{{Name: "series", Dataset: "full"}}}}
	// A real owner plan and selected registered benchmark must agree on dataset.
	registered, err := selectedBenchmarks(cfg.Inputs)
	if err != nil {
		t.Fatal(err)
	}
	owner.Plan.Benchmarks = registered
	owner.Plan.Datasets[0].Name = registered[0].Dataset
	if err := store.save(&owner); err != nil {
		t.Fatal(err)
	}
	child, err := store.createRerun(owner, "series")
	if err != nil {
		t.Fatal(err)
	}
	if child.StorageRunID != "owner" || child.ParentRunID != "owner" || child.Config.RunID == "owner" || !child.ExpiresAt.After(time.Now()) {
		t.Fatalf("bad child identity or deadline: %+v", child)
	}
	states, err := store.list()
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 {
		t.Fatalf("expected owner and child, got %d", len(states))
	}
	if err := destroySession(context.Background(), store, child); err == nil {
		t.Fatal("child destruction allowed")
	}
	child.Phase = "preparing"
	if err := store.save(child); err != nil {
		t.Fatal(err)
	}
	if err := destroySession(context.Background(), store, &owner); err == nil {
		t.Fatal("owner destroyed while child is preparing")
	}
}

func TestRerunManifestModeBoundToOwner(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "rerun.yaml"), []byte("binding"), 0600); err != nil {
		t.Fatal(err)
	}
	state := sessionState{Kind: "rerun", StorageRunID: "owner"}
	state.Config.Bundle = root
	state.Config.RunID = "child"
	state.ExpiresAt = time.Now().Add(time.Hour)
	m, _, err := sessionRunManifest(&state)
	if err != nil {
		t.Fatal(err)
	}
	if m.Mode != "rerun" || m.StorageOwner != "owner" {
		t.Fatalf("wrong manifest: %+v", m)
	}
	m.StorageOwner = "child"
	if err := validateManifest(m); err == nil {
		t.Fatal("self-owned rerun accepted")
	}
}
