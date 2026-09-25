package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBootstrapHelperFreshAndCached(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python 3 unavailable")
	}
	root := filepath.Join(t.TempDir(), "agents")
	script := strings.Replace(bootstrapPython, "/var/lib/macro-benchmark/agents", root, 1)
	agentBytes := []byte("#!/bin/sh\nexit 0\n")
	digestBytes := sha256.Sum256(agentBytes)
	digest := hex.EncodeToString(digestBytes[:])
	for i, want := range []byte{'S', 'C'} {
		cmd := exec.Command(python, "-u", "-c", script)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		greeting := make([]byte, 4)
		if _, err := io.ReadFull(stdout, greeting); err != nil || string(greeting) != "MB1\n" {
			t.Fatalf("greeting: %q %v", greeting, err)
		}
		var header [72]byte
		binary.BigEndian.PutUint64(header[:8], uint64(len(agentBytes)))
		copy(header[8:], digest)
		if _, err := stdin.Write(header[:]); err != nil {
			t.Fatal(err)
		}
		response := make([]byte, 1)
		if _, err := io.ReadFull(stdout, response); err != nil || response[0] != want {
			t.Fatalf("attempt %d response: %q %v", i, response, err)
		}
		if i == 0 {
			if _, err := stdin.Write(agentBytes); err != nil {
				t.Fatal(err)
			}
		}
		_ = stdin.Close()
		if err := cmd.Wait(); err != nil {
			t.Fatalf("bootstrap: %v %s", err, &stderr)
		}
	}
	installed, err := os.ReadFile(filepath.Join(root, digest, "macro-benchmark"))
	if err != nil || sha256.Sum256(installed) != digestBytes {
		t.Fatalf("installed binary: %v", err)
	}
}
