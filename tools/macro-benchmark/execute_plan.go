package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type replayWindow struct {
	Tenant string `yaml:"tenant"`
	Start  int64  `yaml:"start_ms"`
	End    int64  `yaml:"end_ms"`
	Pushed int64  `yaml:"pushed"`
}

func replayDatasets(ctx context.Context, inputs inputsConfig, plan runPlan, endpoint string) (map[string]replayWindow, error) {
	timeout, err := time.ParseDuration(inputs.ReplayTimeout)
	if err != nil {
		return nil, err
	}
	windows := map[string]replayWindow{}
	for _, dataset := range plan.Datasets {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		input := dataset.URL
		if dataset.Path != "" {
			input = filepath.Join(remoteBundle, dataset.Path)
		}
		args := replayPushArgs(input, endpoint, dataset.Tenant)
		if dataset.SHA256 != "" {
			args = append(args, "--sha256="+dataset.SHA256)
		}
		name := "replay-" + dataset.Name + ".log"
		window := replayWindow{Tenant: dataset.Tenant, Start: time.Now().UnixMilli()}
		log.Printf("replaying %s into tenant %s", dataset.Name, dataset.Tenant)
		if err := runWorkload(ctx, timeout, name, nil, filepath.Join(remoteBundle, "profilecli"), args...); err != nil {
			return nil, err
		}
		window.End = time.Now().UnixMilli() + 1000
		file, err := os.Open(filepath.Join(remoteResults, name))
		if err != nil {
			return nil, err
		}
		window.Pushed, err = replaySummary(file)
		if err := errors.Join(err, file.Close()); err != nil {
			return nil, err
		}
		windows[dataset.Name] = window
		if err := writeYAML(filepath.Join(remoteResults, "windows.yaml"), windows); err != nil {
			return nil, err
		}
	}
	return windows, nil
}

type benchmarkExecution struct {
	Role      string
	Benchmark string
	Window    replayWindow
}

func benchmarkExecutions(plan runPlan, windows map[string]replayWindow) ([]benchmarkExecution, error) {
	var executions []benchmarkExecution
	for _, b := range plan.Benchmarks {
		window, ok := windows[b.Dataset]
		if !ok || window.Tenant == "" || window.End <= window.Start || window.Pushed <= 0 {
			return nil, fmt.Errorf("no successful replay window for %s", b.Dataset)
		}
		for _, role := range []string{"baseline", "comparison"} {
			executions = append(executions, benchmarkExecution{role, b.Name, window})
		}
	}
	return executions, nil
}

func (e benchmarkExecution) resultDir() string {
	return filepath.Join(e.Role, e.Benchmark)
}

func benchmarkArgs(inputs inputsConfig) []string {
	return []string{"-test.run=^$", "-test.bench=.", "-test.benchtime=" + inputs.Benchtime, "-test.count=" + strconv.Itoa(inputs.Count), "-test.timeout=2h"}
}

func (e benchmarkExecution) queryEnv(inputs inputsConfig, endpoint string) []string {
	return []string{"PYROSCOPE_URL=" + endpoint, "TENANT_ID=" + e.Window.Tenant, "PROFILE_TYPE=" + inputs.ProfileType, "LABEL_SELECTOR=" + inputs.Selector, "START_MS=" + strconv.FormatInt(e.Window.Start, 10), "END_MS=" + strconv.FormatInt(e.Window.End, 10)}
}

func runComparisons(ctx context.Context, inputs inputsConfig, plan runPlan, windows map[string]replayWindow, ingest endpointManifest, clusterEnv []string) error {
	executions, err := benchmarkExecutions(plan, windows)
	if err != nil {
		return err
	}
	for _, execution := range executions {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := runBenchmarkGroup(ctx, inputs, execution, ingest, clusterEnv); err != nil {
			return err
		}
	}
	return nil
}

// Each benchmark/version owns one query process for all repetitions. Stop waits
// for profiles to be finalized before the surrounding write cluster is stopped.
func runBenchmarkCluster(ctx context.Context, inputs inputsConfig, execution benchmarkExecution, ingest endpointManifest, clusterEnv []string) (runErr error) {
	relative := execution.resultDir()
	root := filepath.Join(remoteResults, relative)
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	manifest := filepath.Join(root, "endpoints.yaml")
	if _, err := os.Stat(manifest); err == nil {
		return fmt.Errorf("refusing to reuse benchmark results: %s", root)
	} else if !os.IsNotExist(err) {
		return err
	}
	cpu, heap := filepath.Join(root, "cpu.pprof"), filepath.Join(root, "heap.pprof")
	log.Printf("starting fresh query cluster: %s", relative)
	p, err := startProcess("taskset", []string{"-c", "7-14", filepath.Join(remoteBundle, "cluster-"+execution.Role), "-mode=query", "-metastore-address=" + ingest.MetastoreAddress, "-endpoints=" + manifest, "-cpu-profile=" + cpu, "-mem-profile=" + heap}, append(append([]string{}, clusterEnv...), "GOMEMLIMIT=33GiB"), filepath.Join(root, "cluster.log"))
	if err != nil {
		return err
	}
	defer func() {
		// Exclude the query process from periodic snapshots before shutting down.
		runErr = errors.Join(runErr, publishEndpoints(ingest))
		stopCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		runErr = errors.Join(runErr, p.stop(stopCtx), p.err)
		runErr = errors.Join(runErr, checkProfiles(cpu, heap))
	}()
	var query endpointManifest
	readyCtx, cancel := context.WithTimeout(ctx, 11*time.Minute)
	err = waitCluster(readyCtx, p, manifest, "query", &query)
	cancel()
	if err != nil {
		return err
	}
	combined := ingest
	combined.Components = append(append([]componentEndpoint{}, ingest.Components...), query.Components...)
	combined.QueryURL = query.QueryURL
	combined.Processes = map[string]int{}
	for name, pid := range ingest.Processes {
		combined.Processes[name] = pid
	}
	combined.Processes["query"] = p.cmd.Process.Pid
	if err := publishEndpoints(combined); err != nil {
		return err
	}
	if err := runWorkload(ctx, 2*time.Hour, filepath.Join(relative, "bench.txt"), execution.queryEnv(inputs, query.QueryURL), filepath.Join(remoteBundle, execution.Benchmark+".test"), benchmarkArgs(inputs)...); err != nil {
		return err
	}
	_, err = snapshot(ctx, filepath.Join(root, "metrics"))
	return err
}

func publishEndpoints(endpoints endpointManifest) error {
	path := filepath.Join(remoteBundle, "endpoints.yaml")
	if err := writeYAML(path+".tmp", endpoints); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}
