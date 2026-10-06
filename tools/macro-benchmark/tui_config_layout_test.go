package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestConfigScreenLayout(t *testing.T) {
	header := []string{"Configure", "Header"}
	footer := []string{"Save", "Esc cancel", "Error: keep entered values"}
	body := make([]string, 50)
	for i := range body {
		body[i] = fmt.Sprintf("field-%02d %s", i, strings.Repeat("界", 100))
	}
	for _, size := range [][2]int{{100, 30}, {40, 12}, {10, 5}, {1, 1}, {150, 60}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			offset := 0
			for _, focus := range []int{0, 25, 49, 1} {
				view, next := renderConfigScreen(size[0], size[1], header, body, footer, focus, offset, true)
				offset = next
				plain := ansi.Strip(view)
				if got := strings.Count(plain, "\r\n"); got != size[1]-1 {
					t.Fatalf("got %d rows", got)
				}
				for _, line := range strings.Split(plain, "\r\n") {
					if ansi.StringWidth(line) > size[0]-1 {
						t.Fatalf("line exceeds width: %q", line)
					}
				}
				if size[1] >= 12 {
					if !strings.Contains(plain, fmt.Sprintf("field-%02d", focus)) {
						t.Fatal("focus is off screen")
					}
					lines := strings.Split(plain, "\r\n")
					if lines[size[1]-4] != "Save" || lines[size[1]-3] != "Esc cancel" {
						t.Fatal("footer moved")
					}
					if !strings.Contains(plain, "Error: keep entered values") {
						t.Fatal("missing inline error")
					}
				}
			}
		})
	}
}

func TestConfigScreenSaveKeepsViewport(t *testing.T) {
	body := make([]string, 40)
	_, offset := renderConfigScreen(80, 12, []string{"Header"}, body, []string{"Save", "Cancel"}, 35, 0, true)
	_, savedOffset := renderConfigScreen(80, 12, []string{"Header"}, body, []string{"Save", "Cancel"}, 0, offset, false)
	if savedOffset != offset {
		t.Fatal("focusing Save moved the body")
	}
	_, resized := renderConfigScreen(80, 60, []string{"Header"}, body, []string{"Save", "Cancel"}, 0, offset, false)
	if resized != 0 {
		t.Fatal("resize did not clamp the viewport")
	}
}
