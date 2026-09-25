package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

func (s *stateStore) createRerun(source sessionState, selected string) (*sessionState, error) {
	if source.Kind == "rerun" {
		return nil, errors.New("select the storage owner to rerun")
	}
	if source.Phase != "completed" && source.Phase != "failed" {
		return nil, errors.New("only completed or benchmark-failed owners can be rerun")
	}
	if source.InstanceID == "" || source.Plan == nil {
		return nil, errors.New("owner has no instance or resolved plan")
	}
	states, err := s.list()
	if err != nil {
		return nil, err
	}
	for _, state := range states {
		if state.InstanceID == source.InstanceID && (activePhase(state.Phase) || state.Phase == "ready") {
			return nil, fmt.Errorf("node busy: %s", state.Config.RunID)
		}
	}
	cfg := source.Config
	cfg.Inputs.Benchmarks = selected
	benchmarks, err := selectedBenchmarks(cfg.Inputs)
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	for _, d := range source.Plan.Datasets {
		allowed[d.Name] = true
	}
	for _, b := range benchmarks {
		if !allowed[b.Dataset] {
			return nil, fmt.Errorf("dataset %s was not replayed by owner", b.Dataset)
		}
	}
	id, err := observabilityName()
	if err != nil {
		return nil, err
	}
	cfg.RunID, cfg.Bundle = id, filepath.Join(s.root, "bundles", id)
	sourceDir, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	plan := *source.Plan
	plan.Benchmarks = benchmarks
	needed := map[string]bool{}
	for _, b := range benchmarks {
		needed[b.Dataset] = true
	}
	plan.Datasets = nil
	for _, d := range source.Plan.Datasets {
		if needed[d.Name] {
			plan.Datasets = append(plan.Datasets, d)
		}
	}
	state := &sessionState{Kind: "rerun", ParentRunID: source.Config.RunID, StorageRunID: source.Config.RunID, SourceDir: sourceDir, Config: cfg, Plan: &plan, Phase: "draft", InstanceID: source.InstanceID, ExpiresAt: time.Now().UTC().Add(time.Duration(cfg.TimeoutMinutes) * time.Minute)}
	if err := s.save(state); err != nil {
		return nil, err
	}
	return state, nil
}

func (s *stateStore) ownerForChild(child *sessionState) (*sessionState, error) {
	if child.Kind != "rerun" || !validSessionID(child.StorageRunID) || child.StorageRunID != child.ParentRunID {
		return nil, errors.New("invalid child owner binding")
	}
	states, err := s.list()
	if err != nil {
		return nil, err
	}
	for _, owner := range states {
		if owner.Config.RunID != child.StorageRunID {
			continue
		}
		if owner.Kind == "rerun" || owner.Phase == "destroying" || owner.Phase == "destroyed" || (owner.Phase != "completed" && owner.Phase != "failed") || owner.InstanceID == "" || owner.InstanceID != child.InstanceID || owner.Config.Region != child.Config.Region {
			return nil, errors.New("storage owner no longer eligible")
		}
		return &owner, nil
	}
	return nil, errors.New("storage owner snapshot missing")
}

func prepareRerunSession(ctx context.Context, store *stateStore, child *sessionState) error {
	owner, err := store.ownerForChild(child)
	if err != nil {
		return err
	}
	child.Phase = "preparing"
	if err := store.save(child); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(child.Config.Bundle), 0700); err != nil {
		return err
	}
	bootDir, err := os.MkdirTemp(filepath.Dir(child.Config.Bundle), ".rerun-agent-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(bootDir)
	if err := command(ctx, child.SourceDir, []string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64"}, "go", "build", "-o", filepath.Join(bootDir, "macro-benchmark"), "."); err != nil {
		return err
	}
	bootstrap := *child
	bootstrap.Config.Bundle = bootDir
	client, err := store.agentForSession(ctx, &bootstrap)
	if err != nil {
		return err
	}
	var remote ownerSnapshot
	if err := client.Request(ctx, "rerun_source", owner.Config.RunID, nil, &remote); err != nil {
		return err
	}
	if !reflect.DeepEqual(remote.Plan.Datasets, owner.Plan.Datasets) || remote.Plan.Ingest != owner.Plan.Ingest || remote.Plan.Baseline != owner.Plan.Baseline || remote.Plan.Comparison != owner.Plan.Comparison {
		return errors.New("remote owner plan differs from saved plan")
	}
	if _, err := os.Lstat(child.Config.Bundle); err == nil {
		if err := verifyBundle(child.Config.Bundle); err != nil {
			return err
		}
		var binding rerunBinding
		if err := readYAML(filepath.Join(child.Config.Bundle, "rerun.yaml"), &binding); err != nil {
			return err
		}
		if binding.Version != 1 || binding.StorageRunID != child.StorageRunID || binding.PlanSHA != remote.PlanSHA || binding.WindowsSHA != remote.WindowsSHA {
			return errors.New("existing child bundle conflicts with owner")
		}
	} else if os.IsNotExist(err) {
		if err := buildRerunBundle(ctx, child, owner, remote); err != nil {
			return err
		}
	} else {
		return err
	}
	child.Phase = "prepared"
	return store.save(child)
}

