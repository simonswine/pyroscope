package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestArchiveSession(t *testing.T) {
	store, err := openStateStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	draft, err := store.create("")
	if err != nil {
		t.Fatal(err)
	}
	id := draft.Config.RunID
	if err := store.archive(id); err != nil {
		t.Fatal(err)
	}
	states, err := store.list()
	if err != nil || len(states) != 0 {
		t.Fatalf("archive still listed: %v %v", states, err)
	}
	if _, err := os.Stat(filepath.Join(store.root, "archived-sessions", id, "state.json")); err != nil {
		t.Fatal(err)
	}
	if err := store.archive(id); err == nil {
		t.Fatal("archived twice")
	}
	if err := store.save(draft); err != nil {
		t.Fatal(err)
	}
	if err := store.archive(id); err == nil {
		t.Fatal("overwrote archived session")
	}
}

func TestArchiveRejectsRunningAndOwnedDraft(t *testing.T) {
	store, err := openStateStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state, err := store.create("")
	if err != nil {
		t.Fatal(err)
	}
	state.InstanceID = "i-owner"
	if err := store.save(state); err != nil {
		t.Fatal(err)
	}
	if err := store.archive(state.Config.RunID); err == nil {
		t.Fatal("archived owned draft")
	}
	state.InstanceID = ""
	state.Phase = "running"
	if err := store.save(state); err != nil {
		t.Fatal(err)
	}
	if err := store.archive(state.Config.RunID); err == nil {
		t.Fatal("archived active run")
	}
	state.Phase = "destroyed"
	if err := store.save(state); err != nil {
		t.Fatal(err)
	}
	if err := store.archive(state.Config.RunID); err != nil {
		t.Fatal(err)
	}
}
