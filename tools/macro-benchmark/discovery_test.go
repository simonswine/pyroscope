package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

func TestConfiguredRegion(t *testing.T) {
	for _, tt := range []struct {
		name string
		env  map[string]string
		cli  string
		want string
	}{
		{"profile", nil, "eu-west-1\n", "eu-west-1"},
		{"default-env", map[string]string{"AWS_DEFAULT_REGION": "eu-central-1"}, "us-east-1", "eu-central-1"},
		{"region-env", map[string]string{"AWS_REGION": "us-west-2", "AWS_DEFAULT_REGION": "eu-central-1"}, "us-east-1", "us-west-2"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result, err := configuredRegion(func(key string) string { return tt.env[key] }, func() ([]byte, error) { return []byte(tt.cli), nil })
			if err != nil || result != tt.want {
				t.Fatalf("region=%q error=%v", result, err)
			}
		})
	}
	if _, err := configuredRegion(func(string) string { return "" }, func() ([]byte, error) { return nil, errors.New("no CLI") }); err == nil {
		t.Fatal("ignored missing CLI region")
	}
}

func TestPublicIPv4(t *testing.T) {
	for _, tt := range []struct {
		body    string
		wantErr bool
	}{
		{"198.51.100.7\n", false}, {"::1", true}, {"2001:db8::1", true}, {"10.0.0.1", true}, {"127.0.0.1", true}, {"0.0.0.0", true}, {"not-an-ip", true}, {strings.Repeat("a", 1000), true},
	} {
		t.Run(tt.body[:min(len(tt.body), 16)], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(tt.body)) }))
			defer server.Close()
			result, err := publicIPv4(context.Background(), server.Client(), server.URL)
			if (err != nil) != tt.wantErr {
				t.Fatalf("CIDR=%s err=%v", result, err)
			}
			if !tt.wantErr && result != "198.51.100.7/32" {
				t.Fatal(result)
			}
		})
	}
}

func ubuntuImage(id, date string) types.Image {
	return types.Image{ImageId: aws.String(id), Name: aws.String("ubuntu/images/hvm-ssd-gp3/ubuntu-resolute-26.04-amd64-server-20260922"), OwnerId: aws.String(canonicalOwner), CreationDate: aws.String(date), Architecture: types.ArchitectureValuesX8664, RootDeviceType: types.DeviceTypeEbs, State: types.ImageStateAvailable, Public: aws.Bool(true), VirtualizationType: types.VirtualizationTypeHvm}
}

func TestNewestUbuntu(t *testing.T) {
	older := ubuntuImage("ami-old", "2026-05-01T00:00:00Z")
	newest := ubuntuImage("ami-new", "2026-09-01T00:00:00.000Z")
	otherOwner := ubuntuImage("ami-attacker", "2026-09-22T00:00:00Z")
	otherOwner.OwnerId = aws.String("untrusted")
	wrongVersion := ubuntuImage("ami-24", "2026-09-22T00:00:00Z")
	wrongVersion.Name = aws.String("ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-20260922")
	selected, err := newestUbuntu([]types.Image{otherOwner, older, wrongVersion, newest})
	if err != nil || aws.ToString(selected.ImageId) != "ami-new" {
		t.Fatalf("selection=%v error=%v", selected.ImageId, err)
	}
	if _, err := newestUbuntu([]types.Image{otherOwner, wrongVersion}); err == nil {
		t.Fatal("fell back to another owner or release")
	}
}

type imageDiscovery struct {
	discoveryAPI
	t     *testing.T
	calls int
}

func (f *imageDiscovery) DescribeImages(_ context.Context, input *ec2.DescribeImagesInput, _ ...func(*ec2.Options)) (*ec2.DescribeImagesOutput, error) {
	f.calls++
	if len(input.Owners) != 1 || input.Owners[0] != canonicalOwner {
		f.t.Fatal("AMI discovery must constrain owner")
	}
	filters := map[string]string{}
	for _, filter := range input.Filters {
		filters[aws.ToString(filter.Name)] = strings.Join(filter.Values, ",")
	}
	if filters["name"] != ubuntuAMIName || filters["architecture"] != "x86_64" || filters["is-public"] != "true" {
		f.t.Fatal("AMI filters missing")
	}
	if input.NextToken == nil {
		return &ec2.DescribeImagesOutput{Images: []types.Image{ubuntuImage("ami-old", "2026-05-01T00:00:00Z")}, NextToken: aws.String("next")}, nil
	}
	return &ec2.DescribeImagesOutput{Images: []types.Image{ubuntuImage("ami-new", "2026-09-01T00:00:00Z")}}, nil
}

func TestDiscoverUbuntuPagination(t *testing.T) {
	fake := &imageDiscovery{t: t}
	image, err := discoverUbuntu(context.Background(), fake)
	if err != nil || fake.calls != 2 || aws.ToString(image.ImageId) != "ami-new" {
		t.Fatalf("pagination: calls=%d error=%v", fake.calls, err)
	}
}

