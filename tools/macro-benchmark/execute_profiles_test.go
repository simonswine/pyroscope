package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/pprof/profile"
)

func TestCheckProfiles(t *testing.T) {
	var valid bytes.Buffer
	p := &profile.Profile{
		SampleType: []*profile.ValueType{{Type: "cpu", Unit: "nanoseconds"}},
		Sample:     []*profile.Sample{{Value: []int64{10000000}}},
	}
	if err := p.Write(&valid); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		data    []byte
		missing bool
		wantErr bool
	}{
		{name: "valid", data: valid.Bytes()},
		{name: "empty", data: []byte{}, wantErr: true},
		{name: "truncated", data: valid.Bytes()[:10], wantErr: true},
		{name: "missing", missing: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "replay-cpu.pprof")
			if !tc.missing {
				if err := os.WriteFile(path, tc.data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := checkProfiles(path); (err != nil) != tc.wantErr {
				t.Fatalf("checkProfiles() = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
