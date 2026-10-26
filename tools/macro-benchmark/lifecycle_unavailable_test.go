package main

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
)

type missingInstanceDiscovery struct{ fakeResourceDiscovery }

func (missingInstanceDiscovery) DescribeInstances(_ context.Context, input *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	if len(input.InstanceIds) == 0 {
		return &ec2.DescribeInstancesOutput{}, nil
	}
	return nil, &smithy.GenericAPIError{Code: "InvalidInstanceID.NotFound"}
}

func TestReconcileUnavailableInstance(t *testing.T) {
	tags := []types.Tag{{Key: aws.String("Purpose"), Value: aws.String(purpose)}, {Key: aws.String("RunID"), Value: aws.String("test")}}
	for _, phase := range []string{"running", "destroying"} {
		for _, status := range []types.InstanceStateName{types.InstanceStateNameTerminated, types.InstanceStateNameShuttingDown} {
			t.Run(phase+"/"+string(status), func(t *testing.T) {
				state := &sessionState{Config: runConfig{RunID: "test"}, InstanceID: "i-gone", Phase: phase}
				err := reconcileResources(context.Background(), fakeResourceDiscovery{instances: []types.Instance{{InstanceId: aws.String("i-gone"), Tags: tags, State: &types.InstanceState{Name: status}}}}, state)
				if phase == "destroying" {
					if err != nil {
						t.Fatal(err)
					}
				} else if !errors.Is(err, errInstanceUnavailable) {
					t.Fatalf("unexpected error: %v", err)
				}
			})
		}
		t.Run(phase+"/not-found", func(t *testing.T) {
			state := &sessionState{Config: runConfig{RunID: "test"}, InstanceID: "i-gone", Phase: phase}
			err := reconcileResources(context.Background(), missingInstanceDiscovery{}, state)
			if phase == "destroying" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, errInstanceUnavailable) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestSessionActionUnavailableInstance(t *testing.T) {
	store, err := openStateStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state, err := store.create("checkoutservice")
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"running", "completed", "destroying"} {
		state.Phase = phase
		err := sessionAction(context.Background(), store, state, func(context.Context, *stateStore, *sessionState) error { return errInstanceUnavailable })
		if !errors.Is(err, errInstanceUnavailable) {
			t.Fatal(err)
		}
		want := phase
		if phase == "running" {
			want = "interrupted"
		}
		if state.Phase != want || state.Error != errInstanceUnavailable.Error() {
			t.Fatalf("unexpected state: %s, %s", state.Phase, state.Error)
		}
	}
}
