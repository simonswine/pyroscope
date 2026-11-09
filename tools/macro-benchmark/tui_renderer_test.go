package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestDashboardRenderer(t *testing.T) {
	var r dashboardRenderer
	var out bytes.Buffer
	paint := func(frame string, width int) string {
		t.Helper()
		out.Reset()
		if err := r.render(&out, frame, width, 24); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	if got := paint("first\r\nsecond\r\n", 80); !strings.Contains(got, "\x1b[2J") {
		t.Fatal("initial paint must clear")
	}
	if got := paint("first\r\nsecond\r\n", 80); got != "" {
		t.Fatalf("identical frame painted: %q", got)
	}
	got := paint("first\r\ns\r\n", 80)
	if strings.Contains(got, "first") || strings.Contains(got, "\x1b[2J") || !strings.Contains(got, "\x1b[2;1H\x1b[0ms\x1b[0m\x1b[K") {
		t.Fatalf("incorrect incremental paint: %q", got)
	}
	if got := paint("first\r\n", 80); !strings.Contains(got, "\x1b[2;1H\x1b[0m\x1b[0m\x1b[K") {
		t.Fatalf("removed row not erased: %q", got)
	}
	if got := paint("first\r\n", 40); !strings.Contains(got, "\x1b[2J") {
		t.Fatal("resize must repaint")
	}
	r.invalidate()
	if got := paint("first\r\n", 40); !strings.Contains(got, "\x1b[2J") {
		t.Fatal("screen transition must repaint")
	}
}
