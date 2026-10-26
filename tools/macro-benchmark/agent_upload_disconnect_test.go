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

func TestAgentUploadDisconnectDiscardsPartial(t *testing.T) {
	agent, err := openAgentServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	local, remote := net.Pipe()
	client := newProtocolClient(local)
	done := make(chan error, 1)
	go func() { done <- agent.serveAgent(context.Background(), remote, remote) }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := client.Handshake(ctx, "node-1", true); err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	var progress blobProgress
	if err := client.Request(ctx, "upload_blob", "", blobRequest{Digest: digest, Size: 2}, &progress); err != nil {
		t.Fatal(err)
	}
	if progress.Offset != 0 || progress.Complete {
		t.Fatalf("progress: %+v", progress)
	}
	client.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(agent.blobPath(digest)); !os.IsNotExist(err) {
		t.Fatalf("partial published: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(agent.blobPath(digest)))
	if err != nil || len(entries) != 0 {
		t.Fatalf("left partial: %v %v", entries, err)
	}
}
