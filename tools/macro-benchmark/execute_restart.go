package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/pprof/profile"
)

// The write process includes all three metastores. Reuse its disk state and
// addresses, not its in-memory caches. MinIO remains running and replay is never
// repeated. The ingest ref continues to own the storage/metadata format.
func runBenchmarkGroup(ctx context.Context, paths RunPaths, inputs inputsConfig, execution benchmarkExecution, idle endpointManifest, clusterEnv []string) (runErr error) {
	root := filepath.Join(paths.Results, execution.resultDir())
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	manifest := filepath.Join(root, "ingest-endpoints.yaml")
	if _, err := os.Stat(manifest); err == nil {
		return fmt.Errorf("refusing to reuse benchmark results: %s", root)
	} else if !os.IsNotExist(err) {
		return err
	}
	cpu, heap := filepath.Join(root, "metastore-cpu.pprof"), filepath.Join(root, "metastore-heap.pprof")
	p, err := startProcess("taskset", []string{"-c", "4-6", filepath.Join(paths.Bundle, "cluster-ingest"), "-mode=ingest", "-data-dir=" + paths.Ingest, "-endpoints=" + manifest, "-cpu-profile=" + cpu, "-mem-profile=" + heap}, append(append([]string{}, clusterEnv...), "GOMEMLIMIT=12GiB"), filepath.Join(root, "ingest.log"))
	if err != nil {
		return err
	}
	defer func() {
		runErr = errors.Join(runErr, publishEndpoints(paths, idle))
		stopCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		runErr = errors.Join(runErr, p.stop(stopCtx), p.err, checkProfiles(cpu, heap))
	}()
	var ingest endpointManifest
	readyCtx, cancel := context.WithTimeout(ctx, 11*time.Minute)
	err = waitCluster(readyCtx, p, manifest, "ingest", &ingest)
	cancel()
	if err != nil {
		return err
	}
	ingest.MinioURL = idle.MinioURL
	ingest.Processes = map[string]int{"ingest": p.cmd.Process.Pid}
	for name, pid := range idle.Processes {
		ingest.Processes[name] = pid
	}
	if err := publishEndpoints(paths, ingest); err != nil {
		return err
	}
	// Recover persisted Raft state and let any outstanding compaction finish
	// before starting the measured query process.
	if err := observePaths(ctx, []string{"-settle", "-output", filepath.Join(paths.Results, "metrics")}, paths); err != nil {
		return err
	}
	return runBenchmarkCluster(ctx, paths, inputs, execution, ingest, clusterEnv)
}

func checkProfiles(paths ...string) error {
	var result error
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			result = errors.Join(result, err)
		} else if info.Size() == 0 {
			result = errors.Join(result, fmt.Errorf("empty profile: %s", path))
		} else {
			data, err := os.ReadFile(path)
			if err == nil {
				_, err = profile.ParseData(data)
			}
			if err != nil {
				result = errors.Join(result, fmt.Errorf("invalid profile %s: %w", path, err))
			}
		}
	}
	return result
}
