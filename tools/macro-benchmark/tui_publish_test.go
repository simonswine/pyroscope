package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublishSessionConfirmation(t *testing.T) {
	state := sessionState{Config: runConfig{RunID: "validating_kestrel_9e0bb1a9da1d", Results: t.TempDir()}, Phase: "completed"}
	if _, err := publishSessionConfirmation(state); err == nil || !strings.Contains(err.Error(), "collect results with g") {
		t.Fatalf("missing archive: %v", err)
	}
	dir := filepath.Join(state.Config.Results, state.Config.RunID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "results.tar.gz"), []byte("archive"), 0600); err != nil {
		t.Fatal(err)
	}
	confirm, err := publishSessionConfirmation(state)
	if err != nil {
		t.Fatal(err)
	}
	if confirm.action != 'P' || confirm.runID != state.Config.RunID || !strings.Contains(confirm.detail, defaultPublishRepo) || !strings.Contains(confirm.title, "PUBLICLY") || !strings.Contains(confirm.detail, "Profiles can expose") {
		t.Fatalf("unsafe confirmation: %+v", confirm)
	}
	view := tuiDisplayWithConfirmation(100, 35, "/state", []sessionState{state}, 0, "", false, confirm)
	if !strings.Contains(view, "PUBLISH REPORT PUBLICLY") {
		t.Fatal("confirmation missing from dashboard")
	}
	state.Kind = "machine"
	if _, err := publishSessionConfirmation(state); err == nil {
		t.Fatal("machine accepted")
	}
}
