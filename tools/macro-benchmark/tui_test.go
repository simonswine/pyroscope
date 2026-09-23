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

func TestTUIConfirmationPopup(t *testing.T) {
	confirm := &tuiConfirmation{action: 'd', runID: "test-run", title: "DESTROY INSTANCE", detail: "Instance data will be lost."}
	for _, size := range []struct{ width, height int }{{100, 30}, {30, 14}} {
		display := tuiDisplayWithConfirmation(size.width, size.height, "/state", nil, 0, "Ready", false, confirm)
		for _, want := range []string{"CONFIRM: DESTROY", "test-run", "Press Y to confirm", "\x1b[1;37;44m"} {
			if !strings.Contains(display, want) {
				t.Errorf("%dx%d: missing %q", size.width, size.height, want)
			}
		}
	}
}

func TestTUIArrowKeys(t *testing.T) {
	var keys tuiKeys
	if got := keys.feed([]byte{27, '['}); len(got) != 0 {
		t.Fatalf("incomplete escape produced keys: %q", got)
	}
	if got := string(keys.feed([]byte{'A', 'j', 27, 'O', 'B'})); got != "kjj" {
		t.Fatalf("expected up/down to match k/j, got %q", got)
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
