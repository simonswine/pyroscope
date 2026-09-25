package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestControllerAgentBundleAndDownload(t *testing.T) {
	agent, err := openAgentServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	local, remote := net.Pipe()
	client := newProtocolClient(local)
	defer client.Close()
	defer remote.Close()
	done := make(chan error, 1)
	go func() { done <- agent.serveAgent(context.Background(), remote, remote) }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := client.Handshake(ctx, "node-1", true); err != nil {
		t.Fatal(err)
	}
	bundle := t.TempDir()
	file := filepath.Join(bundle, "nested", "binary")
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("bundle"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "empty.patch"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	state := &sessionState{Config: runConfig{RunID: "run-1", Bundle: bundle}, ExpiresAt: time.Now().Add(time.Hour).UTC()}
	digest, err := uploadRunBundle(ctx, client, state)
	if err != nil {
		t.Fatal(err)
	}
	again, err := uploadRunBundle(ctx, client, state)
	if err != nil || again != digest {
		t.Fatalf("repeat: %s %v", again, err)
	}
	paths, _ := newRunPaths(agent.root, "run-1")
	got, err := os.ReadFile(filepath.Join(paths.Bundle, "nested", "binary"))
	if err != nil || string(got) != "bundle" {
		t.Fatalf("bundle: %s %v", got, err)
	}
	archive := []byte("diagnostics archive")
	if err := os.WriteFile(paths.Archive, archive, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(archive)
	if err := atomicJSON(filepath.Join(paths.Root, "results-manifest.json"), artifactInfo{Name: "results.tar.gz", Size: int64(len(archive)), Digest: hex.EncodeToString(sum[:])}); err != nil {
		t.Fatal(err)
	}
	results := t.TempDir()
	if err := client.DownloadArtifact(ctx, "run-1", results); err != nil {
		t.Fatal(err)
	}
	downloaded, err := os.ReadFile(filepath.Join(results, "run-1", "results.tar.gz"))
	if err != nil || string(downloaded) != string(archive) {
		t.Fatalf("download: %s %v", downloaded, err)
	}
	checksum := sha256.Sum256(downloaded)
	remoteInfo, err := agent.artifact("run-1")
	if err != nil || hex.EncodeToString(checksum[:]) != remoteInfo.Digest {
		t.Fatalf("checksum: %+v %v", remoteInfo, err)
	}
	client.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
