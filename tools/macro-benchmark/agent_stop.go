package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

func (s *agentServer) stopRun(id string) (agentRunSummary, error) {
	paths, err := newRunPaths(s.root, id)
	if err != nil {
		return agentRunSummary{}, err
	}
	run, err := s.runSummary(id)
	if err != nil {
		return agentRunSummary{}, err
	}
	switch run.Phase {
	case "completed", "failed", "stopped", "interrupted":
		return run, nil
	case "starting", "running":
	default:
		return agentRunSummary{}, errors.New("invalid_state: run has not started")
	}
	// Persist stop intent before signaling; a lost response never changes which
	// run was stopped, and the terminal worker status remains authoritative.
	intentPath := filepath.Join(paths.Root, "stop.json")
	if _, err := os.Stat(intentPath); os.IsNotExist(err) {
		if err := atomicJSON(intentPath, struct {
			RequestedAt time.Time `json:"requested_at"`
		}{time.Now().UTC()}); err != nil {
			return agentRunSummary{}, err
		}
	} else if err != nil {
		return agentRunSummary{}, err
	}
	active, err := unitActive(paths.unitName())
	if err != nil {
		return agentRunSummary{}, err
	}
	if active {
		if out, err := exec.Command("systemctl", "stop", paths.unitName()).CombinedOutput(); err != nil {
			return agentRunSummary{}, fmt.Errorf("stop run: %w: %s", err, out)
		}
	}
	return s.runSummary(id)
}
