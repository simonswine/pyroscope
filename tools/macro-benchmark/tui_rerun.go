package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// Only used when old benchmark names no longer exist. Never silently select
// every registered benchmark after a rename.
func promptRerunBenchmarks(tty *os.File, owner sessionState) (string, error) {
	var choices []string
	for _, b := range owner.Plan.Benchmarks {
		choices = append(choices, b.Name)
	}
	var input []byte
	for {
		fmt.Fprintf(tty, "\x1b[H\x1b[2JRerun %s: original selection %s is no longer registered.\r\nEnter comma-separated replacement benchmark names (Esc cancels): %s", owner.Config.RunID, strings.Join(choices, ","), string(input))
		var buf [1]byte
		if _, err := tty.Read(buf[:]); err != nil {
			return "", err
		}
		switch buf[0] {
		case 27, 3:
			return "", errors.New("rerun selection cancelled")
		case '\r', '\n':
			if len(input) == 0 {
				return "", errors.New("replacement benchmark selection required")
			}
			return string(input), nil
		case 127, 8:
			if len(input) > 0 {
				input = input[:len(input)-1]
			}
		default:
			if buf[0] >= 32 && buf[0] < 127 && len(input) < 512 {
				input = append(input, buf[0])
			}
		}
	}
}
