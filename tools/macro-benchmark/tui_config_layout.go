package main

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// renderConfigScreen reserves the footer before allocating the scrolling body.
// Rows contain trusted styling; callers sanitize user-provided text.
func renderConfigScreen(width, height int, header, body, footer []string, focus, offset int, followFocus bool) (string, int) {
	width, height = max(1, width), max(1, height)
	usable := height - 1
	footerHeight := min(len(footer), usable)
	headerHeight := min(len(header), usable-footerHeight)
	bodyHeight := usable - headerHeight - footerHeight
	offset = min(max(0, offset), max(0, len(body)-bodyHeight))
	if followFocus && bodyHeight > 0 {
		focus = min(max(0, focus), max(0, len(body)-1))
		if focus < offset {
			offset = focus
		}
		if focus >= offset+bodyHeight {
			offset = focus - bodyHeight + 1
		}
	}
	rows := append([]string(nil), header[:headerHeight]...)
	for i := 0; i < bodyHeight; i++ {
		text := ""
		if offset+i < len(body) {
			text = body[offset+i]
		}
		rows = append(rows, text)
	}
	rows = append(rows, footer[:footerHeight]...)
	var out strings.Builder
	out.WriteString("\x1b[H")
	for _, row := range rows {
		out.WriteString(ansi.Truncate(row, width-1, ""))
		out.WriteString("\x1b[0m\x1b[K\r\n")
	}
	out.WriteString("\x1b[J")
	return out.String(), offset
}
