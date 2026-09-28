package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func executeWithPaths(ctx context.Context, paths RunPaths) (runErr error) {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return errors.New("execute requires root on the disposable Linux EC2 host")
	}
	if err := verifyBundle(paths.Bundle); err != nil {
		return err
	}
	var inputs inputsConfig
	if err := readYAML(filepath.Join(paths.Bundle, "settings.yaml"), &inputs); err != nil {
		return err
	}
	if err := inputs.validate(); err != nil {
		return err
	}
	var plan runPlan
	if err := readYAML(filepath.Join(paths.Bundle, "plan.yaml"), &plan); err != nil {
		return err
	}
	if err := plan.validate(inputs); err != nil {
		return err
	}
	if err := os.MkdirAll(paths.Results, 0755); err != nil {
		return err
	}
	runLog, err := os.OpenFile(filepath.Join(paths.Results, "run.log"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer runLog.Close()
	startedAt := time.Now().UTC()
	previous := log.Writer()
	log.SetOutput(io.MultiWriter(os.Stdout, runLog))
	defer log.SetOutput(previous)
	var minio, cluster *managedProcess
	var observerCancel context.CancelFunc
	var observerDone chan struct{}
	defer func() {
		if observerCancel != nil {
			observerCancel()
			<-observerDone
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		collectArtifacts(cleanupCtx, paths)
		// Stop the cluster before its object store; keep all logs until processes exit.
		for _, p := range []*managedProcess{cluster, minio} {
			if p == nil {
				continue
			}
			stopCtx, stop := context.WithTimeout(cleanupCtx, 40*time.Second)
			runErr = errors.Join(runErr, p.stop(stopCtx))
			stop()
		}
		status := map[string]any{"success": runErr == nil, "started_at": startedAt, "finished_at": time.Now().UTC()}
		if runErr != nil {
			status["error"] = runErr.Error()
		}
		runErr = errors.Join(runErr, writeYAML(filepath.Join(paths.Results, "status.yaml"), status), runLog.Sync())
		if err := archiveDirectoryAtomic(paths.Results, paths.Archive); err != nil {
			runErr = errors.Join(runErr, err)
		} else {
			digest, err := hashFile(paths.Archive)
			if err != nil {
				runErr = errors.Join(runErr, err)
			} else {
				info, err := os.Stat(paths.Archive)
				if err != nil {
					runErr = errors.Join(runErr, err)
				} else {
					runErr = errors.Join(runErr, atomicJSON(filepath.Join(paths.Root, "results-manifest.json"), artifactInfo{Name: "results.tar.gz", Size: info.Size(), Digest: digest}))
				}
			}
		}
	}()
	cpus, err := exec.CommandContext(ctx, "nproc").Output()
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(cpus)) != "16" {
		return fmt.Errorf("expected 16 CPUs, got %s", cpus)
	}
	topology, err := exec.CommandContext(ctx, "lscpu", "-p=CORE,SOCKET").Output()
	if err != nil {
		return err
	}
	cores := map[string]bool{}
	for _, line := range strings.Split(string(topology), "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			cores[line] = true
		}
	}
	if len(cores) != 16 {
		return fmt.Errorf("expected 16 cores, got %d", len(cores))
	}
	if err := command(ctx, "", nil, "taskset", "-apc", "0-1", strconv.Itoa(os.Getpid())); err != nil {
		return err
	}
	log.Print("checking profilecli replay arguments")
	if err := preflightReplayCLI(ctx, filepath.Join(paths.Bundle, "profilecli")); err != nil {
		return err
	}
	for _, dir := range []string{paths.Minio, paths.Cluster, paths.Ingest} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
	}
	credentials, err := createStorageCredentials(paths)
	if err != nil {
		return err
	}
	access, secret := credentials.AccessKey, credentials.SecretKey
	const minioURL = "http://127.0.0.1:9000"
	const bucket = "macro-benchmark"
	log.Print("starting pgsty MinIO on CPUs 2-3")
	minio, err = startProcess("taskset", []string{"-c", "2-3", filepath.Join(paths.Bundle, "minio"), "server", "--address", "127.0.0.1:9000", "--console-address", "127.0.0.1:9001", "--quiet", paths.Minio},
		[]string{"MINIO_ROOT_USER=" + access, "MINIO_ROOT_PASSWORD=" + secret, "MINIO_PROMETHEUS_AUTH_TYPE=public", "GOMEMLIMIT=8GiB"}, filepath.Join(paths.Results, "minio.log"))
	if err != nil {
		return err
	}
	readyCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	err = waitMinio(readyCtx, minio, minioURL)
	cancel()
	if err != nil {
		return err
	}
	if err := createBucket(ctx, minioURL, bucket, access, secret); err != nil {
		return err
	}
	clusterEnv := []string{"MINIO_ENDPOINT=127.0.0.1:9000", "MINIO_BUCKET=" + bucket, "MINIO_ROOT_USER=" + access, "MINIO_ROOT_PASSWORD=" + secret, "TMPDIR=" + paths.Cluster}
	log.Print("starting ingest cluster on CPUs 4-6")
	cluster, err = startProcess("taskset", []string{"-c", "4-6", filepath.Join(paths.Bundle, "cluster-ingest"), "-mode=ingest", "-data-dir=" + paths.Ingest, "-endpoints", filepath.Join(paths.Bundle, "endpoints.yaml")},
		append(append([]string{}, clusterEnv...), "GOMEMLIMIT=12GiB"), filepath.Join(paths.Results, "cluster-ingest.log"))
	if err != nil {
		return err
	}
	var endpoints endpointManifest
	readyCtx, cancel = context.WithTimeout(ctx, 11*time.Minute)
	err = waitCluster(readyCtx, cluster, filepath.Join(paths.Bundle, "endpoints.yaml"), "ingest", &endpoints)
	cancel()
	if err != nil {
		return err
	}
	endpoints.MinioURL = minioURL
	endpoints.Processes = map[string]int{"minio": minio.cmd.Process.Pid, "cluster": cluster.cmd.Process.Pid}
	if err := publishEndpoints(paths, endpoints); err != nil {
		return err
	}
	observerCtx, cancel := context.WithCancel(ctx)
	observerCancel = cancel
	observerDone = make(chan struct{})
	go func() {
		defer close(observerDone)
		if err := observePaths(observerCtx, []string{"-timeout", "24h", "-output", filepath.Join(paths.Results, "metrics")}, paths); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("observer: %v", err)
		}
	}()
	windows, err := replayDatasets(ctx, paths, inputs, plan, endpoints.WriteURL)
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Duration(inputs.SettleSeconds) * time.Second):
	}
	if err := observePaths(ctx, []string{"-settle", "-output", filepath.Join(paths.Results, "metrics")}, paths); err != nil {
		return err
	}
	// Preserve WAL/snapshots and port assignments, but discard the ingest runtime.
	// Each benchmark/version will start fresh write and query processes.
	stopCtx, stop := context.WithTimeout(context.Background(), 90*time.Second)
	err = errors.Join(cluster.stop(stopCtx), cluster.err)
	stop()
	cluster = nil
	if err != nil {
		return err
	}
	idle := endpointManifest{MinioURL: minioURL, Processes: map[string]int{"minio": minio.cmd.Process.Pid}}
	if err := publishEndpoints(paths, idle); err != nil {
		return err
	}
	if err := publishIngestReady(paths, plan, windows); err != nil {
		return err
	}
	return runComparisons(ctx, paths, inputs, plan, windows, idle, clusterEnv)
}

