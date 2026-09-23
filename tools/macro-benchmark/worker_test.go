package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkerRunsAtMostOnce(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			root := t.TempDir()
			calls := 0
			work := func(context.Context) error {
				calls++
				state, err := loadWorkerState(root)
				if err != nil || state.Phase != "running" {
					t.Fatalf("work began before running checkpoint: %v %v", state, err)
				}
				if fail {
					return errors.New("replay failed")
				}
				return nil
			}
			err := runWorker(context.Background(), root, work)
			if (err != nil) != fail {
				t.Fatalf("error=%v", err)
			}
			if err := runWorker(context.Background(), root, work); err == nil {
				t.Fatal("worker restarted")
			}
			if calls != 1 {
				t.Fatal("duplicate ingestion")
			}
			state, err := loadWorkerState(root)
			want := "completed"
			if fail {
				want = "failed"
			}
			if err != nil || state.Phase != want || state.FinishedAt.IsZero() {
				t.Fatalf("missing final checkpoint: %+v %v", state, err)
			}
		})
	}
}

func TestWorkerCrashBeforeStatusIsNotFresh(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "started"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	state, err := loadWorkerState(root)
	if err != nil || state.Phase != "interrupted" {
		t.Fatalf("%+v %v", state, err)
	}
	if err := runWorker(context.Background(), root, func(context.Context) error { t.Fatal("restarted after crash"); return nil }); err == nil {
		t.Fatal("expected refusal")
	}
}

type fakeWorkerHost struct {
	phase    string
	unit     string
	startErr error
	starts   int
	copies   int
	cancel   context.CancelFunc
}

func (h *fakeWorkerHost) output(_ context.Context, command string) ([]byte, error) {
	if strings.Contains(command, "LoadState") {
		return []byte(h.unit), nil
	}
	if strings.Contains(command, "ActiveState") {
		return []byte("active"), nil
	}
	if h.cancel != nil {
		h.cancel()
	}
	return json.Marshal(workerState{Phase: h.phase})
}
func (h *fakeWorkerHost) ssh(_ context.Context, command string) error {
	if !strings.Contains(command, "systemd-run") || !strings.Contains(command, "worker -deadline") {
		return errors.New("not a detached worker submission")
	}
	h.starts++
	return h.startErr
}
func (h *fakeWorkerHost) copy(_ context.Context, _, dest string, upload bool) error {
	if upload {
		return errors.New("unexpected upload")
	}
	h.copies++
	return os.WriteFile(dest, []byte("archive"), 0600)
}

func TestLostWorkerSubmissionResponseResumesWithoutReplay(t *testing.T) {
	store, err := openStateStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state, err := store.create("checkoutservice")
	if err != nil {
		t.Fatal(err)
	}
	state.ExpiresAt = time.Now().Add(time.Hour)
	host := &fakeWorkerHost{phase: "not-started", unit: "not-found", startErr: errors.New("SSH response lost")}
	if err := followRemote(context.Background(), store, state, host); err == nil {
		t.Fatal("expected lost response")
	}
	states, err := store.list()
	if err != nil {
		t.Fatal(err)
	}
	if !states[0].StartRequested || states[0].Phase != "starting" {
		t.Fatal("start intent was not persisted")
	}
	// The remote job kept running after the controller lost its connection.
	host.phase = "completed"
	if err := followRemote(context.Background(), store, &states[0], host); err != nil {
		t.Fatal(err)
	}
	if host.starts != 1 || host.copies != 1 || states[0].Phase != "completed" {
		t.Fatalf("starts=%d copies=%d phase=%s", host.starts, host.copies, states[0].Phase)
	}
	data, err := os.ReadFile(filepath.Join(state.Config.Results, state.Config.RunID, "results.tar.gz"))
	if err != nil || string(data) != "archive" {
		t.Fatalf("results=%q error=%v", data, err)
	}
}

func TestDetachDoesNotStopRemoteWorker(t *testing.T) {
	store, err := openStateStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state, err := store.create("checkoutservice")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	host := &fakeWorkerHost{phase: "running", cancel: cancel}
	if err := followRemote(ctx, store, state, host); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if host.starts != 0 || host.copies != 0 || state.Phase != "running" {
		t.Fatal("detaching changed remote workload")
	}
}

func TestTerminalTextStripsControlSequences(t *testing.T) {
	got := terminalText("hello\x1b[2J\nworld", 10)
	if strings.ContainsAny(got, "\x1b\n") || len([]rune(got)) > 10 {
		t.Fatalf("unsafe terminal text: %q", got)
	}
}
