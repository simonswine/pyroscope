package main

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

type fakeResourceDiscovery struct {
	instances []types.Instance
	groups    []types.SecurityGroup
}

func (f fakeResourceDiscovery) DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	return &ec2.DescribeInstancesOutput{Reservations: []types.Reservation{{Instances: f.instances}}}, nil
}
func (f fakeResourceDiscovery) DescribeSecurityGroups(context.Context, *ec2.DescribeSecurityGroupsInput, ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error) {
	return &ec2.DescribeSecurityGroupsOutput{SecurityGroups: f.groups}, nil
}

func TestReconcileRecoversLostAWSResponses(t *testing.T) {
	tags := []types.Tag{{Key: aws.String("Purpose"), Value: aws.String(purpose)}, {Key: aws.String("RunID"), Value: aws.String("test")}}
	for _, phase := range []types.InstanceStateName{types.InstanceStateNameRunning, types.InstanceStateNameTerminated} {
		state := &sessionState{Config: runConfig{RunID: "test"}}
		client := fakeResourceDiscovery{
			instances: []types.Instance{{InstanceId: aws.String("i-existing"), Tags: tags, State: &types.InstanceState{Name: phase}}},
			groups:    []types.SecurityGroup{{GroupId: aws.String("sg-existing"), Tags: tags}},
		}
		if err := reconcileResources(context.Background(), client, state); err != nil {
			t.Fatal(err)
		}
		if state.InstanceID != "i-existing" || state.GroupID != "sg-existing" {
			t.Fatal("failed to recover resource IDs")
		}
		// Terminated instances must also be recovered, never replaced on resume.
		if err := reconcileResources(context.Background(), client, state); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReconcileRejectsAmbiguousOrUnownedResources(t *testing.T) {
	tags := []types.Tag{{Key: aws.String("Purpose"), Value: aws.String(purpose)}, {Key: aws.String("RunID"), Value: aws.String("test")}}
	instance := types.Instance{InstanceId: aws.String("i-owned"), Tags: tags}
	for _, test := range []struct {
		name    string
		client  fakeResourceDiscovery
		knownID string
	}{
		{"ambiguous", fakeResourceDiscovery{instances: []types.Instance{instance, instance}}, ""},
		{"unowned", fakeResourceDiscovery{instances: []types.Instance{{InstanceId: aws.String("i-other")}}}, ""},
		{"persisted-unowned", fakeResourceDiscovery{instances: []types.Instance{{InstanceId: aws.String("i-other")}}}, "i-other"},
		{"mismatched-id", fakeResourceDiscovery{instances: []types.Instance{instance}}, "i-stale"},
		{"unowned-group", fakeResourceDiscovery{groups: []types.SecurityGroup{{GroupId: aws.String("sg-other")}}}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := &sessionState{Config: runConfig{RunID: "test"}, InstanceID: test.knownID}
			if err := reconcileResources(context.Background(), test.client, state); err == nil {
				t.Fatal("accepted unsafe recovery")
			}
		})
	}
}

func TestDestroyDraftNeedsNoAWS(t *testing.T) {
	store, err := openStateStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state, err := store.create("checkoutservice")
	if err != nil {
		t.Fatal(err)
	}
	if err := destroySession(context.Background(), store, state); err != nil {
		t.Fatal(err)
	}
	if state.Phase != "destroyed" {
		t.Fatal("not destroyed")
	}
	if err := resumeSession(context.Background(), store, state); err == nil {
		t.Fatal("resumed destroyed session")
	}
}
