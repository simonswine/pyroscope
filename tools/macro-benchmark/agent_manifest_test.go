package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPrepareAndFinalizeRun(t *testing.T) {
	s, err := openAgentServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	blob := []byte("hello")
	hash := sha256.Sum256(blob)
	digest := hex.EncodeToString(hash[:])
	manifest := runManifest{RunID: "run-1", Deadline: time.Now().Add(time.Hour).UTC(), Files: []manifestFile{{Path: "bin/hello", Digest: digest, Size: int64(len(blob)), Executable: true}}}
	first, err := s.prepareRun(manifest)
	if err != nil || len(first.Missing) != 1 || first.Missing[0] != digest {
		t.Fatalf("prepare=%+v %v", first, err)
	}
	if err := s.finalizeRun(manifest.RunID, first.Digest); err == nil {
		t.Fatal("finalized missing blob")
	}
	if _, err := s.startBlobUpload(blobRequest{Digest: digest, Size: int64(len(blob))}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.uploadChunk(digest, 0, blob); err != nil {
		t.Fatal(err)
	}
	again, err := s.prepareRun(manifest)
	if err != nil || len(again.Missing) != 0 || again.Digest != first.Digest {
		t.Fatalf("repeat=%+v %v", again, err)
	}
	changed := manifest
	changed.Deadline = changed.Deadline.Add(time.Hour)
	if _, err := s.prepareRun(changed); err == nil {
		t.Fatal("accepted manifest conflict")
	}
	if err := s.finalizeRun(manifest.RunID, first.Digest); err != nil {
		t.Fatal(err)
	}
	if err := s.finalizeRun(manifest.RunID, first.Digest); err != nil {
		t.Fatal("repeat: ", err)
	}
	paths, _ := newRunPaths(s.root, manifest.RunID)
	data, err := os.ReadFile(filepath.Join(paths.Bundle, "bin", "hello"))
	if err != nil || string(data) != string(blob) {
		t.Fatalf("bundle file=%q %v", data, err)
	}
	info, err := os.Stat(filepath.Join(paths.Bundle, "bin", "hello"))
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("permissions: %v %v", info, err)
	}
}

func TestManifestRejectsUnsafePaths(t *testing.T) {
	digest := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, name := range []string{"..", "../escape", "/etc/passwd", "a/../b", "a//b", "a/./b", "a\\b", "a/../../b", "."} {
		m := runManifest{RunID: "run-1", Deadline: time.Now(), Files: []manifestFile{{Path: name, Digest: digest, Size: 1}}}
		if err := validateManifest(m); err == nil {
			t.Errorf("accepted %q", name)
		}
	}
	m := runManifest{RunID: "run-1", Deadline: time.Now(), Files: []manifestFile{{Path: "a", Digest: digest, Size: 1}, {Path: "a/b", Digest: digest, Size: 1}}}
	if err := validateManifest(m); err == nil {
		t.Fatal("accepted file/directory conflict")
	}
}
