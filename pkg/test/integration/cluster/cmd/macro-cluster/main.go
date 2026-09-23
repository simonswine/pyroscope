// macro-cluster runs the integration V2 cluster on a benchmark host.
// Components share one process/Go runtime, exactly as in integration tests.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/grafana/pyroscope/v2/pkg/test/integration/cluster"
)

func main() {
	output := flag.String("endpoints", "endpoints.yaml", "Ready endpoint manifest")
	dataDir := flag.String("data-dir", "", "Persistent cluster directory; retains data and ports across restarts")
	mode := flag.String("mode", "ingest", "Cluster role: ingest or query")
	metastore := flag.String("metastore-address", "", "Existing metastore for query mode")
	cpuProfile := flag.String("cpu-profile", "", "CPU profile output (from readiness until shutdown)")
	memProfile := flag.String("mem-profile", "", "Heap profile output (before cluster teardown)")
	flag.Parse()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, *output, *mode, *metastore, *cpuProfile, *memProfile, *dataDir); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run(ctx context.Context, output, mode, metastore, cpuProfile, memProfile, dataDir string) (runErr error) {
	for _, name := range []string{"MINIO_ENDPOINT", "MINIO_BUCKET", "MINIO_ROOT_USER", "MINIO_ROOT_PASSWORD"} {
		if os.Getenv(name) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	targets := []string{"distributor", "segment-writer", "segment-writer", "metastore", "metastore", "metastore", "compaction-worker"}
	opts := []cluster.ClusterOption{cluster.WithV2()}
	switch mode {
	case "ingest":
	case "query":
		if metastore == "" {
			return fmt.Errorf("query mode requires metastore-address")
		}
		targets = []string{"query-frontend", "query-backend", "query-backend", "query-backend"}
		opts = append(opts, cluster.WithExternalMetastore(metastore))
	default:
		return fmt.Errorf("unknown mode %q", mode)
	}
	opts = append(opts, cluster.WithTargets(targets...), cluster.WithExtraFlags(
		// Public macro fixtures exceed the normal 4 MiB/s tenant ingestion limit.
		"-distributor.ingestion-rate-limit-mb=1024",
		"-distributor.ingestion-burst-size-mb=1024",
		// Public fixtures include series with more than the default 30 label names.
		"-validation.max-label-names-per-series=128",
		"-storage.backend=s3",
		"-storage.s3.endpoint="+os.Getenv("MINIO_ENDPOINT"),
		"-storage.s3.bucket-name="+os.Getenv("MINIO_BUCKET"),
		"-storage.s3.access-key-id="+os.Getenv("MINIO_ROOT_USER"),
		"-storage.s3.secret-access-key="+os.Getenv("MINIO_ROOT_PASSWORD"),
		"-storage.s3.insecure=true", "-storage.s3.bucket-lookup-type=path-style",
	))
	if dataDir != "" {
		opts = append(opts, cluster.WithDirectory(dataDir))
	}
	c := cluster.NewMicroServiceCluster(opts...)
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		runErr = errors.Join(runErr, c.Stop()(stopCtx))
		if dataDir == "" {
			if err := os.RemoveAll(c.Directory()); err != nil {
				runErr = errors.Join(runErr, err)
			}
		}
	}()
	if err := c.Prepare(ctx); err != nil {
		return err
	}
	readyCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	err := c.Start(readyCtx)
	cancel()
	if err != nil {
		return err
	}
	stopProfiles, err := startProfiles(cpuProfile, memProfile)
	if err != nil {
		return err
	}
	// Registered after cluster cleanup: profiles are finalized before teardown.
	defer func() { runErr = errors.Join(runErr, stopProfiles()) }()
	type endpoint struct {
		Name   string `yaml:"name"`
		Target string `yaml:"target"`
		URL    string `yaml:"url"`
	}
	manifest := struct {
		Components       []endpoint `yaml:"components"`
		WriteURL         string     `yaml:"write_url"`
		QueryURL         string     `yaml:"query_url"`
		MetastoreAddress string     `yaml:"metastore_address"`
	}{}
	manifest.MetastoreAddress = c.MetastoreAddress()
	for _, comp := range c.Components {
		manifest.Components = append(manifest.Components, endpoint{comp.Name(), comp.Target, comp.HTTPURL()})
		if comp.Target == "distributor" {
			manifest.WriteURL = comp.HTTPURL()
		}
		if comp.Target == "query-frontend" {
			manifest.QueryURL = comp.HTTPURL()
		}
	}
	data, err := yaml.Marshal(manifest)
	if err != nil {
		return err
	}
	if err := os.WriteFile(output+".tmp", data, 0600); err != nil {
		return err
	}
	if err := os.Rename(output+".tmp", output); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}
