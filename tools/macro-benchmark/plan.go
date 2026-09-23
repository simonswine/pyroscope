package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/grafana/pyroscope/macro-benchmark/benchmark"
)

type targetRevision struct {
	Ref    string `yaml:"ref"`
	Commit string `yaml:"commit"`
}

type datasetReplay struct {
	Name      string `yaml:"name"`
	Tenant    string `yaml:"tenant"`
	URL       string `yaml:"url,omitempty"`
	Path      string `yaml:"path,omitempty"`
	SHA256    string `yaml:"sha256,omitempty"`
	SizeBytes int64  `yaml:"size_bytes,omitempty"`
}

type plannedBenchmark struct {
	Name    string `yaml:"name"`
	Dataset string `yaml:"dataset"`
}

type runPlan struct {
	Baseline   targetRevision     `yaml:"baseline"`
	Comparison targetRevision     `yaml:"comparison"`
	Ingest     targetRevision     `yaml:"ingest"`
	Datasets   []datasetReplay    `yaml:"datasets"`
	Benchmarks []plannedBenchmark `yaml:"benchmarks"`
}

func selectedBenchmarks(c inputsConfig) ([]plannedBenchmark, error) {
	wanted := map[string]bool{}
	if c.Benchmarks != "" {
		for _, value := range strings.Split(c.Benchmarks, ",") {
			name := strings.TrimSpace(value)
			if name == "" || wanted[name] {
				return nil, fmt.Errorf("empty or duplicate benchmark %q", name)
			}
			wanted[name] = true
		}
	}
	var selected []plannedBenchmark
	for _, b := range benchmark.All() {
		if c.Benchmarks != "" && !wanted[b.Name] {
			continue
		}
		if c.Dataset != "" && c.Dataset != b.Dataset {
			continue
		}
		selected = append(selected, plannedBenchmark{Name: b.Name, Dataset: b.Dataset})
		delete(wanted, b.Name)
	}
	if len(wanted) != 0 {
		return nil, fmt.Errorf("unknown benchmarks or benchmarks outside selected dataset: %v", wanted)
	}
	if len(selected) == 0 {
		return nil, errors.New("select at least one benchmark")
	}
	return selected, nil
}

func (p runPlan) validate(c inputsConfig) error {
	selected, err := selectedBenchmarks(c)
	if err != nil {
		return err
	}
	if !slices.Equal(selected, p.Benchmarks) {
		return errors.New("plan benchmarks do not match selection")
	}
	ingestRef := c.IngestRef
	if ingestRef == "" {
		ingestRef = c.ComparisonRef
	}
	if p.Baseline.Ref != c.BaselineRef || p.Comparison.Ref != c.ComparisonRef || p.Ingest.Ref != ingestRef {
		return errors.New("plan refs do not match selection")
	}
	if c.IngestRef == "" && p.Ingest != p.Comparison {
		return errors.New("default ingest revision must match comparison")
	}
	commitPattern := regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
	for _, target := range []targetRevision{p.Baseline, p.Comparison, p.Ingest} {
		if target.Ref == "" || !commitPattern.MatchString(target.Commit) {
			return errors.New("plan contains an unresolved Git ref")
		}
	}
	needed, tenants := map[string]bool{}, map[string]bool{}
	for _, b := range selected {
		needed[b.Dataset] = true
	}
	for _, d := range p.Datasets {
		if !needed[d.Name] || !validSessionID(d.Tenant) || tenants[d.Tenant] {
			return errors.New("invalid or duplicate plan dataset/tenant")
		}
		delete(needed, d.Name)
		tenants[d.Tenant] = true
		if (d.URL == "") == (d.Path == "") || (d.Path != "" && d.Path != "fixture.replay") {
			return errors.New("invalid dataset input")
		}
		fixture := c
		fixture.FixtureURL, fixture.Fixture, fixture.FixtureSHA256 = d.URL, d.Path, d.SHA256
		fixture.FixtureSizeBytes = d.SizeBytes
		if err := fixture.validate(); err != nil {
			return err
		}
	}
	if len(needed) != 0 {
		return errors.New("plan is missing required datasets")
	}
	return nil
}

func makeRunPlan(ctx context.Context, source string, c inputsConfig) (*runPlan, error) {
	selected, err := selectedBenchmarks(c)
	if err != nil {
		return nil, err
	}
	p := &runPlan{Benchmarks: selected}
	refs := []string{c.BaselineRef, c.ComparisonRef, c.IngestRef}
	if refs[2] == "" {
		refs[2] = refs[1]
	}
	resolved := map[string]string{}
	for i, target := range []*targetRevision{&p.Baseline, &p.Comparison, &p.Ingest} {
		commit, ok := resolved[refs[i]]
		if !ok {
			commit, err = resolveRevision(ctx, source, refs[i])
			if err != nil {
				return nil, err
			}
			resolved[refs[i]] = commit
		}
		*target = targetRevision{Ref: refs[i], Commit: commit}
	}
	needed := map[string]bool{}
	for _, b := range selected {
		needed[b.Dataset] = true
	}
	if (c.Fixture != "" || c.FixtureURL != "") && len(needed) != 1 {
		return nil, errors.New("custom fixture requires benchmarks from exactly one dataset")
	}
	var names []string
	for name := range needed {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		preset, ok := defaultDatasets()[name]
		if !ok {
			return nil, fmt.Errorf("no dataset registered for %s", name)
		}
		var suffix [4]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			return nil, err
		}
		d := datasetReplay{Name: name, Tenant: name + "-" + hex.EncodeToString(suffix[:]), URL: preset.URL, SizeBytes: preset.SizeBytes}
		if c.FixtureURL != "" {
			d.URL, d.SizeBytes, d.SHA256 = c.FixtureURL, c.FixtureSizeBytes, c.FixtureSHA256
		}
		if c.Fixture != "" {
			d.URL, d.Path, d.SHA256 = "", "fixture.replay", c.FixtureSHA256
		}
		p.Datasets = append(p.Datasets, d)
	}
	return p, p.validate(c)
}
