package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWorkerProgress(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed", true: "failed"}[failed], func(t *testing.T) {
			root := t.TempDir()
			failure := errors.New("work failed")
			err := runWorker(context.Background(), root, func(ctx context.Context) error {
				reportProgress(ctx, "replaying dataset 2/5", time.Time{})
				state, err := loadWorkerState(root)
				if err != nil || state.Progress == nil || state.Progress.Stage != "replaying dataset 2/5" || state.Phase != "running" {
					t.Fatalf("checkpoint: %+v, %v", state, err)
				}
				if state.Progress.StartedAt.IsZero() || state.Progress.UpdatedAt.Before(state.Progress.StartedAt) {
					t.Fatal("missing or invalid timestamps")
				}
				group := context.WithValue(ctx, benchmarkProgressKey{}, "benchmark 3/12 · baseline · series")
				reportBenchmarkProgress(group, "measuring")
				if failed {
					return failure
				}
				return nil
			})
			if failed != errors.Is(err, failure) {
				t.Fatalf("unexpected result: %v", err)
			}
			state, err := loadWorkerState(root)
			if err != nil || state.Progress == nil || state.Progress.Stage != "benchmark 3/12 · baseline · series · measuring" || state.Phase != map[bool]string{false: "completed", true: "failed"}[failed] {
				t.Fatalf("terminal checkpoint: %+v, %v", state, err)
			}
		})
	}
}

func TestProgressRows(t *testing.T) {
	now := time.Now().UTC()
	p := &runProgress{Stage: "settling", StartedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-5 * time.Second), Until: now.Add(45 * time.Second)}
	stage, timing := progressRows(p, "running", now)
	if stage != "settling · 45s remaining" || timing != "elapsed 1m0s · stage updated 5s ago" {
		t.Fatalf("%q / %q", stage, timing)
	}
	stage, _ = progressRows(p, "running", now.Add(time.Minute))
	if stage != "settling · 0s remaining" {
		t.Fatal(stage)
	}
	// Older workers and standalone execution don't need a reporter.
	reportProgress(context.Background(), "ignored", time.Time{})
}
