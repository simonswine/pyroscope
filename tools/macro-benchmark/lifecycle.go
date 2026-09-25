package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
)

func awsErrorCode(err error, code string) bool {
	var api smithy.APIError
	return errors.As(err, &api) && api.ErrorCode() == code
}

func ownedResource(tags []types.Tag, runID string) bool {
	var owner, run string
	for _, tag := range tags {
		switch aws.ToString(tag.Key) {
		case "Purpose":
			owner = aws.ToString(tag.Value)
		case "RunID":
			run = aws.ToString(tag.Value)
		}
	}
	return owner == purpose && run == runID
}

type resourceDiscovery interface {
	DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
	DescribeSecurityGroups(context.Context, *ec2.DescribeSecurityGroupsInput, ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error)
}

// Tags recover responses lost between AWS committing a request and our fsync.
// Include terminated instances: never silently replace a lost benchmark host.
func reconcileResources(ctx context.Context, client resourceDiscovery, state *sessionState) error {
	// Never trust a persisted ID alone when performing destructive operations.
	// A missing resource is acceptable only while finishing idempotent cleanup.
	if state.InstanceID != "" {
		out, err := client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{state.InstanceID}})
		if err != nil {
			if state.Phase != "destroying" || !awsErrorCode(err, "InvalidInstanceID.NotFound") {
				return err
			}
		} else if len(out.Reservations) != 1 || len(out.Reservations[0].Instances) != 1 || !ownedResource(out.Reservations[0].Instances[0].Tags, state.Config.RunID) {
			return errors.New("persisted instance ownership mismatch")
		}
	}
	if state.GroupID != "" {
		out, err := client.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{GroupIds: []string{state.GroupID}})
		if err != nil {
			if state.Phase != "destroying" || !awsErrorCode(err, "InvalidGroup.NotFound") {
				return err
			}
		} else if len(out.SecurityGroups) != 1 || !ownedResource(out.SecurityGroups[0].Tags, state.Config.RunID) {
			return errors.New("persisted security group ownership mismatch")
		}
	}
	filters := []types.Filter{{Name: aws.String("tag:Purpose"), Values: []string{purpose}}, {Name: aws.String("tag:RunID"), Values: []string{state.Config.RunID}}}
	input := &ec2.DescribeInstancesInput{Filters: filters}
	var ids []string
	for {
		out, err := client.DescribeInstances(ctx, input)
		if err != nil {
			return err
		}
		for _, reservation := range out.Reservations {
			for _, instance := range reservation.Instances {
				if !ownedResource(instance.Tags, state.Config.RunID) {
					return errors.New("instance ownership mismatch")
				}
				ids = append(ids, aws.ToString(instance.InstanceId))
			}
		}
		if aws.ToString(out.NextToken) == "" {
			break
		}
		input.NextToken = out.NextToken
	}
	if len(ids) > 1 {
		return errors.New("multiple instances found for session; refusing to choose")
	}
	if len(ids) == 1 {
		if state.InstanceID != "" && state.InstanceID != ids[0] {
			return errors.New("instance ID mismatch")
		}
		state.InstanceID = ids[0]
	}
	groupInput := &ec2.DescribeSecurityGroupsInput{Filters: filters}
	var groups []string
	for {
		out, err := client.DescribeSecurityGroups(ctx, groupInput)
		if err != nil {
			return err
		}
		for _, group := range out.SecurityGroups {
			if !ownedResource(group.Tags, state.Config.RunID) {
				return errors.New("security group ownership mismatch")
			}
			groups = append(groups, aws.ToString(group.GroupId))
		}
		if aws.ToString(out.NextToken) == "" {
			break
		}
		groupInput.NextToken = out.NextToken
	}
	if len(groups) > 1 {
		return errors.New("multiple security groups found for session")
	}
	if len(groups) == 1 {
		if state.GroupID != "" && state.GroupID != groups[0] {
			return errors.New("security group ID mismatch")
		}
		state.GroupID = groups[0]
	}
	return nil
}

