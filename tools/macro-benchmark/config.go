package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

type inputsConfig struct {
	Benchmarks       string `yaml:"benchmarks,omitempty"` // Comma-separated names; empty means all.
	BaselineRef      string `yaml:"baseline_ref"`
	ComparisonRef    string `yaml:"comparison_ref"`
	IngestRef        string `yaml:"ingest_ref,omitempty"` // Empty follows ComparisonRef.
	Dataset          string `yaml:"dataset,omitempty"`
	FixtureURL       string `yaml:"fixture_url,omitempty"`
	FixtureSizeBytes int64  `yaml:"fixture_size_bytes,omitempty"`
	FixtureCRC32C    string `yaml:"fixture_crc32c,omitempty"`
	ReplayTimeout    string `yaml:"replay_timeout"`
	Fixture          string `yaml:"fixture,omitempty"`
	FixtureSHA256    string `yaml:"fixture_sha256,omitempty"`
	TenantID         string `yaml:"tenant_id"`
	ProfileType      string `yaml:"profile_type"`
	Selector         string `yaml:"selector"`
	MinioURL         string `yaml:"minio_url"`
	MinioSHA256      string `yaml:"minio_sha256"`
	Benchtime        string `yaml:"benchtime"`
	Count            int    `yaml:"count"`
	SettleSeconds    int    `yaml:"settle_seconds"`
}

func (c *inputsConfig) defaults() {
	if c.ReplayTimeout == "" {
		c.ReplayTimeout = "3h"
	}
	if c.ProfileType == "" {
		c.ProfileType = "process_cpu:cpu:nanoseconds:cpu:nanoseconds"
	}
	if c.Selector == "" {
		c.Selector = "{}"
	}
	if c.Benchtime == "" {
		c.Benchtime = "5x"
	}
	if c.Count == 0 {
		c.Count = 5
	}
	if c.SettleSeconds == 0 {
		c.SettleSeconds = 120
	}
}

func (c inputsConfig) validate() error {
	if c.TenantID == "" {
		return errors.New("inputs.tenant_id is required")
	}
	if c.BaselineRef != "" || c.ComparisonRef != "" {
		if c.BaselineRef == "" || c.ComparisonRef == "" {
			return errors.New("baseline_ref and comparison_ref are required")
		}
		if _, err := selectedBenchmarks(c); err != nil {
			return err
		}
	} else if (c.Fixture == "") == (c.FixtureURL == "") {
		return errors.New("choose one local fixture or public dataset/fixture_url")
	}
	if c.Fixture != "" && c.FixtureURL != "" {
		return errors.New("fixture and fixture_url are mutually exclusive")
	}
	if c.FixtureSHA256 != "" && !regexp.MustCompile(`^[a-fA-F0-9]{64}$`).MatchString(c.FixtureSHA256) {
		return errors.New("invalid fixture_sha256")
	}
	if c.Fixture != "" && !regexp.MustCompile(`^[a-fA-F0-9]{64}$`).MatchString(c.FixtureSHA256) {
		return errors.New("local fixture requires a trusted SHA256")
	}
	if c.FixtureURL != "" {
		u, err := url.Parse(c.FixtureURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			return errors.New("fixture_url must use HTTPS without credentials")
		}
		crc, err := base64.StdEncoding.DecodeString(c.FixtureCRC32C)
		if err != nil || len(crc) != 4 || c.FixtureSizeBytes <= 0 || c.FixtureSizeBytes > 1<<40 {
			return errors.New("remote fixture requires size (up to 1 TiB) and base64 CRC32C")
		}
	}
	if d, err := time.ParseDuration(c.ReplayTimeout); err != nil || d <= 0 {
		return errors.New("replay_timeout must be a positive duration")
	}
	u, err := url.Parse(c.MinioURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return errors.New("minio_url must be an HTTPS release-binary URL without credentials")
	}
	if !regexp.MustCompile(`^[a-fA-F0-9]{64}$`).MatchString(c.MinioSHA256) {
		return errors.New("minio_sha256 must be a trusted SHA256 digest")
	}
	if strings.HasSuffix(c.Benchtime, "x") {
		n, err := strconv.ParseUint(strings.TrimSuffix(c.Benchtime, "x"), 10, 63)
		if err != nil || n == 0 {
			return errors.New("inputs.benchtime must be a positive iteration count or duration")
		}
	} else if d, err := time.ParseDuration(c.Benchtime); err != nil || d <= 0 {
		return errors.New("inputs.benchtime must be a positive iteration count or duration")
	}
	if c.Count < 1 || c.SettleSeconds < 0 {
		return errors.New("count must be positive and settle_seconds nonnegative")
	}
	return nil
}

func readYAML(path string, out any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("%s must contain exactly one YAML document", path)
	}
	return nil
}

func writeYAML(path string, value any) error {
	data, err := yaml.Marshal(value)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}
