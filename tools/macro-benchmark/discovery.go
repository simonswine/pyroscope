package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"go.yaml.in/yaml/v3"
)

const canonicalOwner = "099720109477"
const ubuntuAMIName = "ubuntu/images/hvm-ssd-gp3/ubuntu-*-26.04-amd64-server-*"
const publicIPWebsite = "https://checkip.amazonaws.com"

// Discovery is read-only: it never creates a VPC, subnet, key pair or instance.
type discoveryAPI interface {
	DescribeImages(context.Context, *ec2.DescribeImagesInput, ...func(*ec2.Options)) (*ec2.DescribeImagesOutput, error)
	DescribeVpcs(context.Context, *ec2.DescribeVpcsInput, ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error)
	DescribeSubnets(context.Context, *ec2.DescribeSubnetsInput, ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error)
	DescribeRouteTables(context.Context, *ec2.DescribeRouteTablesInput, ...func(*ec2.Options)) (*ec2.DescribeRouteTablesOutput, error)
	DescribeInstanceTypeOfferings(context.Context, *ec2.DescribeInstanceTypeOfferingsInput, ...func(*ec2.Options)) (*ec2.DescribeInstanceTypeOfferingsOutput, error)
}

func configuredRegion(getenv func(string) string, cli func() ([]byte, error)) (string, error) {
	for _, key := range []string{"AWS_REGION", "AWS_DEFAULT_REGION"} {
		if value := strings.TrimSpace(getenv(key)); value != "" {
			return value, nil
		}
	}
	output, err := cli()
	if err != nil {
		return "", fmt.Errorf("cannot read AWS CLI region; set region or configure your AWS profile: %w", err)
	}
	region := strings.TrimSpace(string(output))
	if region == "" {
		return "", errors.New("AWS CLI has no region; set region or run aws configure")
	}
	return region, nil
}

func publicIPv4(ctx context.Context, client *http.Client, website string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, website, nil)
	if err != nil {
		return "", err
	}
	response, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("IPv4 discovery: HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 65))
	if err != nil {
		return "", err
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(string(data)))
	if err != nil || !addr.Is4() || !addr.IsGlobalUnicast() || addr.IsPrivate() {
		return "", errors.New("IPv4 website did not return a public IPv4 address")
	}
	return addr.String() + "/32", nil
}

func observabilityName() (string, error) {
	verbs := []string{
		"analyzing", "auditing", "benchmarking", "bikeshedding", "cataloging", "comparing",
		"diagnosing", "exploring", "herdingcats", "inspecting", "measuring", "monitoring",
		"observing", "probing", "profiling", "querying", "recording", "rubberducking",
		"sampling", "scraping", "spelunking", "surveying", "testing", "tracing", "tracking",
		"validating", "watching", "yakshaving",
	}
	animals := []string{
		"alpaca", "badger", "beaver", "capybara", "dolphin", "elk", "falcon", "gecko",
		"heron", "ibis", "jaguar", "kestrel", "koala", "lemur", "lynx", "marten",
		"narwhal", "oryx", "otter", "puffin", "quokka", "raccoon", "stoat", "wombat",
	}
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return verbs[int(random[0])%len(verbs)] + "_" + animals[int(random[1])%len(animals)] + "_" + hex.EncodeToString(random[2:]), nil
}

