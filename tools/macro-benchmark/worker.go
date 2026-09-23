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
	"strings"
	"time"
)

const workerDirectory = "/var/lib/macro-benchmark/worker"

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

// The durable marker is never removed: restarting a service must not replay
// into an already partially populated tenant. A failed run requires a new session.
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
	}
	return errors.Join(err, atomicJSON(filepath.Join(root, "status.json"), state))
}

func worker(ctx context.Context, args []string) error {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return errors.New("worker requires the disposable Linux host as root")
	}
	flags := flag.NewFlagSet("worker", flag.ContinueOnError)
	deadline := flags.Int64("deadline", 0, "Absolute instance deadline in Unix seconds")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *deadline <= time.Now().Unix() {
		return errors.New("worker deadline has expired")
	}
	ctx, cancel := context.WithDeadline(ctx, time.Unix(*deadline, 0))
	defer cancel()
	return runWorker(ctx, workerDirectory, execute)
}

type workerHost interface {
	ssh(context.Context, string) error
	output(context.Context, string) ([]byte, error)
	copy(context.Context, string, string, bool) error
}

func remoteWorkerState(ctx context.Context, host workerHost) (workerState, error) {
	data, err := host.output(ctx, "sudo /tmp/benchmark/macro-benchmark worker-status")
	if err != nil {
		return workerState{}, err
	}
	var state workerState
	if err := json.Unmarshal(data, &state); err != nil {
		return state, fmt.Errorf("remote worker status: %w", err)
	}
	return state, nil
}

func followRemote(ctx context.Context, store *stateStore, state *sessionState, host workerHost) error {
	for {
		remote, err := remoteWorkerState(ctx, host)
		if err != nil {
			return err
		}
		switch remote.Phase {
		case "not-started":
			if time.Now().After(state.ExpiresAt) {
				return errors.New("worker deadline expired; refusing to start")
			}
			// Submission may have succeeded while SSH lost its response, before
			// the worker got CPU time to publish its marker.
			load, err := host.output(ctx, "sudo systemctl show macro-benchmark.service --property=LoadState --value")
			if err != nil {
				return err
			}
			if strings.TrimSpace(string(load)) == "loaded" {
				active, err := host.output(ctx, "sudo systemctl show macro-benchmark.service --property=ActiveState --value")
				if err != nil {
					return err
				}
				if value := strings.TrimSpace(string(active)); value != "active" && value != "activating" {
					state.Phase = "failed"
					return errors.New("submitted worker did not start; inspect the remote systemd journal")
				}
				if err := waitPoll(ctx, 3*time.Second); err != nil {
					return err
				}
				continue
			}
			if strings.TrimSpace(string(load)) != "not-found" {
				return fmt.Errorf("unexpected worker unit state %q", strings.TrimSpace(string(load)))
			}
			state.StartRequested = true
			state.Phase = "starting"
			if err := store.save(state); err != nil {
				return err
			}
			// systemd owns the process and its children, not the SSH connection.
			// A lost start response is reconciled on the next resume. The worker's
			// exclusive marker independently prevents double ingestion.
			command := fmt.Sprintf("sudo systemd-run --unit=macro-benchmark --property=Type=exec --property=RemainAfterExit=yes --property=TimeoutStopSec=240 --property=KillMode=mixed /tmp/benchmark/macro-benchmark worker -deadline %d", state.ExpiresAt.Unix())
			if err := host.ssh(ctx, command); err != nil {
				return fmt.Errorf("submit worker (resume to reconcile): %w", err)
			}
		case "running":
			active, err := host.output(ctx, "sudo systemctl show macro-benchmark.service --property=ActiveState --value")
			if err != nil {
				return err
			}
			if value := strings.TrimSpace(string(active)); value != "active" && value != "activating" && value != "deactivating" {
				// The worker can publish its final status between our two reads.
				latest, err := remoteWorkerState(ctx, host)
				if err != nil {
					return err
				}
				if latest.Phase == "completed" || latest.Phase == "failed" {
					continue
				}
				state.Phase = "interrupted"
				return errors.New("worker is no longer active; refusing to replay automatically (collect diagnostics or destroy)")
			}
			state.Phase = "running"
			if err := store.save(state); err != nil {
				return err
			}
		case "completed", "failed":
			state.Phase, state.Error = remote.Phase, remote.Error
			if err := store.save(state); err != nil {
				return err
			}
			if err := collectFromHost(ctx, state, host); err != nil {
				return err
			}
			if remote.Phase == "failed" {
				return errors.New(remote.Error)
			}
			return nil
		case "interrupted":
			state.Phase = "interrupted"
			return errors.New(remote.Error)
		default:
			return fmt.Errorf("unknown remote worker phase %q", remote.Phase)
		}
		if err := waitPoll(ctx, 3*time.Second); err != nil {
			return err
		}
	}
}
