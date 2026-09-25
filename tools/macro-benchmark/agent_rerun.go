package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
)

type ownerSnapshot struct {
	Plan       runPlan                 `json:"plan"`
	Windows    map[string]replayWindow `json:"windows"`
	PlanSHA    string                  `json:"plan_sha256"`
	WindowsSHA string                  `json:"windows_sha256"`
}

func (s *agentServer) rerunSource(id string) (ownerSnapshot, error) {
	owner, err := newRunPaths(s.root, id)
	if err != nil {
		return ownerSnapshot{}, err
	}
	status, err := s.runSummary(id)
	if err != nil {
		return ownerSnapshot{}, err
	}
	if status.Phase != "completed" && status.Phase != "failed" {
		return ownerSnapshot{}, fmt.Errorf("owner worker is %s", status.Phase)
	}
	for _, dir := range []string{owner.Root, owner.Bundle, owner.Results, owner.Ingest, owner.Minio} {
		info, err := os.Lstat(dir)
		if err != nil {
			return ownerSnapshot{}, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ownerSnapshot{}, fmt.Errorf("unsafe storage directory %s", dir)
		}
	}
	if err := verifyBundle(owner.Bundle); err != nil {
		return ownerSnapshot{}, err
	}
	if _, err := readStorageCredentials(owner); err != nil {
		return ownerSnapshot{}, fmt.Errorf("owner credentials unavailable: %w", err)
	}
	var p runPlan
	if err := readYAML(filepath.Join(owner.Bundle, "plan.yaml"), &p); err != nil {
		return ownerSnapshot{}, err
	}
	windows, err := validateReadyManifest(owner, p)
	if err != nil {
		return ownerSnapshot{}, err
	}
	var persisted map[string]replayWindow
	if err := readYAML(filepath.Join(owner.Results, "windows.yaml"), &persisted); err != nil {
		return ownerSnapshot{}, err
	}
	if !reflect.DeepEqual(windows, persisted) {
		return ownerSnapshot{}, errors.New("owner windows differ from readiness record")
	}
	planSHA, err := checksum(filepath.Join(owner.Bundle, "plan.yaml"))
	if err != nil {
		return ownerSnapshot{}, err
	}
	windowSHA, err := checksum(filepath.Join(owner.Results, "windows.yaml"))
	if err != nil {
		return ownerSnapshot{}, err
	}
	return ownerSnapshot{Plan: p, Windows: windows, PlanSHA: planSHA, WindowsSHA: windowSHA}, nil
}
