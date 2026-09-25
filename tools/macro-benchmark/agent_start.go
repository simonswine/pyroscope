package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type startIntent struct {
	RequestedAt    time.Time `json:"requested_at"`
	Deadline       time.Time `json:"deadline"`
	ManifestDigest string    `json:"manifest_digest"`
}

func unitActive(unit string) (bool, error) {
	output, err := exec.Command("systemctl", "show", unit, "--property=ActiveState", "--value").Output()
	if err != nil {
		return false, fmt.Errorf("inspect unit %s: %w", unit, err)
	}
	switch strings.TrimSpace(string(output)) {
	case "active", "activating", "deactivating":
		return true, nil
	case "inactive", "failed":
		return false, nil
	default:
		return false, fmt.Errorf("unexpected unit state %q", strings.TrimSpace(string(output)))
	}
}

func (s *agentServer) startRun(id, digest string) (agentRunSummary, error) {
	paths, err := newRunPaths(s.root, id)
	if err != nil {
		return agentRunSummary{}, err
	}
	if !validDigest(digest) {
		return agentRunSummary{}, errors.New("invalid manifest digest")
	}
	manifestPath := filepath.Join(paths.Root, "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return agentRunSummary{}, err
	}
	var m runManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return agentRunSummary{}, err
	}
	if err := validateManifest(m); err != nil {
		return agentRunSummary{}, err
	}
	saved, err := manifestDigest(m)
	if err != nil {
		return agentRunSummary{}, err
	}
	if m.RunID != id || saved != digest {
		return agentRunSummary{}, errors.New("manifest_conflict")
	}
	intentPath := filepath.Join(paths.Root, "start.json")
	if previous, err := os.ReadFile(intentPath); err == nil {
		var intent startIntent
		if err := json.Unmarshal(previous, &intent); err != nil {
			return agentRunSummary{}, err
		}
		if intent.ManifestDigest != digest {
			return agentRunSummary{}, errors.New("manifest_conflict")
		}
		return s.runSummary(id)
	} else if !os.IsNotExist(err) {
		return agentRunSummary{}, err
	}
	if time.Now().After(m.Deadline) {
		return agentRunSummary{}, errors.New("deadline_expired")
	}
	if info, err := os.Lstat(paths.Bundle); err != nil || !info.IsDir() {
		return agentRunSummary{}, errors.New("invalid_state: run bundle is not finalized")
	}
	// The agent serializes starts on this connection. The intent scan protects
	// the gap before a submitted worker acquires its lifetime OS lock.
	runs, err := s.listRuns()
	if err != nil {
		return agentRunSummary{}, err
	}
	for _, run := range runs {
		if run.ID != id && (run.Phase == "running" || run.Phase == "starting") {
			return agentRunSummary{}, fmt.Errorf("node_busy: %s", run.ID)
		}
	}
	active, err := unitActive("macro-benchmark.service")
	if err != nil {
		return agentRunSummary{}, err
	}
	if active {
		return agentRunSummary{}, errors.New("node_busy: legacy worker")
	}
	slot, err := acquireExecutionLock(s.root)
	if err != nil {
		return agentRunSummary{}, err
	}
	_ = slot.Close() // systemd worker takes over the slot; intent guards this gap.
	intent := startIntent{RequestedAt: time.Now().UTC(), Deadline: m.Deadline, ManifestDigest: digest}
	if err := atomicJSON(intentPath, intent); err != nil {
		return agentRunSummary{}, err
	}
	binary, err := os.Executable()
	if err != nil {
		return agentRunSummary{}, err
	}
	args := []string{"--unit=" + strings.TrimSuffix(paths.unitName(), ".service"), "--property=Type=exec", "--property=RemainAfterExit=yes", "--property=TimeoutStopSec=240", "--property=KillMode=mixed", binary, "worker", "-run-id", id, "-deadline", strconv.FormatInt(m.Deadline.Unix(), 10)}
	output, err := exec.Command("systemd-run", args...).CombinedOutput()
	if err != nil {
		return agentRunSummary{}, fmt.Errorf("submit worker (reconcile before retry): %w: %s", err, output)
	}
	return agentRunSummary{ID: id, Phase: "starting"}, nil
}
