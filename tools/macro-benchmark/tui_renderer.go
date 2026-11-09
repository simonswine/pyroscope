package main

import (
	"fmt"
	"io"
	"strings"
)

// dashboardRenderer owns terminal painting; dashboard layout contains no cursor
// controls. Invalidate whenever another screen or process writes to the terminal.
type dashboardRenderer struct {
	lines         []string
	width, height int
	valid         bool
}

func (r *dashboardRenderer) invalidate() { r.valid = false }

func (r *dashboardRenderer) render(out io.Writer, frame string, width, height int) error {
	lines := strings.Split(strings.TrimSuffix(frame, "\r\n"), "\r\n")
	full := !r.valid || width != r.width || height != r.height
	var b strings.Builder
	if full {
		b.WriteString("\x1b[0m\x1b[H\x1b[2J")
	}
	count := len(lines)
	if !full {
		count = max(count, len(r.lines))
	}
	for i := 0; i < count; i++ {
		line := ""
		if i < len(lines) {
			line = lines[i]
		}
		if !full && i < len(r.lines) && line == r.lines[i] {
			continue
		}
		fmt.Fprintf(&b, "\x1b[%d;1H\x1b[0m%s\x1b[0m\x1b[K", i+1, line)
	}
	if b.Len() != 0 {
		n, err := io.WriteString(out, b.String())
		if err == nil && n != b.Len() {
			err = io.ErrShortWrite
		}
		if err != nil {
			r.invalidate()
			return err
		}
	}
	r.lines, r.width, r.height, r.valid = lines, width, height, true
	return nil
}