func TestNetworkSelection(t *testing.T) {
	vpcs := []types.Vpc{{VpcId: aws.String("vpc-default"), IsDefault: aws.Bool(true), State: types.VpcStateAvailable}, {VpcId: aws.String("vpc-other"), State: types.VpcStateAvailable}}
	subnet := func(id, vpc, zone string) types.Subnet {
		return types.Subnet{SubnetId: aws.String(id), VpcId: aws.String(vpc), AvailabilityZone: aws.String(zone), State: types.SubnetStateAvailable, AvailableIpAddressCount: aws.Int32(10)}
	}
	route := func(vpc string) types.RouteTable {
		return types.RouteTable{VpcId: aws.String(vpc), Associations: []types.RouteTableAssociation{{Main: aws.Bool(true)}}, Routes: []types.Route{{DestinationCidrBlock: aws.String("0.0.0.0/0"), GatewayId: aws.String("igw-test"), State: types.RouteStateActive}}}
	}
	subnets := []types.Subnet{subnet("subnet-b", "vpc-default", "zone-b"), subnet("subnet-a", "vpc-default", "zone-a"), subnet("subnet-other", "vpc-other", "zone-a")}
	tables := []types.RouteTable{route("vpc-default"), route("vpc-other")}
	offered := map[string]bool{"zone-a": true, "zone-b": true}
	vpc, id, err := chooseNetwork(vpcs, subnets, tables, offered, "", "")
	if err != nil || vpc != "vpc-default" || id != "subnet-a" {
		t.Fatalf("default selection: %s %s %v", vpc, id, err)
	}
	_, id, err = chooseNetwork(vpcs, subnets, tables, offered, "", "subnet-other")
	if err != nil || id != "subnet-other" {
		t.Fatal("explicit subnet not respected")
	}
	vpcs[0].IsDefault = aws.Bool(false)
	if _, _, err := chooseNetwork(vpcs, subnets, tables, offered, "", ""); err == nil {
		t.Fatal("arbitrarily selected among multiple VPCs")
	}
	if _, _, err := chooseNetwork(vpcs, subnets, tables, map[string]bool{}, "vpc-default", ""); err == nil {
		t.Fatal("ignored instance-type availability")
	}
	// An explicit private route table overrides the public VPC main route.
	private := types.RouteTable{VpcId: aws.String("vpc-default"), Associations: []types.RouteTableAssociation{{SubnetId: aws.String("subnet-a")}}}
	if publicSubnet(subnets[1], append(tables, private)) {
		t.Fatal("ignored explicit private route table")
	}
	subnets[1].AvailableIpAddressCount = aws.Int32(0)
	_, id, err = chooseNetwork(vpcs, subnets, tables, offered, "vpc-default", "")
	if err != nil || id != "subnet-b" {
		t.Fatal("selected exhausted subnet")
	}
}

func TestCompleteExplicitConfig(t *testing.T) {
	// All network/AMI settings are explicit: this test must make no AWS/IP calls.
	cfg := runConfig{Region: "eu-west-1", AMI: "ami-explicit", VPCID: "vpc-explicit", SubnetID: "subnet-explicit", SSHCIDR: "198.51.100.7/32"}
	if err := completeConfig(context.Background(), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.AMI != "ami-explicit" || cfg.Inputs.Dataset != "" || cfg.Inputs.Benchmarks != "" || cfg.Inputs.BaselineRef != "HEAD^" || cfg.Inputs.ComparisonRef != "HEAD" || cfg.Inputs.Count != 5 || cfg.Inputs.Benchtime != "5x" || cfg.Inputs.FixtureURL != "" {
		t.Fatalf("incorrect defaults or overrides: %+v", cfg)
	}
	if cfg.Bundle != filepath.Join("bundle", cfg.RunID) || cfg.VolumeSizeGiB != 500 {
		t.Fatal("missing per-run bundle/volume defaults")
	}
	if !regexp.MustCompile(`^[a-z]+_[a-z]+_[a-f0-9]{12}$`).MatchString(cfg.RunID) {
		t.Fatalf("bad run name %q", cfg.RunID)
	}
}

func TestEmptyConfig(t *testing.T) {
	for _, content := range []string{"", "# empty overrides\n", "{}\n"} {
		path := filepath.Join(t.TempDir(), "empty.yaml")
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		var cfg runConfig
		if err := readYAML(path, &cfg); err != nil && !errors.Is(err, io.EOF) {
			t.Fatal(err)
		}
		if cfg != (runConfig{}) {
			t.Fatal("empty config acquired unexpected settings")
		}
	}
}

func TestPrivateDiscoveryNeedsOverrides(t *testing.T) {
	cfg := runConfig{Region: "eu-west-1", PrivateIP: true}
	if err := completeConfig(context.Background(), &cfg); err == nil || !strings.Contains(err.Error(), "private_ip requires") {
		t.Fatalf("private connectivity should not be guessed: %v", err)
	}
}

func TestGeneratedConfig(t *testing.T) {
	t.Chdir(t.TempDir())
	cfg := runConfig{RunID: "tracing_otter_test", Region: "eu-west-1"}
	path, err := writeGeneratedConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^run-\d{4}-\d{2}-\d{2}-\d{2}-\d{2}-\d{2}-tracing_otter_test\.yaml$`).MatchString(path) {
		t.Fatal(path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal("insecure generated config permissions")
	}
	if err := writeExclusiveConfig(path, runConfig{Region: "overwritten"}); !os.IsExist(err) {
		t.Fatalf("overwrote existing file: %v", err)
	}
	var restored runConfig
	if err := readYAML(path, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Region != cfg.Region {
		t.Fatal("config changed")
	}
}
