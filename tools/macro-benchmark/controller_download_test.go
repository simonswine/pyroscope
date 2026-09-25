package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDownloadChecksumMismatchDoesNotPublish(t *testing.T) {
	agent, err := openAgentServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	paths, _ := newRunPaths(agent.root, "run-1")
	if err := os.MkdirAll(paths.Root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Archive, []byte("actual"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(filepath.Join(paths.Root, "results-manifest.json"), artifactInfo{Name: "results.tar.gz", Size: 6, Digest: strings.Repeat("a", 64)}); err != nil {
		t.Fatal(err)
	}
	local, remote := net.Pipe()
	client := newProtocolClient(local)
	defer client.Close()
	defer remote.Close()
	done := make(chan error, 1)
	go func() { done <- agent.serveAgent(context.Background(), remote, remote) }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := client.Handshake(ctx, "node-1", true); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := client.DownloadArtifact(ctx, "run-1", root); err == nil {
		t.Fatal("accepted checksum mismatch")
	}
	if _, err := os.Stat(filepath.Join(root, "run-1", "results.tar.gz")); !os.IsNotExist(err) {
		t.Fatalf("published invalid result: %v", err)
	}
	client.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
