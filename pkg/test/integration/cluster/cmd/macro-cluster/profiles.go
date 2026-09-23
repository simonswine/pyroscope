package main

import (
	"errors"
	"os"
	"runtime"
	"runtime/pprof"
)

// Profiling is process-wide: the frontend and all backends share one runtime.
// Create outputs before publishing readiness so an unwritable path fails fast.
func startProfiles(cpuPath, heapPath string) (func() error, error) {
	var cpu, heap *os.File
	var err error
	if heapPath != "" {
		heap, err = os.OpenFile(heapPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
	}
	if cpuPath != "" {
		cpu, err = os.OpenFile(cpuPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			err = pprof.StartCPUProfile(cpu)
		}
		if err != nil {
			if cpu != nil {
				err = errors.Join(err, cpu.Close())
			}
			if heap != nil {
				err = errors.Join(err, heap.Close())
			}
			return nil, err
		}
	}
	return func() error {
		var err error
		if cpu != nil {
			pprof.StopCPUProfile()
			err = cpu.Close()
		}
		if heap != nil {
			// Capture live memory before stopping components, outside measurement.
			runtime.GC()
			err = errors.Join(err, pprof.WriteHeapProfile(heap), heap.Close())
		}
		return err
	}, nil
}
