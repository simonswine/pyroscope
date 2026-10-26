package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecuteRerunRejectsMissingOwnerWithoutReplay(t *testing.T) {
	paths, err := newRerunPaths(t.TempDir(), "child", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := executeRerun(context.Background(), paths); err == nil {
		t.Fatal("rerun accepted missing owner")
	}
	if _, err := os.Stat(paths.Results); !os.IsNotExist(err) {
		t.Fatalf("rerun wrote results before validation: %v", err)
	}
}

func TestValidateReadyManifestFailsClosed(t *testing.T) {
	root := t.TempDir()
	owner, err := newRunPaths(root, "owner-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(owner.Bundle, 0700); err != nil {
		t.Fatal(err)
	}
	plan := runPlan{Datasets: []datasetReplay{{Name: "series", Tenant: "tenant-1"}}}
	_, err = validateReadyManifest(owner, plan)
	if err == nil || !strings.Contains(err.Error(), "missing readiness record") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateReadyManifestBindsPlanAndTenant(t *testing.T) {
	root := t.TempDir()
	owner, err := newRunPaths(root, "owner-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(owner.Bundle, 0700); err != nil {
		t.Fatal(err)
	}
	plan := runPlan{Datasets: []datasetReplay{{Name: "series", Tenant: "tenant-1"}}}
	if err := writeYAML(filepath.Join(owner.Bundle, "plan.yaml"), plan); err != nil {
		t.Fatal(err)
	}
	digest, err := checksum(filepath.Join(owner.Bundle, "plan.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(owner.Results, 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeYAML(filepath.Join(owner.Results, "windows.yaml"), map[string]replayWindow{"series": {Tenant: "other", Start: 1, End: 2, Pushed: 3}}); err != nil {
		t.Fatal(err)
	}
	windowsSHA, err := checksum(filepath.Join(owner.Results, "windows.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	ready := ingestReadyManifest{Version: 1, OwnerID: owner.ID, PlanSHA: digest, WindowsSHA: windowsSHA, Windows: map[string]replayWindow{"series": {Tenant: "other", Start: 1, End: 2, Pushed: 3}}}
	if err := atomicYAML(filepath.Join(owner.Root, "ingest-ready.yaml"), ready); err != nil {
		t.Fatal(err)
	}
	_, err = validateReadyManifest(owner, plan)
	if err == nil || !strings.Contains(err.Error(), "invalid readiness window") {
		t.Fatalf("unexpected error: %v", err)
	}
}
