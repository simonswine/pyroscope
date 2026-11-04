package compactionworker

import (
	"runtime"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
)

// startMemoryCalibration measures process-wide memory around a single compaction.
// Other process activity and pooled allocations still affect the measurements.
// The returned function must be called exactly once, including on failure.
func startMemoryCalibration(logger log.Logger) func() {
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	peak := before.HeapAlloc
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				var sample runtime.MemStats
				runtime.ReadMemStats(&sample)
				peak = max(peak, sample.HeapAlloc)
			case <-stop:
				return
			}
		}
	}()
	return func() {
		close(stop)
		<-done
		var end, after runtime.MemStats
		runtime.ReadMemStats(&end)
		peak = max(peak, end.HeapAlloc)
		runtime.GC()
		runtime.ReadMemStats(&after)
		level.Info(logger).Log(
			"msg", "compaction memory calibration",
			"scope", "process",
			"baseline_heap", humanize.IBytes(before.HeapAlloc),
			"sampled_peak_heap", humanize.IBytes(peak),
			"peak_heap_increase", humanize.IBytes(peak-before.HeapAlloc),
			"allocated", humanize.IBytes(end.TotalAlloc-before.TotalAlloc),
			"gc_cycles", end.NumGC-before.NumGC,
			"post_gc_heap", humanize.IBytes(after.HeapAlloc),
			// Signed because GC can reclaim pools retained at the baseline.
			"retained_heap_delta_bytes", int64(after.HeapAlloc)-int64(before.HeapAlloc),
		)
	}
}
