package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStateDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, tt := range []struct{ env, want string }{
		{"", filepath.Join(home, ".local", "state")},
		{"relative-path", filepath.Join(home, ".local", "state")},
		{filepath.Join(home, "custom"), filepath.Join(home, "custom")},
	} {
		t.Setenv("XDG_STATE_HOME", tt.env)
		got, err := stateDirectory()
		if err != nil || got != filepath.Join(tt.want, "pyroscope-macro-benchmark") {
			t.Fatalf("path=%q error=%v", got, err)
		}
	}
}

func TestStateStoreResumeAndLock(t *testing.T) {
	root := t.TempDir()
	store, err := openStateStore(root)
	if err != nil {
		t.Fatal(err)
	}
	other, err := openStateStore(root)
	if err == nil {
		other.Close()
		t.Fatal("concurrent controller acquired lock")
	}
	state, err := store.create("checkoutservice")
	if err != nil {
		t.Fatal(err)
	}
	state.Phase, state.InstanceID, state.StartRequested = "running", "i-test", true
	if err := store.save(state); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{root, store.path(state.Config.RunID)} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm()&0077 != 0 {
			t.Fatalf("private state permissions: %s (%v)", path, err)
		}
	}
	for _, path := range []string{state.Config.KeyPath, state.Config.Bundle, state.Config.Results} {
		if !strings.HasPrefix(path, root+string(filepath.Separator)) {
			t.Fatalf("path outside state root: %s", path)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = openStateStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	states, err := store.list()
	if err != nil || len(states) != 1 {
		t.Fatalf("states=%v error=%v", states, err)
	}
	if states[0].InstanceID != "i-test" || states[0].Phase != "running" || !states[0].StartRequested {
		t.Fatal("lost checkpoint")
	}
}

func TestAtomicJSONKeepsPreviousSnapshotOnFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := atomicJSON(path, "original"); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(path, make(chan int)); err == nil {
		t.Fatal("expected serialization failure")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "\"original\"\n" {
		t.Fatalf("original snapshot lost: %q %v", data, err)
	}
}

func TestStateStoreRejectsCorruption(t *testing.T) {
	for _, kind := range []string{"json", "version", "identity"} {
		t.Run(kind, func(t *testing.T) {
			store, err := openStateStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			state, err := store.create("checkoutservice")
			if err != nil {
				t.Fatal(err)
			}
			path := store.path(state.Config.RunID)
			var data []byte
			switch kind {
			case "json":
				data = []byte("{")
			case "version":
				state.Version++
				data, err = json.Marshal(state)
			case "identity":
				state.Config.RunID = "another-session"
				data, err = json.Marshal(state)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.list(); err == nil {
				t.Fatal("silently accepted corrupt state")
			}
		})
	}
}

func TestCancellationPreservesCheckpoint(t *testing.T) {
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
	cancel()
	err = sessionAction(ctx, store, state, func(ctx context.Context, store *stateStore, state *sessionState) error {
		state.Phase, state.InstanceID = "running", "i-keep"
		if err := store.save(state); err != nil {
			return err
		}
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	states, err := store.list()
	if err != nil {
		t.Fatal(err)
	}
	if states[0].Phase != "running" || states[0].InstanceID != "i-keep" || states[0].Error != "" {
		t.Fatalf("cancellation changed remote intent: %+v", states[0])
	}
}

func TestStateStoreRejectsUnsafeID(t *testing.T) {
	store, err := openStateStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, id := range []string{"", "../outside", "a/b", ".", strings.Repeat("x", 65)} {
		if err := store.save(&sessionState{Config: runConfig{RunID: id}}); err == nil {
			t.Fatalf("accepted %q", id)
		}
	}
}
