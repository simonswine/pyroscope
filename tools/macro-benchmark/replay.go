package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func replayPushArgs(input, endpoint, tenant string) []string {
	return []string{"replay", "push", "--input=" + input, "--no-loop", "--url=" + endpoint, "--tenant-id=" + tenant}
}

// Exercise the actual CLI parser before downloading a large fixture. Reaching
// the missing-file error proves these arguments were parsed and replay started.
// No profiles or network requests are sent: replay opens its input first.
func preflightReplayCLI(ctx context.Context, binary string) error {
	dir, err := os.MkdirTemp("", "macro-replay-preflight-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	missing := filepath.Join(dir, "missing.replay")
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, binary, replayPushArgs(missing, "http://127.0.0.1:1", "macro-preflight")...).CombinedOutput()
	if ctx.Err() != nil {
		return fmt.Errorf("profilecli preflight: %w", ctx.Err())
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(output), missing) || !strings.Contains(string(output), "no such file or directory") {
		return fmt.Errorf("profilecli replay argument preflight failed: %v: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// profilecli reports failed pushes in its summary but does not necessarily exit
// nonzero. Never benchmark an incomplete ingestion that merely returned exit 0.
func replaySummary(reader io.Reader) (int64, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	counters := regexp.MustCompile(`(?:^| )pushed=([0-9]+) failed=([0-9]+)(?: |$)`)
	var pushed int64
	complete := false
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, `msg="replay cycle complete"`) {
			continue
		}
		match := counters.FindStringSubmatch(line)
		if complete || len(match) != 3 {
			return 0, fmt.Errorf("invalid or repeated replay completion summary")
		}
		count, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil {
			return 0, err
		}
		if match[2] != "0" || count == 0 {
			return 0, fmt.Errorf("replay incomplete: pushed=%s failed=%s", match[1], match[2])
		}
		complete, pushed = true, count
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	if !complete {
		return 0, fmt.Errorf("no successful replay completion summary")
	}
	return pushed, nil
}