func ensureAWSKey(ctx context.Context, client *ec2.Client, name, runID string, public []byte, tags []types.Tag) error {
	out, err := client.DescribeKeyPairs(ctx, &ec2.DescribeKeyPairsInput{KeyNames: []string{name}, IncludePublicKey: aws.Bool(true)})
	if err == nil {
		if len(out.KeyPairs) != 1 || !ownedResource(out.KeyPairs[0].Tags, runID) {
			return errors.New("key pair ownership mismatch")
		}
		// AWS may omit the comment from an imported OpenSSH key.
		want, got := strings.Fields(string(public)), strings.Fields(aws.ToString(out.KeyPairs[0].PublicKey))
		if len(want) < 2 || len(got) < 2 || want[0] != got[0] || want[1] != got[1] {
			return errors.New("AWS key pair differs from local SSH key")
		}
		return nil
	}
	if !awsErrorCode(err, "InvalidKeyPair.NotFound") {
		return err
	}
	_, err = client.ImportKeyPair(ctx, &ec2.ImportKeyPairInput{KeyName: aws.String(name), PublicKeyMaterial: public, TagSpecifications: []types.TagSpecification{{ResourceType: types.ResourceTypeKeyPair, Tags: tags}}})
	return err
}

func prepareSession(ctx context.Context, store *stateStore, state *sessionState) error {
	if state.Phase != "draft" && state.Phase != "preparing" {
		return errors.New("only draft sessions can be prepared")
	}
	state.Phase = "preparing"
	if err := store.save(state); err != nil {
		return err
	}
	cfg := &state.Config
	if err := completeConfig(ctx, cfg); err != nil {
		return err
	}
	// Persist discovered identity/network settings before building. A cancelled
	// preparation can safely rebuild without selecting a new host configuration.
	if err := store.save(state); err != nil {
		return err
	}
	resolved := cfg.Inputs
	if err := resolved.resolveDataset(); err != nil {
		return err
	}
	resolved.defaults()
	if state.Plan == nil {
		plan, err := makeRunPlan(ctx, state.SourceDir, resolved)
		if err != nil {
			return err
		}
		state.Plan = plan
		if err := store.save(state); err != nil {
			return err
		}
	}
	buildConfig := *cfg
	buildConfig.Inputs = resolved
	if err := prepare(ctx, buildConfig, state.SourceDir, state.Plan); err != nil {
		return err
	}
	cfg.Inputs = resolved
	state.Phase = "prepared"
	return store.save(state)
}

func sessionHost(ctx context.Context, state *sessionState) (remoteHost, error) {
	if state.InstanceID == "" {
		return remoteHost{}, errors.New("no instance yet; resume provisioning first")
	}
	client, err := awsClient(ctx, state.Config.Region)
	if err != nil {
		return remoteHost{}, err
	}
	out, err := client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{state.InstanceID}})
	if err != nil {
		return remoteHost{}, err
	}
	if len(out.Reservations) != 1 || len(out.Reservations[0].Instances) != 1 {
		return remoteHost{}, errors.New("instance missing")
	}
	instance := out.Reservations[0].Instances[0]
	ownerID := state.Config.RunID
	if state.Kind == "rerun" {
		if !validSessionID(state.StorageRunID) || state.StorageRunID == state.Config.RunID {
			return remoteHost{}, errors.New("invalid rerun storage owner")
		}
		ownerID = state.StorageRunID
	}
	if !ownedResource(instance.Tags, ownerID) {
		return remoteHost{}, errors.New("instance ownership mismatch")
	}
	address := aws.ToString(instance.PublicIpAddress)
	if state.Config.PrivateIP {
		address = aws.ToString(instance.PrivateIpAddress)
	}
	if address == "" {
		return remoteHost{}, errors.New("instance has no reachable address")
	}
	return remoteHost{address: address, key: state.Config.KeyPath, knownHosts: filepath.Join(state.Config.Results, ownerID, "known_hosts")}, nil
}

func collectSession(ctx context.Context, store *stateStore, state *sessionState) error {
	return collectManaged(ctx, store, state)
}

func stopSession(ctx context.Context, store *stateStore, state *sessionState) error {
	if !state.StartRequested {
		return errors.New("remote worker has not been submitted")
	}
	client, err := store.agentForSession(ctx, state)
	if err != nil {
		return err
	}
	state.Phase = "stopping"
	if err := store.save(state); err != nil {
		return err
	}
	var run agentRunSummary
	if err := client.Request(ctx, "stop_run", state.Config.RunID, nil, &run); err != nil {
		return err
	}
	state.Phase = run.Phase
	state.Error = run.Error
	return store.save(state)
}

