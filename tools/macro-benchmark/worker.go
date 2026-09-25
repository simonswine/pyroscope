package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

type workerState struct {
	Phase      string
	Error      string
	FinishedAt time.Time
}

func loadWorkerState(root string) (workerState, error) {
	data, err := os.ReadFile(filepath.Join(root, "status.json"))
	if os.IsNotExist(err) {
		if _, err := os.Stat(filepath.Join(root, "started")); err == nil {
			return workerState{Phase: "interrupted", Error: "worker stopped before publishing status; replay will not be restarted"}, nil
		} else if !os.IsNotExist(err) {
			return workerState{}, err
		}
		return workerState{Phase: "not-started"}, nil
	}
	if err != nil {
		return workerState{}, err
	}
	var state workerState
	err = json.Unmarshal(data, &state)
	return state, err
}

// The durable marker is never removed. A failed ingestion needs a new run ID.
func runWorker(ctx context.Context, root string, work func(context.Context) error) error {
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	marker, err := os.OpenFile(filepath.Join(root, "started"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("worker already started or cannot claim recording: %w", err)
	}
	if err := errors.Join(marker.Sync(), marker.Close()); err != nil {
		return err
	}
	if err := atomicJSON(filepath.Join(root, "status.json"), workerState{Phase: "running"}); err != nil {
		return err
	}
	err = work(ctx)
	state := workerState{Phase: "completed", FinishedAt: time.Now().UTC()}
	if err != nil {
		state.Phase, state.Error = "failed", err.Error()
		if errors.Is(err, context.Canceled) {
			if _, stopErr := os.Stat(filepath.Join(filepath.Dir(root), "stop.json")); stopErr == nil {
				state.Phase = "stopped"
			}
		}
	}
	return errors.Join(err, atomicJSON(filepath.Join(root, "status.json"), state))
}

func worker(ctx context.Context, args []string) error {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return errors.New("worker requires the disposable Linux host as root")
	}
	flags := flag.NewFlagSet("worker", flag.ContinueOnError)
	deadline := flags.Int64("deadline", 0, "Absolute execution deadline in Unix seconds")
	runID := flags.String("run-id", "", "Required run ID")
	if err := flags.Parse(args); err != nil {
		return err
	}
	paths, err := newRunPaths(agentRoot, *runID)
	if err != nil {
		return err
	}
	if *deadline <= time.Now().Unix() {
		return errors.New("worker deadline has expired")
	}
	ctx, cancel := context.WithDeadline(ctx, time.Unix(*deadline, 0))
	defer cancel()
	lock, err := acquireExecutionLock(agentRoot)
	if err != nil {
		return err
	}
	defer lock.Close()
	active, err := unitActive("macro-benchmark.service")
	if err != nil {
		return fmt.Errorf("check legacy worker: %w", err)
	}
	if active {
		return errors.New("node_busy: legacy worker is active")
	}
	if _, err := os.Stat(filepath.Join(paths.Root, "stop.json")); err == nil {
		if err := os.MkdirAll(paths.Worker, 0700); err != nil {
			return err
		}
		return atomicJSON(filepath.Join(paths.Worker, "status.json"), workerState{Phase: "stopped", FinishedAt: time.Now().UTC()})
	} else if !os.IsNotExist(err) {
		return err
	}
	return runWorker(ctx, paths.Worker, func(ctx context.Context) error { return executeWithPaths(ctx, paths) })
}
