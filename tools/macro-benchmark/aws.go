package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

const purpose = "pyroscope-macro-benchmark"

type runConfig struct {
	Region         string       `yaml:"region"`
	AMI            string       `yaml:"ami"`
	SubnetID       string       `yaml:"subnet_id"`
	VPCID          string       `yaml:"vpc_id"`
	SSHCIDR        string       `yaml:"ssh_cidr"`
	RunID          string       `yaml:"run_id"`
	PrivateIP      bool         `yaml:"private_ip"`
	KeyPath        string       `yaml:"key_path"`
	Bundle         string       `yaml:"bundle"`
	Results        string       `yaml:"results"`
	TimeoutMinutes int          `yaml:"timeout_minutes"`
	VolumeSizeGiB  int32        `yaml:"volume_size_gib"`
	Inputs         inputsConfig `yaml:"inputs"`
}

func (c *runConfig) validate() error {
	if c.Region == "" || c.AMI == "" || c.SubnetID == "" || c.VPCID == "" {
		return errors.New("region, ami, subnet_id and vpc_id are required")
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`).MatchString(c.RunID) {
		return errors.New("run_id must be 1–64 letters, digits, underscores or hyphens")
	}
	prefix, err := netip.ParsePrefix(c.SSHCIDR)
	if err != nil || !prefix.Addr().Is4() || prefix.Bits() != 32 {
		return errors.New("ssh_cidr must be the runner's IPv4 /32")
	}
	if c.KeyPath == "" {
		c.KeyPath = ".ssh/id_ed25519"
	}
	if c.Bundle == "" {
		c.Bundle = "bundle"
	}
	if c.Results == "" {
		c.Results = "results"
	}
	if c.VolumeSizeGiB == 0 {
		c.VolumeSizeGiB = 200
	}
	if c.VolumeSizeGiB < 80 || c.VolumeSizeGiB > 16384 {
		return errors.New("volume_size_gib must be between 80 and 16384")
	}
	if c.TimeoutMinutes == 0 {
		c.TimeoutMinutes = 240
	}
	if c.TimeoutMinutes < 1 || c.TimeoutMinutes > 1440 {
		return errors.New("timeout_minutes must be between 1 and 1440")
	}
	return nil
}

func awsClient(ctx context.Context, region string) (*ec2.Client, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, err
	}
	return ec2.NewFromConfig(cfg), nil
}

// resumeAWS reconciles persisted intent with AWS before taking any action. It
// deliberately has no cleanup defer: losing the controller must not kill work.
func resumeAWS(ctx context.Context, store *stateStore, state *sessionState) error {
	cfg := state.Config
	if state.Phase == "destroying" || state.Phase == "destroyed" {
		return errors.New("session is being destroyed")
	}
	if !state.Uploaded {
		if state.Plan == nil {
			return errors.New("session has no benchmark plan; create and prepare a new session")
		}
		required := []string{"settings.yaml", "plan.yaml", "macro-benchmark", "cluster-ingest", "cluster-baseline", "cluster-comparison", "minio", "profilecli"}
		for _, b := range state.Plan.Benchmarks {
			required = append(required, b.Name+".test")
		}
		for _, name := range required {
			if _, err := os.Stat(filepath.Join(cfg.Bundle, name)); err != nil {
				return fmt.Errorf("prepare bundle first: %w", err)
			}
		}
		if err := verifyBundle(cfg.Bundle); err != nil {
			return err
		}
		var bundledInputs inputsConfig
		if err := readYAML(filepath.Join(cfg.Bundle, "settings.yaml"), &bundledInputs); err != nil {
			return err
		}
		if err := bundledInputs.validate(); err != nil {
			return err
		}
		if cfg.Inputs != bundledInputs {
			return errors.New("inputs differ from prepared bundle; rerun prepare or omit -skip-prepare")
		}
		var bundledPlan runPlan
		if err := readYAML(filepath.Join(cfg.Bundle, "plan.yaml"), &bundledPlan); err != nil {
			return err
		}
		if !reflect.DeepEqual(state.Plan, &bundledPlan) {
			return errors.New("plan differs from prepared bundle")
		}
		if bundledInputs.Fixture != "" {
			if _, err := os.Stat(filepath.Join(cfg.Bundle, "fixture.replay")); err != nil {
				return err
			}
		}
	}
	for _, executable := range []string{"ssh", "scp"} {
		if err := requireExecutable(executable); err != nil {
			return err
		}
	}
	public, err := ensureSSHKey(cfg.KeyPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.Results, 0700); err != nil {
		return err
	}
	resultDir := filepath.Join(cfg.Results, cfg.RunID)
	if err := os.MkdirAll(resultDir, 0700); err != nil {
		return err
	}
	client, err := awsClient(ctx, cfg.Region)
	if err != nil {
		return err
	}
	if state.ExpiresAt.IsZero() {
		state.ExpiresAt = time.Now().UTC().Add(time.Duration(cfg.TimeoutMinutes) * time.Minute)
	}
	state.KeyName = "pyroscope-macro-" + cfg.RunID
	if !state.StartRequested {
		state.Phase = "provisioning"
	}
	if err := store.save(state); err != nil {
		return err
	}
	expiry := state.ExpiresAt
	tags := []types.Tag{
		{Key: aws.String("Name"), Value: aws.String("pyroscope-macro-" + cfg.RunID)},
		{Key: aws.String("Purpose"), Value: aws.String(purpose)},
		{Key: aws.String("RunID"), Value: aws.String(cfg.RunID)},
		{Key: aws.String("ExpiresAt"), Value: aws.String(expiry.Format(time.RFC3339))},
	}
	keyName := state.KeyName
	groupID, instanceID := state.GroupID, state.InstanceID
	if err := reconcileResources(ctx, client, state); err != nil {
		return err
	}
	groupID, instanceID = state.GroupID, state.InstanceID
	if err := store.save(state); err != nil {
		return err
	}
	if instanceID == "" {
		if time.Now().After(state.ExpiresAt) {
			return errors.New("instance deadline expired; destroy resources or create a new session")
		}
		images, err := client.DescribeImages(ctx, &ec2.DescribeImagesInput{ImageIds: []string{cfg.AMI}})
		if err != nil {
			return err
		}
		if len(images.Images) != 1 || images.Images[0].Architecture != types.ArchitectureValuesX8664 || images.Images[0].RootDeviceType != types.DeviceTypeEbs {
			return errors.New("AMI must be EBS-backed amd64 Ubuntu")
		}
		if err := ensureAWSKey(ctx, client, keyName, cfg.RunID, public, tags); err != nil {
			return err
		}
		if groupID == "" {
			group, err := client.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
				GroupName: aws.String(keyName), Description: aws.String("Temporary macro benchmark SSH"), VpcId: aws.String(cfg.VPCID),
				TagSpecifications: []types.TagSpecification{{ResourceType: types.ResourceTypeSecurityGroup, Tags: tags}},
			})
			if err != nil {
				return err
			}
			groupID = aws.ToString(group.GroupId)
			state.GroupID = groupID
			if err := store.save(state); err != nil {
				return err
			}
		}
		_, err = client.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
			GroupId: aws.String(groupID), IpPermissions: []types.IpPermission{{IpProtocol: aws.String("tcp"), FromPort: aws.Int32(22), ToPort: aws.Int32(22), IpRanges: []types.IpRange{{CidrIp: aws.String(cfg.SSHCIDR)}}}},
		})
		if err != nil && !awsErrorCode(err, "InvalidPermission.Duplicate") {
			return err
		}
		launched, err := client.RunInstances(ctx, &ec2.RunInstancesInput{
			ImageId: aws.String(cfg.AMI), InstanceType: types.InstanceTypeC6i8xlarge, MinCount: aws.Int32(1), MaxCount: aws.Int32(1),
			ClientToken: aws.String(cfg.RunID), KeyName: aws.String(keyName),
			CpuOptions:          &types.CpuOptionsRequest{CoreCount: aws.Int32(16), ThreadsPerCore: aws.Int32(1)},
			MetadataOptions:     &types.InstanceMetadataOptionsRequest{HttpTokens: types.HttpTokensStateRequired},
			NetworkInterfaces:   []types.InstanceNetworkInterfaceSpecification{{DeviceIndex: aws.Int32(0), SubnetId: aws.String(cfg.SubnetID), Groups: []string{groupID}, AssociatePublicIpAddress: aws.Bool(!cfg.PrivateIP), DeleteOnTermination: aws.Bool(true)}},
			BlockDeviceMappings: []types.BlockDeviceMapping{{DeviceName: images.Images[0].RootDeviceName, Ebs: &types.EbsBlockDevice{VolumeType: types.VolumeTypeGp3, VolumeSize: aws.Int32(cfg.VolumeSizeGiB), Iops: aws.Int32(6000), Throughput: aws.Int32(250), Encrypted: aws.Bool(true), DeleteOnTermination: aws.Bool(true)}}},
			TagSpecifications:   []types.TagSpecification{{ResourceType: types.ResourceTypeInstance, Tags: tags}, {ResourceType: types.ResourceTypeVolume, Tags: tags}},
		})
		if err != nil {
			return fmt.Errorf("launch EC2 instance: %w", err)
		}
		if len(launched.Instances) != 1 {
			return errors.New("expected one EC2 instance")
		}
		instanceID = aws.ToString(launched.Instances[0].InstanceId)
		state.InstanceID = instanceID
		if err := store.save(state); err != nil {
			return err
		}
	}
	log.Printf("instance=%s region=%s expires=%s", instanceID, cfg.Region, expiry.Format(time.RFC3339))
	if err := ec2.NewInstanceRunningWaiter(client).Wait(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}}, 10*time.Minute); err != nil {
		return err
	}
	described, err := client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}})
	if err != nil {
		return err
	}
	if len(described.Reservations) != 1 || len(described.Reservations[0].Instances) != 1 {
		return errors.New("instance missing after launch")
	}
	instance := described.Reservations[0].Instances[0]
	if instance.CpuOptions == nil || aws.ToInt32(instance.CpuOptions.CoreCount) != 16 || aws.ToInt32(instance.CpuOptions.ThreadsPerCore) != 1 {
		return errors.New("EC2 CPU options verification failed")
	}
	address := aws.ToString(instance.PublicIpAddress)
	if cfg.PrivateIP {
		address = aws.ToString(instance.PrivateIpAddress)
	}
	if address == "" {
		return errors.New("instance has no reachable IP address")
	}
	key, err := filepath.Abs(cfg.KeyPath)
	if err != nil {
		return err
	}
	host := remoteHost{address: address, key: key, knownHosts: filepath.Join(resultDir, "known_hosts")}
	sshCtx, stop := context.WithTimeout(ctx, 10*time.Minute)
	err = host.wait(sshCtx)
	stop()
	if err != nil {
		return err
	}
	if !state.Uploaded {
		if time.Now().After(state.ExpiresAt) {
			return errors.New("instance deadline expired before upload; destroy resources")
		}
		if err := host.ssh(ctx, "sudo cloud-init status --wait && mkdir -p /tmp/benchmark"); err != nil {
			return err
		}
		bundle, err := filepath.Abs(cfg.Bundle)
		if err != nil {
			return err
		}
		if err := host.copy(ctx, bundle+"/.", "/tmp/benchmark/", true); err != nil {
			return err
		}
		state.Uploaded = true
		if err := store.save(state); err != nil {
			return err
		}
	}
	return followRemote(ctx, store, state, host)
}

func instancesForRun(ctx context.Context, client *ec2.Client, runID string) ([]string, error) {
	out, err := client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{Filters: []types.Filter{
		{Name: aws.String("tag:Purpose"), Values: []string{purpose}},
		{Name: aws.String("tag:RunID"), Values: []string{runID}},
		{Name: aws.String("instance-state-name"), Values: []string{"pending", "running", "stopping", "stopped"}},
	}})
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, r := range out.Reservations {
		for _, i := range r.Instances {
			ids = append(ids, aws.ToString(i.InstanceId))
		}
	}
	return ids, nil
}

func terminate(ctx context.Context, client *ec2.Client, ids []string) error {
	log.Printf("terminating instances: %v", ids)
	if _, err := client.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: ids}); err != nil {
		return err
	}
	return ec2.NewInstanceTerminatedWaiter(client).Wait(ctx, &ec2.DescribeInstancesInput{InstanceIds: ids}, 8*time.Minute)
}

func expired(tags []types.Tag, now time.Time) bool {
	var matches bool
	var expiry time.Time
	for _, tag := range tags {
		switch aws.ToString(tag.Key) {
		case "Purpose":
			matches = aws.ToString(tag.Value) == purpose
		case "ExpiresAt":
			expiry, _ = time.Parse(time.RFC3339, aws.ToString(tag.Value))
		}
	}
	return matches && !expiry.IsZero() && !expiry.After(now)
}

func janitor(ctx context.Context, args []string) error {
	f := flag.NewFlagSet("janitor", flag.ContinueOnError)
	region := f.String("region", "", "AWS region (required)")
	apply := f.Bool("delete", false, "Terminate expired instances and delete expired SSH groups/key pairs; otherwise list only")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *region == "" {
		return errors.New("region is required")
	}
	client, err := awsClient(ctx, *region)
	if err != nil {
		return err
	}
	filters := []types.Filter{{Name: aws.String("tag:Purpose"), Values: []string{purpose}}}
	now := time.Now()
	pages := ec2.NewDescribeInstancesPaginator(client, &ec2.DescribeInstancesInput{Filters: filters})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, r := range page.Reservations {
			for _, i := range r.Instances {
				if !expired(i.Tags, now) || i.State == nil || i.State.Name == types.InstanceStateNameTerminated {
					continue
				}
				id := aws.ToString(i.InstanceId)
				log.Printf("expired instance: %s delete=%v", id, *apply)
				if *apply {
					if err := terminate(ctx, client, []string{id}); err != nil {
						return err
					}
				}
			}
		}
	}
	groups := ec2.NewDescribeSecurityGroupsPaginator(client, &ec2.DescribeSecurityGroupsInput{Filters: filters})
	for groups.HasMorePages() {
		page, err := groups.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, group := range page.SecurityGroups {
			if !expired(group.Tags, now) {
				continue
			}
			log.Printf("expired security group: %s delete=%v", aws.ToString(group.GroupId), *apply)
			if *apply {
				if _, err := client.DeleteSecurityGroup(ctx, &ec2.DeleteSecurityGroupInput{GroupId: group.GroupId}); err != nil {
					return err
				}
			}
		}
	}
	keys, err := client.DescribeKeyPairs(ctx, &ec2.DescribeKeyPairsInput{Filters: filters})
	if err != nil {
		return err
	}
	for _, key := range keys.KeyPairs {
		if !expired(key.Tags, now) {
			continue
		}
		log.Printf("expired key pair: %s delete=%v", aws.ToString(key.KeyName), *apply)
		if *apply {
			if _, err := client.DeleteKeyPair(ctx, &ec2.DeleteKeyPairInput{KeyPairId: key.KeyPairId}); err != nil {
				return err
			}
		}
	}
	return nil
}