func waitCluster(ctx context.Context, p *managedProcess, path, mode string, out *endpointManifest) error {
	for {
		select {
		case <-p.done:
			return fmt.Errorf("cluster exited before readiness: %v", p.err)
		default:
		}
		if err := readYAML(path, out); err == nil {
			if out.MetastoreAddress == "" || (mode == "ingest" && out.WriteURL == "") || (mode == "query" && out.QueryURL == "") {
				return errors.New("cluster did not publish required endpoints")
			}
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func runWorkload(ctx context.Context, paths RunPaths, timeout time.Duration, logName string, env []string, binary string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	file, err := os.OpenFile(filepath.Join(paths.Results, logName), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	cmd := exec.CommandContext(ctx, "taskset", append([]string{"-c", "15", binary}, args...)...)
	cmd.Env = append(os.Environ(), append(env, "GOMEMLIMIT=4GiB")...)
	cmd.Stdout, cmd.Stderr = file, file
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed (see %s): %w", filepath.Base(binary), logName, errors.Join(err, ctx.Err()))
	}
	return nil
}

func capture(ctx context.Context, path, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		output = append(output, []byte("\ncollection error: "+err.Error()+"\n")...)
	}
	return errors.Join(err, os.WriteFile(path, output, 0600))
}

func collectArtifacts(ctx context.Context, paths RunPaths) {
	if err := copyHarness(filepath.Join(paths.Bundle, "harness"), filepath.Join(paths.Results, "harness")); err != nil {
		log.Printf("collect harness: %v", err)
	}
	for _, name := range []string{"settings.yaml", "plan.yaml", "checksums.yaml", "commit.txt", "source.patch", "endpoints.yaml"} {
		if err := copyFile(filepath.Join(paths.Bundle, name), filepath.Join(paths.Results, name)); err != nil {
			log.Printf("collect %s: %v", name, err)
		}
	}
	commands := map[string][]string{"cpu.txt": {"lscpu"}, "kernel.txt": {"uname", "-a"}, "disks.txt": {"lsblk", "-o", "NAME,SIZE,TYPE,MOUNTPOINTS"}, "minio-version.txt": {filepath.Join(paths.Bundle, "minio"), "--version"}, "processes.txt": {"ps", "-eo", "pid,ppid,psr,pcpu,pmem,rss,args"}}
	for file, args := range commands {
		if err := capture(ctx, filepath.Join(paths.Results, file), args[0], args[1:]...); err != nil {
			log.Printf("collect %s: %v", file, err)
		}
	}
	if _, err := snapshot(ctx, filepath.Join(paths.Results, "metrics-final"), paths.Bundle); err != nil {
		log.Printf("final metrics: %v", err)
	}
}

// archiveDirectoryAtomic only exposes the archive after the compressor and
// filesystem have finished writing it. Failed packaging leaves diagnostics in
// the results directory without claiming that the archive is ready.
func archiveDirectoryAtomic(root, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(destination), ".archive-*")
	if err != nil {
		return err
	}
	name := file.Name()
	if err := file.Close(); err != nil {
		return err
	}
	defer os.Remove(name)
	if err := archiveDirectory(root, name); err != nil {
		return err
	}
	complete, err := os.Open(name)
	if err != nil {
		return err
	}
	if err := errors.Join(complete.Sync(), complete.Close()); err != nil {
		return err
	}
	if err := os.Rename(name, destination); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(destination))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func archiveDirectory(root, destination string) (err error) {
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	defer func() { err = errors.Join(err, tw.Close(), gz.Close(), file.Close()) }()
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing nonregular artifact %s", path)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relative)
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, in)
		return errors.Join(copyErr, in.Close())
	})
}
