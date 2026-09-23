package main

import (
	"encoding/base64"
	"hash/crc32"
	"os"
	"path/filepath"
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

func TestStoreFixture(t *testing.T) {
	const data = "streamed compressed fixture bytes"
	hash := crc32.New(crc32.MakeTable(crc32.Castagnoli))
	if _, err := hash.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
	cfg := inputsConfig{FixtureSizeBytes: int64(len(data)), FixtureCRC32C: base64.StdEncoding.EncodeToString(hash.Sum(nil))}
	for _, tt := range []struct {
		name, body string
		wantErr    bool
	}{{"valid", data, false}, {"truncated", data[:8], true}, {"extra", data + "extra", true}, {"corrupt", strings.Repeat("x", len(data)), true}} {
		t.Run(tt.name, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "fixture.replay")
			meta, err := storeFixture(strings.NewReader(tt.body), cfg, dest)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error=%v", err)
			}
			if tt.wantErr {
				if _, err := os.Stat(dest); !os.IsNotExist(err) {
					t.Fatal("published corrupt fixture")
				}
				return
			}
			if len(meta["sha256"].(string)) != 64 {
				t.Fatal("missing recorded SHA256")
			}
		})
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
