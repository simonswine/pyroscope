package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
				t.Fatalf("final checkpoint: %+v %v", state, err)
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

func TestTerminalTextStripsControlSequences(t *testing.T) {
	got := terminalText("hello\x1b[2J\nworld", 10)
	if strings.ContainsAny(got, "\x1b\n") || len([]rune(got)) > 10 {
		t.Fatalf("unsafe terminal text: %q", got)
	}
}
