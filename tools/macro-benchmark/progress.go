package main

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"time"
)

// Progress is a durable stage checkpoint, not a heartbeat or estimated percentage.
type runProgress struct {
	Stage     string    `json:"stage"`
	StartedAt time.Time `json:"started_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Until     time.Time `json:"until,omitempty"`
}

type benchmarkProgressKey struct{}

func reportBenchmarkProgress(ctx context.Context, stage string) {
	if label, ok := ctx.Value(benchmarkProgressKey{}).(string); ok {
		stage = label + " · " + stage
	}
	reportProgress(ctx, stage, time.Time{})
}

type progressContextKey struct{}
type progressReporter struct {
	root  string
	state *workerState
}

// Execution reports from its main goroutine, including deferred cleanup. Standalone
// execution without a worker remains supported. Reporting must not abort workloads.
func reportProgress(ctx context.Context, stage string, until time.Time) {
	r, ok := ctx.Value(progressContextKey{}).(*progressReporter)
	if !ok {
		return
	}
	r.state.Progress.Stage = stage
	r.state.Progress.UpdatedAt = time.Now().UTC()
	r.state.Progress.Until = until
	if err := atomicJSON(filepath.Join(r.root, "status.json"), r.state); err != nil {
		log.Printf("publish progress: %v", err)
	}
}

func progressDuration(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	return d.Round(time.Second)
}

func progressRows(p *runProgress, phase string, now time.Time) (stage, timing string) {
	stage = p.Stage
	elapsed := progressDuration(now.Sub(p.StartedAt))
	age := progressDuration(now.Sub(p.UpdatedAt))
	timing = fmt.Sprintf("elapsed %s · stage updated %s ago", elapsed, age)
	if phase == "running" && !p.Until.IsZero() {
		remaining := progressDuration(p.Until.Sub(now))
		stage += fmt.Sprintf(" · %s remaining", remaining)
	}
	return stage, timing
}