func completeConfig(ctx context.Context, cfg *runConfig) error {
	example := defaultRunConfig()
	if cfg.RunID == "" {
		name, err := observabilityName()
		if err != nil {
			return err
		}
		cfg.RunID = name
	}
	if cfg.Region == "" {
		region, err := configuredRegion(os.Getenv, func() ([]byte, error) {
			cliCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			return exec.CommandContext(cliCtx, "aws", "configure", "get", "region").Output()
		})
		if err != nil {
			return err
		}
		cfg.Region = region
	}
	if cfg.PrivateIP && (cfg.SSHCIDR == "" || cfg.SubnetID == "" || cfg.VPCID == "") {
		return errors.New("private_ip requires explicit ssh_cidr, vpc_id and subnet_id; website discovery cannot infer private connectivity")
	}
	if cfg.SSHCIDR == "" {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		dialer := &net.Dialer{Timeout: 10 * time.Second}
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp4", address)
		}
		defer transport.CloseIdleConnections()
		cidr, err := publicIPv4(ctx, &http.Client{Transport: transport, Timeout: 15 * time.Second}, publicIPWebsite)
		if err != nil {
			return fmt.Errorf("discover SSH source address: %w", err)
		}
		cfg.SSHCIDR = cidr
	}
	if cfg.AMI == "" || cfg.VPCID == "" || cfg.SubnetID == "" {
		discoveryCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		client, err := awsClient(discoveryCtx, cfg.Region)
		if err != nil {
			return err
		}
		if cfg.AMI == "" {
			image, err := discoverUbuntu(discoveryCtx, client)
			if err != nil {
				return err
			}
			cfg.AMI = aws.ToString(image.ImageId)
			log.Printf("selected Canonical Ubuntu 26.04: %s (%s)", cfg.AMI, aws.ToString(image.Name))
		}
		if cfg.VPCID == "" || cfg.SubnetID == "" {
			vpc, subnet, err := discoverNetwork(discoveryCtx, client, cfg.VPCID, cfg.SubnetID)
			if err != nil {
				return err
			}
			cfg.VPCID, cfg.SubnetID = vpc, subnet
		}
	}
	if cfg.Bundle == "" {
		cfg.Bundle = filepath.Join("bundle", cfg.RunID)
	}
	if cfg.TimeoutMinutes == 0 {
		cfg.TimeoutMinutes = example.TimeoutMinutes
	}
	if cfg.VolumeSizeGiB == 0 {
		cfg.VolumeSizeGiB = example.VolumeSizeGiB
	}
	if cfg.Inputs.BaselineRef == "" {
		cfg.Inputs.BaselineRef = example.Inputs.BaselineRef
	}
	if cfg.Inputs.ComparisonRef == "" {
		cfg.Inputs.ComparisonRef = example.Inputs.ComparisonRef
	}
	if cfg.Inputs.MinioURL == "" {
		cfg.Inputs.MinioURL = example.Inputs.MinioURL
	}
	if cfg.Inputs.MinioSHA256 == "" && cfg.Inputs.MinioURL == example.Inputs.MinioURL {
		cfg.Inputs.MinioSHA256 = example.Inputs.MinioSHA256
	}
	if cfg.Inputs.TenantID == "" {
		cfg.Inputs.TenantID = example.Inputs.TenantID
	}
	if cfg.Inputs.Benchtime == "" {
		cfg.Inputs.Benchtime = example.Inputs.Benchtime
	}
	if cfg.Inputs.Count == 0 {
		cfg.Inputs.Count = example.Inputs.Count
	}
	cfg.Inputs.defaults()
	if err := cfg.validate(); err != nil {
		return err
	}
	resolved := cfg.Inputs
	if err := resolved.resolveDataset(); err != nil {
		return err
	}
	resolved.defaults()
	return resolved.validate()
}

func discoverUbuntu(ctx context.Context, client discoveryAPI) (types.Image, error) {
	pages := ec2.NewDescribeImagesPaginator(client, &ec2.DescribeImagesInput{
		Owners: []string{canonicalOwner}, IncludeDeprecated: aws.Bool(false),
		Filters: []types.Filter{
			{Name: aws.String("name"), Values: []string{ubuntuAMIName}},
			{Name: aws.String("state"), Values: []string{"available"}},
			{Name: aws.String("architecture"), Values: []string{"x86_64"}},
			{Name: aws.String("root-device-type"), Values: []string{"ebs"}},
			{Name: aws.String("virtualization-type"), Values: []string{"hvm"}},
			{Name: aws.String("is-public"), Values: []string{"true"}},
		},
	})
	var images []types.Image
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return types.Image{}, err
		}
		images = append(images, page.Images...)
	}
	return newestUbuntu(images)
}