func buildRerunBundle(ctx context.Context, child, owner *sessionState, remote ownerSnapshot) error {
	original := owner.Config.Bundle
	if err := verifyBundle(original); err != nil {
		return fmt.Errorf("original local bundle unavailable: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(child.Config.Bundle), 0700); err != nil {
		return err
	}
	temp, err := os.MkdirTemp(filepath.Dir(child.Config.Bundle), ".rerun-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	err = filepath.WalkDir(original, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(original, name)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if rel == "checksums.yaml" || rel == "endpoints.yaml" || rel == "fixture.replay" || rel == "profilecli" || rel == "macro-benchmark" || rel == "commit.txt" || rel == "source.patch" || rel == ".macro-benchmark-bundle" || strings.HasPrefix(rel, "cluster-baseline") || strings.HasPrefix(rel, "cluster-comparison") || strings.HasSuffix(rel, ".test") {
			return nil
		}
		dest := filepath.Join(temp, rel)
		if entry.IsDir() {
			return os.MkdirAll(dest, 0700)
		}
		return copyFile(name, dest)
	})
	if err != nil {
		return err
	}
	linuxEnv := []string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64"}
	if err := command(ctx, child.SourceDir, linuxEnv, "go", "build", "-o", filepath.Join(temp, "macro-benchmark"), "."); err != nil {
		return err
	}
	for _, target := range []struct {
		name     string
		revision targetRevision
	}{{"baseline", child.Plan.Baseline}, {"comparison", child.Plan.Comparison}} {
		if err := buildRevisionCluster(ctx, child.SourceDir, target.revision.Commit, filepath.Join(temp, "cluster-"+target.name), filepath.Join(temp, "harness")); err != nil {
			return err
		}
	}
	for _, b := range child.Plan.Benchmarks {
		if err := command(ctx, child.SourceDir, linuxEnv, "go", "test", "-c", "-o", filepath.Join(temp, b.Name+".test"), "./benchmark/"+b.Name); err != nil {
			return err
		}
	}
	if err := writeYAML(filepath.Join(temp, "plan.yaml"), child.Plan); err != nil {
		return err
	}
	if err := writeYAML(filepath.Join(temp, "settings.yaml"), child.Config.Inputs); err != nil {
		return err
	}
	if err := writeYAML(filepath.Join(temp, "rerun.yaml"), rerunBinding{Version: 1, StorageRunID: child.StorageRunID, PlanSHA: remote.PlanSHA, WindowsSHA: remote.WindowsSHA}); err != nil {
		return err
	}
	for file, args := range map[string][]string{"commit.txt": {"rev-parse", "HEAD"}, "source.patch": {"diff", "--binary", "HEAD"}} {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = child.SourceDir
		data, err := cmd.Output()
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(temp, file), data, 0600); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(temp, ".macro-benchmark-bundle"), []byte("v1\n"), 0600); err != nil {
		return err
	}
	hashes := map[string]string{}
	if err := filepath.WalkDir(temp, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(temp, name)
		if err != nil {
			return err
		}
		hash, err := checksum(name)
		if err != nil {
			return err
		}
		hashes[filepath.ToSlash(rel)] = hash
		return nil
	}); err != nil {
		return err
	}
	if err := writeYAML(filepath.Join(temp, "checksums.yaml"), hashes); err != nil {
		return err
	}
	if err := verifyBundle(temp); err != nil {
		return err
	}
	if _, err := os.Lstat(child.Config.Bundle); err == nil {
		return errors.New("child bundle already exists; refusing replacement")
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.Rename(temp, child.Config.Bundle)
}
