package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"time"
)

type ingestReadyManifest struct {
	Version      int                     `yaml:"version"`
	OwnerID      string                  `yaml:"owner_id"`
	PlanSHA      string                  `yaml:"plan_sha256"`
	WindowsSHA   string                  `yaml:"windows_sha256"`
	IngestCommit string                  `yaml:"ingest_commit"`
	Windows      map[string]replayWindow `yaml:"windows"`
}

func validateReadyManifest(owner RunPaths, plan runPlan) (map[string]replayWindow, error) {
	path := filepath.Join(owner.Root, "ingest-ready.yaml")
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("storage owner is not rerunnable (missing readiness record): %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("ingest readiness record is not a regular file")
	}
	var ready ingestReadyManifest
	if err := readYAML(path, &ready); err != nil {
		return nil, err
	}
	planDigest, err := checksum(filepath.Join(owner.Bundle, "plan.yaml"))
	if err != nil {
		return nil, err
	}
	windowDigest, err := checksum(filepath.Join(owner.Results, "windows.yaml"))
	if err != nil {
		return nil, err
	}
	if ready.Version != 1 || ready.OwnerID != owner.ID || ready.PlanSHA != planDigest || ready.WindowsSHA != windowDigest || ready.IngestCommit != plan.Ingest.Commit {
		return nil, errors.New("storage readiness record does not match owner plan or windows")
	}
	if len(ready.Windows) != len(plan.Datasets) {
		return nil, errors.New("storage readiness dataset set does not match plan")
	}
	for _, dataset := range plan.Datasets {
		window, ok := ready.Windows[dataset.Name]
		if !ok || window.Tenant != dataset.Tenant || window.Pushed <= 0 || window.End <= window.Start {
			return nil, fmt.Errorf("invalid readiness window for dataset %q", dataset.Name)
		}
	}
	return ready.Windows, nil
}

type rerunBinding struct {
	Version      int    `yaml:"version"`
	StorageRunID string `yaml:"storage_run_id"`
	PlanSHA      string `yaml:"plan_sha256"`
	WindowsSHA   string `yaml:"windows_sha256"`
}