func newestUbuntu(images []types.Image) (types.Image, error) {
	var selected types.Image
	var newest time.Time
	for _, image := range images {
		// Defense in depth: never trust a similarly named image from another owner.
		match, _ := filepath.Match(ubuntuAMIName, aws.ToString(image.Name))
		if !match || aws.ToString(image.OwnerId) != canonicalOwner || image.Architecture != types.ArchitectureValuesX8664 || image.RootDeviceType != types.DeviceTypeEbs || image.State != types.ImageStateAvailable || !aws.ToBool(image.Public) || image.VirtualizationType != types.VirtualizationTypeHvm {
			continue
		}
		created, err := time.Parse(time.RFC3339, aws.ToString(image.CreationDate))
		if err != nil {
			continue
		}
		if created.After(newest) || (created.Equal(newest) && aws.ToString(image.ImageId) > aws.ToString(selected.ImageId)) {
			selected, newest = image, created
		}
	}
	if aws.ToString(selected.ImageId) == "" {
		return types.Image{}, errors.New("no official Canonical Ubuntu Server 26.04 amd64 AMI found in this region; specify ami explicitly (no older-version fallback)")
	}
	return selected, nil
}

func discoverNetwork(ctx context.Context, client discoveryAPI, vpcID, subnetID string) (string, string, error) {
	var vpcs []types.Vpc
	vp := ec2.NewDescribeVpcsPaginator(client, &ec2.DescribeVpcsInput{Filters: []types.Filter{{Name: aws.String("state"), Values: []string{"available"}}}})
	for vp.HasMorePages() {
		page, err := vp.NextPage(ctx)
		if err != nil {
			return "", "", err
		}
		vpcs = append(vpcs, page.Vpcs...)
	}
	filters := []types.Filter{{Name: aws.String("state"), Values: []string{"available"}}}
	if vpcID != "" {
		filters = append(filters, types.Filter{Name: aws.String("vpc-id"), Values: []string{vpcID}})
	}
	if subnetID != "" {
		filters = append(filters, types.Filter{Name: aws.String("subnet-id"), Values: []string{subnetID}})
	}
	var subnets []types.Subnet
	sp := ec2.NewDescribeSubnetsPaginator(client, &ec2.DescribeSubnetsInput{Filters: filters})
	for sp.HasMorePages() {
		page, err := sp.NextPage(ctx)
		if err != nil {
			return "", "", err
		}
		subnets = append(subnets, page.Subnets...)
	}
	var tables []types.RouteTable
	rp := ec2.NewDescribeRouteTablesPaginator(client, &ec2.DescribeRouteTablesInput{})
	for rp.HasMorePages() {
		page, err := rp.NextPage(ctx)
		if err != nil {
			return "", "", err
		}
		tables = append(tables, page.RouteTables...)
	}
	offered := map[string]bool{}
	op := ec2.NewDescribeInstanceTypeOfferingsPaginator(client, &ec2.DescribeInstanceTypeOfferingsInput{LocationType: types.LocationTypeAvailabilityZone, Filters: []types.Filter{{Name: aws.String("instance-type"), Values: []string{"c6i.8xlarge"}}}})
	for op.HasMorePages() {
		page, err := op.NextPage(ctx)
		if err != nil {
			return "", "", err
		}
		for _, o := range page.InstanceTypeOfferings {
			offered[aws.ToString(o.Location)] = true
		}
	}
	return chooseNetwork(vpcs, subnets, tables, offered, vpcID, subnetID)
}

