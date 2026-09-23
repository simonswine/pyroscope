package main

import (
	"strings"
	"testing"
)

func TestTUIDisplay(t *testing.T) {
	for _, size := range []struct{ width, height int }{{100, 30}, {40, 24}, {10, 5}} {
		display := tuiDisplay(size.width, size.height, "/state", nil, 0, "Ready", false)
		if strings.Count(display, "\r\n") > size.height-1 {
			t.Fatal("display exceeds terminal height")
		}
	}
	display := tuiDisplay(100, 30, "/state", nil, 0, "Ready\x1b[2J", false)
	if !strings.Contains(display, "h SSH") || !strings.Contains(display, "No sessions yet") {
		t.Fatal("missing SSH control or empty state")
	}
	if strings.Count(display, "\x1b[2J") != 1 {
		t.Fatal("message injected terminal escape")
	}
	if !strings.Contains(tuiDisplay(100, 30, "/state", nil, 0, "", true), "WORKING") {
		t.Fatal("missing busy indicator")
	}
}

func TestTUIDisplaySelection(t *testing.T) {
	states := make([]sessionState, 40)
	for i := range states {
		states[i].Phase = "prepared"
	}
	states[39].Config.RunID = "selected-session"
	display := tuiDisplay(100, 30, "/state", states, 39, "Ready", false)
	if !strings.Contains(display, "selected-session") || !strings.Contains(display, "\x1b[1;30;46m›") {
		t.Fatal("selected session not visible or highlighted")
	}
	if !strings.Contains(display, "EC2 keeps billing") {
		t.Fatal("billing warning not visible")
	}
}
