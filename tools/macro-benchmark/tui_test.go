package main

import (
	"strings"
	"testing"
	"time"
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
		for _, want := range []string{"CONFIRM: DESTROY", "test-run", "Press Y to confirm", "\x1b[48;5;235m"} {
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

func TestTUISpinnerAndPhaseColours(t *testing.T) {
	states := []sessionState{{Phase: "draft"}, {Phase: "running"}, {Phase: "failed"}, {Phase: "destroyed"}}
	for i := range states {
		states[i].Config.RunID = string(rune('a' + i))
	}
	first := tuiDisplayWithConfirmationAndSpinner(100, 35, "/state", states, 1, "", false, nil, "⠋")
	second := tuiDisplayWithConfirmationAndSpinner(100, 35, "/state", states, 1, "", false, nil, "⠙")
	if !strings.Contains(first, "\x1b[38;5;16;48;5;159m ⠋ running ") || !strings.Contains(second, "\x1b[38;5;16;48;5;159m ⠙ running ") || first == second {
		t.Fatal("active session did not animate")
	}
	if strings.Contains(first, "⠋ draft") || strings.Contains(first, "⠋ destroyed") {
		t.Fatal("inactive session is spinning")
	}
	for _, want := range []string{"\x1b[38;5;16;48;5;159m ⠋ running ", "\x1b[38;5;16;48;5;210m failed ", "\x1b[38;5;16;48;5;252m draft "} {
		if !strings.Contains(first, want) {
			t.Errorf("missing phase colour %q", want)
		}
	}
}

func TestPhaseBadgeContrast(t *testing.T) {
	for phase, style := range map[string]string{
		"completed": "\x1b[38;5;16;48;5;157m", // black on light green
		"uploading": "\x1b[38;5;16;48;5;183m", // black on light purple
		"starting":  "\x1b[38;5;16;48;5;229m", // black on light yellow
	} {
		if got := phaseBadgeStyle(phase); got != style {
			t.Errorf("%s: %q, want %q", phase, got, style)
		}
	}
}

func TestTUISelectedSessionTable(t *testing.T) {
	state := sessionState{Phase: "running", InstanceID: "i-example", ExpiresAt: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC), Error: "bad\x1b[2Jerror"}
	state.Config.RunID = "selected-run"
	state.Config.Region = "us-east-1"
	state.Config.Inputs.BaselineRef = "baseline"
	state.Config.Inputs.ComparisonRef = "comparison"
	state.Config.Inputs.Count = 3
	state.Config.Inputs.Benchtime = "5x"
	for _, size := range []struct{ width, height int }{{100, 32}, {38, 25}} {
		view := tuiDisplay(size.width, size.height, "/state", []sessionState{state}, 0, "Ready", false)
		for _, text := range []string{"FIELD", "VALUE", "Region", "Instance", "Refs", "Ingest", "Measurement", "Results", "Deadline", "Error"} {
			if !strings.Contains(view, text) {
				t.Errorf("%dx%d: missing %q", size.width, size.height, text)
			}
		}
		if strings.Count(view, "\x1b[2J") != 1 {
			t.Fatal("untrusted error injected escape")
		}
		if strings.Count(view, "\r\n") > size.height-1 {
			t.Fatal("view exceeds terminal height")
		}
	}
}

func TestTUIDisplaySelection(t *testing.T) {
	states := make([]sessionState, 40)
	for i := range states {
		states[i].Phase = "prepared"
	}
	states[39].Config.RunID = "selected-session"
	display := tuiDisplay(100, 30, "/state", states, 39, "Ready", false)
	if !strings.Contains(display, "selected-session") || !strings.Contains(display, "›") {
		t.Fatalf("selected session not visible or highlighted: %q", display)
	}
	if !strings.Contains(display, "EC2 keeps billing") {
		t.Fatal("billing warning not visible")
	}
}