func publicSubnet(subnet types.Subnet, tables []types.RouteTable) bool {
	var main, explicit *types.RouteTable
	for i := range tables {
		table := &tables[i]
		if aws.ToString(table.VpcId) != aws.ToString(subnet.VpcId) {
			continue
		}
		for _, association := range table.Associations {
			if aws.ToBool(association.Main) {
				main = table
			}
			if aws.ToString(association.SubnetId) == aws.ToString(subnet.SubnetId) {
				explicit = table
			}
		}
	}
	table := explicit
	if table == nil {
		table = main
	}
	if table == nil {
		return false
	}
	for _, route := range table.Routes {
		if aws.ToString(route.DestinationCidrBlock) == "0.0.0.0/0" && strings.HasPrefix(aws.ToString(route.GatewayId), "igw-") && route.State == types.RouteStateActive {
			return true
		}
	}
	return false
}

func chooseNetwork(vpcs []types.Vpc, subnets []types.Subnet, tables []types.RouteTable, offered map[string]bool, vpcID, subnetID string) (string, string, error) {
	available := map[string]bool{}
	defaults := map[string]bool{}
	for _, vpc := range vpcs {
		if vpc.State == types.VpcStateAvailable {
			available[aws.ToString(vpc.VpcId)] = true
			defaults[aws.ToString(vpc.VpcId)] = aws.ToBool(vpc.IsDefault)
		}
	}
	candidates := map[string][]types.Subnet{}
	for _, subnet := range subnets {
		vpc, id := aws.ToString(subnet.VpcId), aws.ToString(subnet.SubnetId)
		if !available[vpc] || (vpcID != "" && vpcID != vpc) || (subnetID != "" && subnetID != id) {
			continue
		}
		if subnet.State != types.SubnetStateAvailable || aws.ToInt32(subnet.AvailableIpAddressCount) < 1 || aws.ToBool(subnet.Ipv6Native) || !offered[aws.ToString(subnet.AvailabilityZone)] || !publicSubnet(subnet, tables) {
			continue
		}
		candidates[vpc] = append(candidates[vpc], subnet)
	}
	chosen := vpcID
	if chosen == "" {
		for vpc := range candidates {
			if defaults[vpc] {
				chosen = vpc
				break
			}
		}
	}
	if chosen == "" && len(candidates) == 1 {
		for vpc := range candidates {
			chosen = vpc
		}
	}
	if chosen == "" && len(candidates) > 1 {
		return "", "", errors.New("multiple eligible VPCs and no default VPC; set vpc_id explicitly")
	}
	eligible := candidates[chosen]
	if len(eligible) == 0 {
		return "", "", errors.New("no available IPv4 subnet with an active internet-gateway route in an AZ offering c6i.8xlarge; supply suitable vpc_id/subnet_id explicitly")
	}
	sort.Slice(eligible, func(i, j int) bool {
		if aws.ToBool(eligible[i].DefaultForAz) != aws.ToBool(eligible[j].DefaultForAz) {
			return aws.ToBool(eligible[i].DefaultForAz)
		}
		if aws.ToString(eligible[i].AvailabilityZone) != aws.ToString(eligible[j].AvailabilityZone) {
			return aws.ToString(eligible[i].AvailabilityZone) < aws.ToString(eligible[j].AvailabilityZone)
		}
		return aws.ToString(eligible[i].SubnetId) < aws.ToString(eligible[j].SubnetId)
	})
	return chosen, aws.ToString(eligible[0].SubnetId), nil
}

func writeGeneratedConfig(ctx context.Context, cfg runConfig) (string, error) {
	for {
		path := "run-" + time.Now().UTC().Format("2006-01-02-15-04-05") + "-" + cfg.RunID + ".yaml"
		err := writeExclusiveConfig(path, cfg)
		if err == nil {
			return path, nil
		}
		if !os.IsExist(err) {
			return "", err
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func writeExclusiveConfig(path string, cfg runConfig) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	return errors.Join(writeErr, file.Close())
}
