package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

type sshAccessAPI interface {
	DescribeSecurityGroups(context.Context, *ec2.DescribeSecurityGroupsInput, ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error)
	DescribeSecurityGroupRules(context.Context, *ec2.DescribeSecurityGroupRulesInput, ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupRulesOutput, error)
	ModifySecurityGroupRules(context.Context, *ec2.ModifySecurityGroupRulesInput, ...func(*ec2.Options)) (*ec2.ModifySecurityGroupRulesOutput, error)
}

// Replace only the exact SSH rule recorded by the controller. Never widen an
// existing private-network CIDR or alter unrelated ingress rules.
func replaceSSHSource(ctx context.Context, client sshAccessAPI, owner, groupID, previous, current string) error {
	for _, cidr := range []string{previous, current} {
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil || !prefix.Addr().Is4() || prefix.Bits() != 32 {
			return errors.New("automatic SSH IP updates require IPv4 /32 source rules")
		}
	}
	groups, err := client.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{GroupIds: []string{groupID}})
	if err != nil {
		return err
	}
	if len(groups.SecurityGroups) != 1 || !ownedResource(groups.SecurityGroups[0].Tags, owner) {
		return errors.New("SSH security group ownership mismatch")
	}
	var oldIDs []string
	alreadyCurrent := false
	var token *string
	for {
		page, err := client.DescribeSecurityGroupRules(ctx, &ec2.DescribeSecurityGroupRulesInput{Filters: []types.Filter{{Name: aws.String("group-id"), Values: []string{groupID}}}, NextToken: token})
		if err != nil {
			return err
		}
		for _, rule := range page.SecurityGroupRules {
			if aws.ToBool(rule.IsEgress) || aws.ToString(rule.IpProtocol) != "tcp" || rule.FromPort == nil || rule.ToPort == nil || *rule.FromPort != 22 || *rule.ToPort != 22 {
				continue
			}
			if aws.ToString(rule.CidrIpv4) == previous {
				oldIDs = append(oldIDs, aws.ToString(rule.SecurityGroupRuleId))
			}
			if aws.ToString(rule.CidrIpv4) == current {
				alreadyCurrent = true
			}
		}
		token = page.NextToken
		if token == nil || *token == "" {
			break
		}
	}
	if previous == current && alreadyCurrent {
		return nil
	}
	if len(oldIDs) == 0 && alreadyCurrent {
		return nil
	} // retry after AWS succeeded but local persistence failed
	if len(oldIDs) != 1 || oldIDs[0] == "" {
		return errors.New("expected exactly one previous managed SSH source rule; no rules changed")
	}
	if alreadyCurrent {
		return errors.New("current IP already has a separate SSH rule; reconcile rules manually")
	}
	_, err = client.ModifySecurityGroupRules(ctx, &ec2.ModifySecurityGroupRulesInput{GroupId: aws.String(groupID), SecurityGroupRules: []types.SecurityGroupRuleUpdate{{SecurityGroupRuleId: aws.String(oldIDs[0]), SecurityGroupRule: &types.SecurityGroupRuleRequest{IpProtocol: aws.String("tcp"), FromPort: aws.Int32(22), ToPort: aws.Int32(22), CidrIpv4: aws.String(current)}}}})
	return err
}

func updateSessionAccessIP(ctx context.Context, store *stateStore, selected *sessionState) error {
	if selected.Config.PrivateIP {
		return errors.New("automatic public IP updates are unavailable for private-IP sessions")
	}
	states, err := store.list()
	if err != nil {
		return err
	}
	owner := *selected
	if selected.Kind == "rerun" {
		found := false
		for _, state := range states {
			if state.Config.RunID == selected.StorageRunID && state.Kind != "rerun" && state.InstanceID == selected.InstanceID && state.Config.Region == selected.Config.Region {
				owner = state
				found = true
				break
			}
		}
		if !found {
			return errors.New("storage owner unavailable")
		}
	}
	if owner.InstanceID == "" || owner.GroupID == "" || owner.Phase == "destroying" || owner.Phase == "destroyed" {
		return errors.New("session has no eligible instance/security group")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return dialer.DialContext(ctx, "tcp4", address)
	}
	defer transport.CloseIdleConnections()
	current, err := publicIPv4(ctx, &http.Client{Transport: transport, Timeout: 15 * time.Second}, publicIPWebsite)
	if err != nil {
		return fmt.Errorf("discover SSH source address: %w", err)
	}
	client, err := awsClient(ctx, owner.Config.Region)
	if err != nil {
		return err
	}
	if err := replaceSSHSource(ctx, client, owner.Config.RunID, owner.GroupID, owner.Config.SSHCIDR, current); err != nil {
		return err
	}
	// Children share access to the owner's instance. Persist all their snapshots,
	// without changing run phases or requiring an SSH connection.
	for _, state := range states {
		if state.InstanceID == owner.InstanceID && state.Config.Region == owner.Config.Region {
			state.Config.SSHCIDR = current
			if err := store.save(&state); err != nil {
				return err
			}
		}
	}
	selected.Config.SSHCIDR = current
	return nil
}
