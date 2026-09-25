package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStartIntentNeverResubmitsTerminalRun(t *testing.T) {
	s, err := openAgentServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sum := sha256.Sum256([]byte("binary"))
	m := runManifest{RunID: "run-1", Deadline: time.Now().Add(time.Hour).UTC(), Files: []manifestFile{{Path: "binary", Digest: hex.EncodeToString(sum[:]), Size: 6}}}
	prepared, err := s.prepareRun(m)
	if err != nil {
		t.Fatal(err)
	}
	paths, _ := newRunPaths(s.root, m.RunID)
	if err := os.MkdirAll(paths.Bundle, 0700); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(filepath.Join(paths.Root, "start.json"), startIntent{RequestedAt: time.Now().UTC(), ManifestDigest: prepared.Digest, Deadline: m.Deadline}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.Worker, 0700); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(filepath.Join(paths.Worker, "status.json"), workerState{Phase: "completed"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		run, err := s.startRun(m.RunID, prepared.Digest)
		if err != nil || run.Phase != "completed" {
			t.Fatalf("repeat %d: %+v %v", i, run, err)
		}
	}
	if _, err := s.startRun(m.RunID, hex.EncodeToString(make([]byte, 32))); err == nil {
		t.Fatal("accepted conflicting start")
	}
	if _, err := os.Stat(filepath.Join(paths.Worker, "started")); !os.IsNotExist(err) {
		t.Fatalf("replayed terminal worker: %v", err)
	}
}
