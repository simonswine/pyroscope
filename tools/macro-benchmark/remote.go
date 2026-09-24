package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type componentEndpoint struct {
	Name   string `yaml:"name"`
	Target string `yaml:"target"`
	URL    string `yaml:"url"`
}

type endpointManifest struct {
	Components       []componentEndpoint `yaml:"components"`
	WriteURL         string              `yaml:"write_url"`
	QueryURL         string              `yaml:"query_url"`
	MetastoreAddress string              `yaml:"metastore_address"`
	MinioURL         string              `yaml:"minio_url,omitempty"`
	Processes        map[string]int      `yaml:"processes,omitempty"`
}

func requireExecutable(name string) error { _, err := exec.LookPath(name); return err }

var schedulerSample = regexp.MustCompile(`^\w*compaction_scheduler_queue_jobs\{`)

func schedulerIdle(metrics string) bool {
	found := false
	healthy := false
	for _, line := range strings.Split(metrics, "\n") {
		// Queue metrics are lazy: after recovery an empty scheduler may have
		// no initialized levels and therefore emit no queue samples. Require
		// a runtime sample so an empty or invalid response is not called idle.
		if strings.HasPrefix(line, "go_goroutines ") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				value, err := strconv.ParseUint(fields[1], 10, 64)
				healthy = err == nil && value > 0
			}
		}
		if !schedulerSample.MatchString(line) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return false
		}
		value, err := strconv.ParseFloat(fields[1], 64)
		if err != nil || value != 0 {
			return false
		}
		found = true
	}
	return found || healthy
}

func getMetrics(ctx context.Context, url string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metrics HTTP %d from %s", response.StatusCode, url)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 64<<20+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 64<<20 {
		return nil, errors.New("metrics response exceeds 64 MiB")
	}
	return data, nil
}

func snapshot(ctx context.Context, root string) ([]string, error) {
	var endpoints endpointManifest
	if err := readYAML(filepath.Join(remoteBundle, "endpoints.yaml"), &endpoints); err != nil {
		return nil, err
	}
	dest := filepath.Join(root, strconv.FormatInt(time.Now().UnixNano(), 10))
	if err := os.MkdirAll(dest, 0700); err != nil {
		return nil, err
	}
	var stores []string
	for _, component := range endpoints.Components {
		metrics, err := getMetrics(ctx, component.URL+"/metrics")
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(dest, component.Name+".prom"), metrics, 0600); err != nil {
			return nil, err
		}
		if component.Target == "metastore" {
			stores = append(stores, string(metrics))
		}
	}
	if endpoints.MinioURL != "" {
		metrics, err := getMetrics(ctx, endpoints.MinioURL+"/minio/v2/metrics/cluster")
		if err != nil {
			log.Printf("MinIO metrics: %v", err)
		} else if err := os.WriteFile(filepath.Join(dest, "minio.prom"), metrics, 0600); err != nil {
			return nil, err
		}
	}
	for _, file := range []string{"stat", "meminfo", "diskstats", "net/dev"} {
		data, err := os.ReadFile(filepath.Join("/proc", file))
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(dest, strings.ReplaceAll(file, "/", "-")), data, 0600); err != nil {
			return nil, err
		}
	}
	for name, pid := range endpoints.Processes {
		for _, file := range []string{"stat", "status", "io", "smaps_rollup"} {
			data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), file))
			if err != nil {
				return nil, err
			}
			if err := os.WriteFile(filepath.Join(dest, name+"-"+file), data, 0600); err != nil {
				return nil, err
			}
		}
	}
	return stores, nil
}

func observe(ctx context.Context, args []string) error {
	f := flag.NewFlagSet("observe", flag.ContinueOnError)
	settle := f.Bool("settle", false, "Require a quiet compaction window")
	output := f.String("output", "/tmp/results/metrics", "Output directory")
	quiet := f.Duration("quiet", time.Minute, "Required quiet duration")
	timeout := f.Duration("timeout", 30*time.Minute, "Collection deadline")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *quiet <= 0 || *timeout <= 0 {
		return errors.New("durations must be positive")
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	var quietSince time.Time
	for {
		stores, err := snapshot(ctx, *output)
		idle := err == nil && len(stores) == 3
		if err != nil {
			log.Printf("metrics collection: %v", err)
		}
		for _, metrics := range stores {
			idle = idle && schedulerIdle(metrics)
		}
		if *settle {
			if idle {
				if quietSince.IsZero() {
					quietSince = time.Now()
				}
				if time.Since(quietSince) >= *quiet {
					return nil
				}
			} else {
				quietSince = time.Time{}
			}
		}
		select {
		case <-ctx.Done():
			if *settle {
				return fmt.Errorf("waiting for quiet compaction window: %w", ctx.Err())
			}
			return ctx.Err()
		case <-time.After(15 * time.Second):
		}
	}
}
