package main

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestReplayPushArgs(t *testing.T) {
	args := replayPushArgs("fixture.replay", "http://127.0.0.1:4040", "benchmark")
	if !slices.Contains(args, "--no-loop") {
		t.Fatal("replay must use Kingpin's negated boolean flag")
	}
	for _, arg := range args {
		if strings.HasPrefix(arg, "--loop=") {
			t.Fatalf("unsupported boolean syntax: %s", arg)
		}
	}
}

func TestReplayPushArgsURL(t *testing.T) {
	input := "https://storage.googleapis.com/pyroscope-sample-data/fixture.replay.zst?generation=123"
	args := replayPushArgs(input, "http://127.0.0.1:4040", "benchmark")
	if !slices.Contains(args, "--input="+input) {
		t.Fatalf("replay must use the URL directly: %v", args)
	}
}

// Build profilecli for the host, then set PROFILECLI_TEST_BINARY to exercise
// the real executable without downloading fixtures or starting any services.
func TestReplayCLIExecutable(t *testing.T) {
	binary := os.Getenv("PROFILECLI_TEST_BINARY")
	if binary == "" {
		t.Skip("set PROFILECLI_TEST_BINARY to test the actual executable")
	}
	if err := preflightReplayCLI(context.Background(), binary); err != nil {
		t.Fatal(err)
	}
}
