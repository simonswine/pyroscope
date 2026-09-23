package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

func TestRunConfig(t *testing.T) {
	valid := runConfig{Region: "us-east-1", AMI: "ami-example", SubnetID: "subnet-example", VPCID: "vpc-example", SSHCIDR: "192.0.2.1/32", RunID: "smoke-1"}
	if err := valid.validate(); err != nil {
		t.Fatal(err)
	}
	if valid.KeyPath != ".ssh/id_ed25519" || valid.TimeoutMinutes != 240 {
		t.Fatal("missing defaults")
	}
	for _, cidr := range []string{"0.0.0.0/0", "192.0.2.0/24", "invalid"} {
		cfg := valid
		cfg.SSHCIDR = cidr
		if cfg.validate() == nil {
			t.Fatalf("accepted CIDR %q", cidr)
		}
	}
	for _, id := range []string{"", "../escape", "has space"} {
		cfg := valid
		cfg.RunID = id
		if cfg.validate() == nil {
			t.Fatalf("accepted run ID %q", id)
		}
	}
}

func TestSSHKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".ssh", "id_ed25519")
	public, err := ensureSSHKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(public), "ssh-ed25519 ") {
		t.Fatal("not Ed25519")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal("insecure key permissions")
	}
	again, err := ensureSSHKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(public) {
		t.Fatal("key changed")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureSSHKey(path); err == nil {
		t.Fatal("accepted insecure key")
	}
}

func TestExpired(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name, purpose, expiry string
		want                  bool
	}{
		{"expired", purpose, "2025-01-01T00:00:00Z", true}, {"future", purpose, "2030-01-01T00:00:00Z", false},
		{"invalid", purpose, "invalid", false}, {"missing", purpose, "", false}, {"unrelated", "other", "2025-01-01T00:00:00Z", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tags := []types.Tag{{Key: aws.String("Purpose"), Value: aws.String(tt.purpose)}, {Key: aws.String("ExpiresAt"), Value: aws.String(tt.expiry)}}
			if expired(tags, now) != tt.want {
				t.Fatal("unexpected expiry result")
			}
		})
	}
}

func TestSchedulerIdle(t *testing.T) {
	for _, tt := range []struct {
		metrics string
		want    bool
	}{
		{"", false}, {`compaction_scheduler_queue_jobs{level="0",status="assigned"} 0`, true},
		{`compaction_scheduler_queue_jobs{level="0",status="assigned"} 1`, false}, {`pyroscope_compaction_scheduler_queue_jobs{level="0"} NaN`, false},
	} {
		if schedulerIdle(tt.metrics) != tt.want {
			t.Fatalf("unexpected idle result for %q", tt.metrics)
		}
	}
}

func TestMinioInputs(t *testing.T) {
	cfg := inputsConfig{Fixture: "fixture", FixtureSHA256: strings.Repeat("a", 64), TenantID: "test", MinioURL: "https://github.com/pgsty/minio/releases/download/pinned/minio", MinioSHA256: strings.Repeat("a", 64)}
	cfg.defaults()
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	cfg.MinioSHA256 = "bad"
	if cfg.validate() == nil {
		t.Fatal("accepted missing checksum")
	}
	cfg.MinioSHA256 = strings.Repeat("a", 64)
	cfg.MinioURL = "http://example.com/minio"
	if cfg.validate() == nil {
		t.Fatal("accepted HTTP download")
	}
}

func TestCreateMinioBucket(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" || r.URL.Path != "/macro-benchmark" {
			t.Errorf("unexpected S3 request: %s %s", r.Method, r.URL)
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			t.Error("missing SigV4 signature")
		}
		if !strings.Contains(r.Header.Get("Authorization"), "Credential=local-user/") {
			t.Error("wrong credentials")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	if err := createBucket(context.Background(), server.URL, "macro-benchmark", "local-user", "local-secret"); err != nil {
		t.Fatal(err)
	}
}
