package compactionworker

import (
	"bytes"
	"flag"
	"runtime"
	"testing"

	"github.com/go-kit/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func TestCalibrationConfig(t *testing.T) {
	var cfg Config
	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg.RegisterFlags(flags)
	require.False(t, cfg.Calibration)
	require.NoError(t, flags.Parse([]string{"-compaction-worker.calibration", "-compaction-worker.job-concurrency=8"}))
	cfg.TempDir = t.TempDir()
	w, err := New(log.NewNopLogger(), cfg, nil, nil, prometheus.NewRegistry(), nil, nil)
	require.NoError(t, err)
	require.Equal(t, 1, w.threads)
	require.Equal(t, int32(1), w.capacity.Load())
}

func TestMemoryCalibration(t *testing.T) {
	var output bytes.Buffer
	finish := startMemoryCalibration(log.NewLogfmtLogger(&output))
	allocation := make([]byte, 1<<20)
	finish()
	runtime.KeepAlive(allocation)
	require.Contains(t, output.String(), `msg="compaction memory calibration"`)
	for _, field := range []string{"baseline_heap=", "sampled_peak_heap=", "peak_heap_increase=", "allocated=", "gc_cycles=", "post_gc_heap=", "retained_heap_delta_bytes="} {
		require.Contains(t, output.String(), field)
	}
}