func destroySession(ctx context.Context, store *stateStore, state *sessionState) error {
	if state.Kind == "rerun" {
		return errors.New("child destruction is not supported; use s to stop and retain its artifacts; only the owner can destroy AWS")
	}
	states, err := store.list()
	if err != nil {
		return err
	}
	for _, child := range states {
		if child.Kind == "rerun" && child.StorageRunID == state.Config.RunID && activePhase(child.Phase) {
			return fmt.Errorf("child %s is active; stop it before destroying the owner", child.Config.RunID)
		}
	}
	state.Phase = "destroying"
	if err := store.save(state); err != nil {
		return err
	}
	// Drafts have no AWS side effects, and may not even have a region yet.
	if state.KeyName == "" {
		state.Phase = "destroyed"
		return store.save(state)
	}
	client, err := awsClient(ctx, state.Config.Region)
	if err != nil {
		return err
	}
	if err := reconcileResources(ctx, client, state); err != nil {
		return err
	}
	if err := store.save(state); err != nil {
		return err
	}
	if state.InstanceID != "" {
		err := terminate(ctx, client, []string{state.InstanceID})
		if err != nil && !awsErrorCode(err, "InvalidInstanceID.NotFound") {
			return err
		}
	}
	if state.GroupID != "" {
		_, err := client.DeleteSecurityGroup(ctx, &ec2.DeleteSecurityGroupInput{GroupId: aws.String(state.GroupID)})
		if err != nil && !awsErrorCode(err, "InvalidGroup.NotFound") {
			return err
		}
	}
	keys, err := client.DescribeKeyPairs(ctx, &ec2.DescribeKeyPairsInput{KeyNames: []string{state.KeyName}})
	if err != nil && !awsErrorCode(err, "InvalidKeyPair.NotFound") {
		return err
	}
	if err == nil {
		if len(keys.KeyPairs) != 1 || !ownedResource(keys.KeyPairs[0].Tags, state.Config.RunID) {
			return errors.New("key pair ownership mismatch")
		}
		_, err = client.DeleteKeyPair(ctx, &ec2.DeleteKeyPairInput{KeyName: aws.String(state.KeyName)})
		if err != nil && !awsErrorCode(err, "InvalidKeyPair.NotFound") {
			return err
		}
	}
	state.Phase = "destroyed"
	return store.save(state)
}

func resumeSession(ctx context.Context, store *stateStore, state *sessionState) error {
	if state.Kind == "rerun" {
		switch state.Phase {
		case "draft", "preparing":
			return prepareRerunSession(ctx, store, state)
		case "prepared", "uploading", "ready", "starting", "running":
			return followManaged(ctx, store, state)
		case "completed", "failed", "stopped", "interrupted":
			return collectSession(ctx, store, state)
		case "stopping":
			return stopSession(ctx, store, state)
		default:
			return fmt.Errorf("unsupported child phase %q", state.Phase)
		}
	}
	switch state.Phase {
	case "draft", "preparing":
		return prepareSession(ctx, store, state)
	case "destroying":
		return destroySession(ctx, store, state)
	case "destroyed":
		return errors.New("session has been destroyed")
	case "stopping":
		return stopSession(ctx, store, state)
	case "completed", "failed", "stopped", "interrupted":
		return collectSession(ctx, store, state)
	case "prepared", "provisioning", "uploading", "ready", "starting", "running":
		return resumeAWS(ctx, store, state)
	default:
		return fmt.Errorf("unknown session phase %q", state.Phase)
	}
}

// A cancelled controller only stops local work. Keep the checkpoint intact.
func sessionAction(ctx context.Context, store *stateStore, state *sessionState, action func(context.Context, *stateStore, *sessionState) error) error {
	state.Error = ""
	if err := store.save(state); err != nil {
		return err
	}
	err := action(ctx, store, state)
	if err != nil && !errors.Is(err, context.Canceled) {
		state.Error = err.Error()
		log.Printf("session=%s phase=%s error=%v", state.Config.RunID, state.Phase, err)
	}
	return errors.Join(err, store.save(state))
}

func waitPoll(ctx context.Context, interval time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(interval):
		return nil
	}
}