// Called only under the node execution lock. Never prepares storage, creates a
// bucket, or invokes the replay/initial-ingest code path.
func executeRerun(ctx context.Context, paths RunPaths) (runErr error) {
	if !paths.Rerun || !validSessionID(paths.OwnerID) {
		return errors.New("invalid rerun storage binding")
	}
	owner, err := newRunPaths(filepath.Dir(filepath.Dir(paths.Root)), paths.OwnerID)
	if err != nil {
		return err
	}
	for _, dir := range []string{owner.Root, owner.Bundle, owner.Results, owner.Minio, owner.Ingest} {
		info, err := os.Lstat(dir)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsafe owner directory %s", dir)
		}
	}
	status, err := loadWorkerState(owner.Worker)
	if err != nil {
		return err
	}
	if status.Phase != "completed" && status.Phase != "failed" {
		return fmt.Errorf("storage owner worker is %s", status.Phase)
	}
	if err := verifyBundle(owner.Bundle); err != nil {
		return err
	}
	if err := verifyBundle(paths.Bundle); err != nil {
		return err
	}
	var hashes map[string]string
	if err := readYAML(filepath.Join(paths.Bundle, "checksums.yaml"), &hashes); err != nil {
		return err
	}
	for _, name := range []string{"rerun.yaml", "settings.yaml", "plan.yaml", "minio", "cluster-ingest"} {
		if hashes[name] == "" {
			return fmt.Errorf("rerun bundle does not checksum %s", name)
		}
	}
	var binding rerunBinding
	if err := readYAML(filepath.Join(paths.Bundle, "rerun.yaml"), &binding); err != nil {
		return err
	}
	planSHA, err := checksum(filepath.Join(owner.Bundle, "plan.yaml"))
	if err != nil {
		return err
	}
	windowsSHA, err := checksum(filepath.Join(owner.Results, "windows.yaml"))
	if err != nil {
		return err
	}
	if binding.Version != 1 || binding.StorageRunID != owner.ID || binding.PlanSHA != planSHA || binding.WindowsSHA != windowsSHA {
		return errors.New("rerun storage binding mismatch")
	}
	var ownerPlan, childPlan runPlan
	if err := readYAML(filepath.Join(owner.Bundle, "plan.yaml"), &ownerPlan); err != nil {
		return err
	}
	if err := readYAML(filepath.Join(paths.Bundle, "plan.yaml"), &childPlan); err != nil {
		return err
	}
	windows, err := validateReadyManifest(owner, ownerPlan)
	if err != nil {
		return err
	}
	var persisted map[string]replayWindow
	if err := readYAML(filepath.Join(owner.Results, "windows.yaml"), &persisted); err != nil {
		return err
	}
	if !reflect.DeepEqual(windows, persisted) {
		return errors.New("readiness windows differ from persisted windows")
	}
	if ownerPlan.Ingest != childPlan.Ingest || ownerPlan.Baseline != childPlan.Baseline || ownerPlan.Comparison != childPlan.Comparison {
		return errors.New("rerun plan changes owner product revisions")
	}
	original := map[string]datasetReplay{}
	for _, d := range ownerPlan.Datasets {
		original[d.Name] = d
	}
	for _, d := range childPlan.Datasets {
		if original[d.Name] != d {
			return errors.New("rerun plan changes owner datasets or tenants")
		}
	}
	var inputs inputsConfig
	if err := readYAML(filepath.Join(paths.Bundle, "settings.yaml"), &inputs); err != nil {
		return err
	}
	if err := inputs.validate(); err != nil {
		return err
	}
	if err := childPlan.validate(inputs); err != nil {
		return err
	}
	if _, err := benchmarkExecutions(childPlan, windows); err != nil {
		return err
	}
	for _, name := range []string{"minio", "cluster-ingest"} {
		for _, root := range []string{owner.Bundle, paths.Bundle} {
			info, err := os.Lstat(filepath.Join(root, name))
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("unsafe %s binary in %s", name, root)
			}
		}
		original, err := checksum(filepath.Join(owner.Bundle, name))
		if err != nil {
			return err
		}
		copied, err := checksum(filepath.Join(paths.Bundle, name))
		if err != nil {
			return err
		}
		if copied != original {
			return fmt.Errorf("rerun %s differs from owner binary", name)
		}
	}
	credentials, err := readStorageCredentials(owner)
	if err != nil {
		return fmt.Errorf("owner storage credentials unavailable: %w", err)
	}
	if err := os.MkdirAll(paths.Results, 0700); err != nil {
		return err
	}
	if err := os.MkdirAll(paths.Cluster, 0700); err != nil {
		return err
	}
	if err := writeYAML(filepath.Join(paths.Results, "windows.yaml"), windows); err != nil {
		return err
	}
	var minio *managedProcess
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		collectArtifacts(cleanupCtx, paths)
		if minio != nil {
			stopCtx, stop := context.WithTimeout(cleanupCtx, 40*time.Second)
			runErr = errors.Join(runErr, minio.stop(stopCtx))
			stop()
		}
		result := map[string]any{"success": runErr == nil, "finished_at": time.Now().UTC()}
		if runErr != nil {
			result["error"] = runErr.Error()
		}
		runErr = errors.Join(runErr, writeYAML(filepath.Join(paths.Results, "status.yaml"), result))
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
	const minioURL = "http://127.0.0.1:9000"
	const bucket = "macro-benchmark"
	log.Print("reopening owner MinIO storage (no bucket creation or replay)")
	minio, err = startProcess("taskset", []string{"-c", "2-3", filepath.Join(owner.Bundle, "minio"), "server", "--address", "127.0.0.1:9000", "--console-address", "127.0.0.1:9001", "--quiet", owner.Minio}, []string{"MINIO_ROOT_USER=" + credentials.AccessKey, "MINIO_ROOT_PASSWORD=" + credentials.SecretKey, "MINIO_PROMETHEUS_AUTH_TYPE=public", "GOMEMLIMIT=8GiB"}, filepath.Join(paths.Results, "minio.log"))
	if err != nil {
		return err
	}
	readyCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	err = waitMinio(readyCtx, minio, minioURL)
	cancel()
	if err != nil {
		return err
	}
	if err := checkBucket(ctx, minioURL, bucket, credentials.AccessKey, credentials.SecretKey); err != nil {
		return err
	}
	idle := endpointManifest{MinioURL: minioURL, Processes: map[string]int{"minio": minio.cmd.Process.Pid}}
	if err := publishEndpoints(paths, idle); err != nil {
		return err
	}
	clusterEnv := []string{"MINIO_ENDPOINT=127.0.0.1:9000", "MINIO_BUCKET=" + bucket, "MINIO_ROOT_USER=" + credentials.AccessKey, "MINIO_ROOT_PASSWORD=" + credentials.SecretKey, "TMPDIR=" + paths.Cluster}
	return runComparisons(ctx, paths, inputs, childPlan, windows, idle, clusterEnv)
}

func publishIngestReady(paths RunPaths, plan runPlan, windows map[string]replayWindow) error {
	if len(windows) != len(plan.Datasets) {
		return errors.New("cannot publish readiness for incomplete replay")
	}
	for _, dataset := range plan.Datasets {
		window, ok := windows[dataset.Name]
		if !ok || window.Tenant != dataset.Tenant || window.Pushed <= 0 || window.End <= window.Start {
			return fmt.Errorf("cannot publish readiness for incomplete replay %q", dataset.Name)
		}
	}
	digest, err := checksum(filepath.Join(paths.Bundle, "plan.yaml"))
	if err != nil {
		return err
	}
	windowPath := filepath.Join(paths.Results, "windows.yaml")
	persisted := map[string]replayWindow{}
	if err := readYAML(windowPath, &persisted); err != nil {
		return err
	}
	if !reflect.DeepEqual(persisted, windows) {
		return errors.New("persisted windows differ from completed replays")
	}
	file, err := os.Open(windowPath)
	if err != nil {
		return err
	}
	if err := errors.Join(file.Sync(), file.Close()); err != nil {
		return err
	}
	resultsDir, err := os.Open(paths.Results)
	if err != nil {
		return err
	}
	if err := errors.Join(resultsDir.Sync(), resultsDir.Close()); err != nil {
		return err
	}
	windowsSHA, err := checksum(windowPath)
	if err != nil {
		return err
	}
	return atomicYAML(filepath.Join(paths.Root, "ingest-ready.yaml"), ingestReadyManifest{Version: 1, OwnerID: paths.ID, PlanSHA: digest, WindowsSHA: windowsSHA, IngestCommit: plan.Ingest.Commit, Windows: windows})
}
