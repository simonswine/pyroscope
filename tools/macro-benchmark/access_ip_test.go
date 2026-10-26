package main

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

type accessFake struct {
	owner    string
	rules    []types.SecurityGroupRule
	modified *ec2.ModifySecurityGroupRulesInput
}

func (f *accessFake) DescribeSecurityGroups(context.Context, *ec2.DescribeSecurityGroupsInput, ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error) {
	return &ec2.DescribeSecurityGroupsOutput{SecurityGroups: []types.SecurityGroup{{Tags: []types.Tag{{Key: aws.String("Purpose"), Value: aws.String(purpose)}, {Key: aws.String("RunID"), Value: aws.String(f.owner)}}}}}, nil
}
func (f *accessFake) DescribeSecurityGroupRules(context.Context, *ec2.DescribeSecurityGroupRulesInput, ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupRulesOutput, error) {
	return &ec2.DescribeSecurityGroupRulesOutput{SecurityGroupRules: f.rules}, nil
}
func (f *accessFake) ModifySecurityGroupRules(_ context.Context, in *ec2.ModifySecurityGroupRulesInput, _ ...func(*ec2.Options)) (*ec2.ModifySecurityGroupRulesOutput, error) {
	f.modified = in
	return &ec2.ModifySecurityGroupRulesOutput{}, nil
}
func TestReplaceSSHSource(t *testing.T) {
	ssh := types.SecurityGroupRule{SecurityGroupRuleId: aws.String("sgr-managed"), IpProtocol: aws.String("tcp"), FromPort: aws.Int32(22), ToPort: aws.Int32(22), CidrIpv4: aws.String("192.0.2.1/32")}
	for _, test := range []struct {
		name, owner, old, current string
		rules                     []types.SecurityGroupRule
		wantError, wantModify     bool
	}{
		{"replace", "owner", "192.0.2.1/32", "192.0.2.2/32", []types.SecurityGroupRule{ssh, {IpProtocol: aws.String("tcp"), FromPort: aws.Int32(443), ToPort: aws.Int32(443), CidrIpv4: aws.String("0.0.0.0/0")}}, false, true},
		{"unchanged", "owner", "192.0.2.1/32", "192.0.2.1/32", []types.SecurityGroupRule{ssh}, false, false},
		{"retry", "owner", "192.0.2.2/32", "192.0.2.1/32", []types.SecurityGroupRule{ssh}, false, false},
		{"ownership mismatch", "other", "192.0.2.1/32", "192.0.2.2/32", []types.SecurityGroupRule{ssh}, true, false},
		{"custom network", "owner", "192.0.2.0/24", "192.0.2.2/32", []types.SecurityGroupRule{ssh}, true, false},
		{"missing rule", "owner", "192.0.2.1/32", "192.0.2.2/32", nil, true, false},
		{"broad target", "owner", "192.0.2.1/32", "0.0.0.0/0", []types.SecurityGroupRule{ssh}, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := &accessFake{owner: test.owner, rules: test.rules}
			err := replaceSSHSource(context.Background(), f, "owner", "sg-test", test.old, test.current)
			if (err != nil) != test.wantError {
				t.Fatalf("unexpected error: %v", err)
			}
			if (f.modified != nil) != test.wantModify {
				t.Fatalf("unexpected modification: %+v", f.modified)
			}
			if f.modified != nil {
				rules := f.modified.SecurityGroupRules
				if len(rules) != 1 || aws.ToString(rules[0].SecurityGroupRuleId) != "sgr-managed" || aws.ToString(rules[0].SecurityGroupRule.CidrIpv4) != test.current {
					t.Fatalf("wrong rule changed: %+v", rules)
				}
			}
		})
	}
}
