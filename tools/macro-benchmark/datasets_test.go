package main

import (
	"strings"
	"testing"
)

func TestDatasetPresets(t *testing.T) {
	for _, name := range []string{"full-tenant", "high-volume-service"} {
		cfg := inputsConfig{Dataset: name}
		if err := cfg.resolveDataset(); err != nil {
			t.Fatal(err)
		}
		cfg.defaults()
		if cfg.FixtureSizeBytes <= 0 || !strings.Contains(cfg.FixtureURL, "?generation=") {
			t.Fatalf("invalid preset: %+v", cfg)
		}
		if cfg.ReplayTimeout != "3h" {
			t.Fatal("two-hour replay needs headroom")
		}
	}
	for _, cfg := range []inputsConfig{{Dataset: "unknown"}, {Dataset: "full-tenant", Fixture: "local"}} {
		if cfg.resolveDataset() == nil {
			t.Fatal("accepted invalid dataset selection")
		}
	}
}

func TestReplaySummary(t *testing.T) {
	for _, tt := range []struct {
		log     string
		wantErr bool
	}{
		{`level=info msg="replay cycle complete" cycle=0 pushed=123 failed=0`, false},
		{`level=info msg="replay cycle complete" cycle=0 pushed=123 failed=4`, true},
		{`level=info msg="replay cycle complete" cycle=0 pushed=0 failed=0`, true},
		{`level=info msg="replay interrupted" cycle=0 pushed=123 failed=0`, true},
		{"", true},
	} {
		_, err := replaySummary(strings.NewReader(tt.log))
		if (err != nil) != tt.wantErr {
			t.Fatalf("summary %q error=%v", tt.log, err)
		}
	}
}
