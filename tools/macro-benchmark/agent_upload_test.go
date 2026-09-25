package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBlobUploadAndCache(t *testing.T) {
	root := t.TempDir()
	agent, err := openAgentServer(root)
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
	source := filepath.Join(t.TempDir(), "binary")
	bytes := []byte(strings.Repeat("abc", maxFramePayload))
	if err := os.WriteFile(source, bytes, 0600); err != nil {
		t.Fatal(err)
	}
	digestBytes := sha256.Sum256(bytes)
	digest := hex.EncodeToString(digestBytes[:])
	file, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := client.UploadBlob(ctx, digest, file); err != nil {
		t.Fatal(err)
	}
	if err := client.UploadBlob(ctx, digest, file); err != nil {
		t.Fatal("cached: ", err)
	}
	got, err := os.ReadFile(agent.blobPath(digest))
	if err != nil || string(got) != string(bytes) {
		t.Fatalf("published blob: %v", err)
	}
	client.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestBlobUploadFailureNeverPublishes(t *testing.T) {
	agent, err := openAgentServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	digest := strings.Repeat("a", 64)
	if _, err := agent.startBlobUpload(blobRequest{Digest: "../escape", Size: 1}); err == nil {
		t.Fatal("accepted path")
	}
	if _, err := agent.startBlobUpload(blobRequest{Digest: digest, Size: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.uploadChunk(digest, 1, []byte("bad")); err == nil {
		t.Fatal("accepted incorrect offset")
	}
	if _, err := agent.uploadChunk(digest, 0, []byte("bad")); err == nil {
		t.Fatal("accepted incorrect digest")
	}
	if _, err := os.Stat(agent.blobPath(digest)); !os.IsNotExist(err) {
		t.Fatalf("published invalid blob: %v", err)
	}
	if _, err := agent.startBlobUpload(blobRequest{Digest: digest, Size: 3}); err != nil {
		t.Fatal("restart: ", err)
	}
	agent.abortUpload()
	entries, err := os.ReadDir(filepath.Dir(agent.blobPath(digest)))
	if err != nil || len(entries) != 0 {
		t.Fatalf("left partials: %v %v", entries, err)
	}
}
