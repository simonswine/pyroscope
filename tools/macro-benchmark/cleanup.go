package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	eventtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
)

const instanceLifetime = 32 * time.Hour

//go:embed cleanup_lambda.py
var cleanupSource []byte

func cleanupName(runID string) string {
	return fmt.Sprintf("pyroscope-macro-cleanup-%x", sha256.Sum256([]byte(runID)))[:55]
}

func cleanupArchive() ([]byte, error) {
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	file, err := archive.Create("cleanup_lambda.py")
	if err != nil {
		return nil, err
	}
	if _, err := file.Write(cleanupSource); err != nil {
		return nil, err
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// Install before creating EC2 resources so a disconnected controller cannot
// leave a host without a reaper. Names are deterministic for crash recovery.
func ensureCleanup(ctx context.Context, state *sessionState) error {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(state.Config.Region))
	if err != nil {
		return err
	}
	name := cleanupName(state.Config.RunID)
	roles := iam.NewFromConfig(cfg)
	trust := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}`
	role, err := roles.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(name)})
	if awsErrorCode(err, "NoSuchEntity") {
		created, createErr := roles.CreateRole(ctx, &iam.CreateRoleInput{
			RoleName: aws.String(name), AssumeRolePolicyDocument: aws.String(trust),
			Tags: []iamtypes.Tag{{Key: aws.String("Purpose"), Value: aws.String(purpose)}, {Key: aws.String("RunID"), Value: aws.String(state.Config.RunID)}},
		})
		if createErr != nil {
			return createErr
		}
		role = &iam.GetRoleOutput{Role: created.Role}
		err = nil
	}
	if err != nil {
		return err
	}
	roleTags := make(map[string]string)
	for _, tag := range role.Role.Tags {
		roleTags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	if roleTags["Purpose"] != purpose || roleTags["RunID"] != state.Config.RunID {
		return fmt.Errorf("cleanup role ownership mismatch: %s", name)
	}
	// Derive partition and account from IAM's ARN (also works in GovCloud).
	roleARN := aws.ToString(role.Role.Arn)
	var partition, account string
	parts := strings.SplitN(roleARN, ":", 6)
	if len(parts) != 6 {
		return fmt.Errorf("invalid cleanup role ARN: %s", roleARN)
	}
	partition, account = parts[1], parts[4]
	base := "arn:" + partition + ":"
	functionARN := base + "lambda:" + cfg.Region + ":" + account + ":function:" + name
	ruleARN := base + "events:" + cfg.Region + ":" + account + ":rule/" + name
	statement := func(actions []string, resources any) map[string]any {
		return map[string]any{"Effect": "Allow", "Action": actions, "Resource": resources}
	}
	mutate := statement([]string{"ec2:TerminateInstances", "ec2:DeleteSecurityGroup", "ec2:DeleteKeyPair"}, "*")
	mutate["Condition"] = map[string]any{"StringEquals": map[string]string{"ec2:ResourceTag/Purpose": purpose, "ec2:ResourceTag/RunID": state.Config.RunID}}
	policy, err := json.Marshal(map[string]any{"Version": "2012-10-17", "Statement": []any{
		statement([]string{"ec2:DescribeInstances", "ec2:DescribeSecurityGroups", "ec2:DescribeKeyPairs"}, "*"), mutate,
		statement([]string{"lambda:DeleteFunction"}, functionARN),
		statement([]string{"events:RemoveTargets", "events:DeleteRule"}, ruleARN),
		statement([]string{"iam:DeleteRolePolicy", "iam:DeleteRole"}, roleARN),
	}})
	if err != nil {
		return err
	}
	if _, err := roles.PutRolePolicy(ctx, &iam.PutRolePolicyInput{RoleName: aws.String(name), PolicyName: aws.String(name), PolicyDocument: aws.String(string(policy))}); err != nil {
		return err
	}
	functions := lambda.NewFromConfig(cfg)
	existing, err := functions.GetFunction(ctx, &lambda.GetFunctionInput{FunctionName: aws.String(name)})
	if err == nil && (existing.Tags["Purpose"] != purpose || existing.Tags["RunID"] != state.Config.RunID) {
		return fmt.Errorf("cleanup function ownership mismatch: %s", name)
	}
	if awsErrorCode(err, "ResourceNotFoundException") {
		code, zipErr := cleanupArchive()
		if zipErr != nil {
			return zipErr
		}
		// IAM trust propagation can take several seconds.
		for attempt := 0; attempt < 12; attempt++ {
			_, err = functions.CreateFunction(ctx, &lambda.CreateFunctionInput{
				FunctionName: aws.String(name), Role: aws.String(roleARN), Runtime: lambdatypes.RuntimePython312,
				Handler: aws.String("cleanup_lambda.handler"), Timeout: aws.Int32(60),
				Code: &lambdatypes.FunctionCode{ZipFile: code},
				Environment: &lambdatypes.Environment{Variables: map[string]string{
					"RUN_ID": state.Config.RunID, "CLEANUP_NAME": name,
					"ORPHAN_DEADLINE": time.Now().UTC().Add(instanceLifetime).Format(time.RFC3339),
				}}, Tags: map[string]string{"Purpose": purpose, "RunID": state.Config.RunID},
			})
			if !awsErrorCode(err, "InvalidParameterValueException") {
				break
			}
			if err := waitPoll(ctx, 5*time.Second); err != nil {
				return err
			}
		}
	}
	if err != nil {
		return err
	}
	if err := lambda.NewFunctionActiveV2Waiter(functions).Wait(ctx, &lambda.GetFunctionInput{FunctionName: aws.String(name)}, 2*time.Minute); err != nil {
		return err
	}
	events := eventbridge.NewFromConfig(cfg)
	if _, err := events.PutRule(ctx, &eventbridge.PutRuleInput{Name: aws.String(name), ScheduleExpression: aws.String("rate(1 minute)"), State: eventtypes.RuleStateEnabled}); err != nil {
		return err
	}
	_, err = functions.AddPermission(ctx, &lambda.AddPermissionInput{FunctionName: aws.String(name), StatementId: aws.String("schedule"), Action: aws.String("lambda:InvokeFunction"), Principal: aws.String("events.amazonaws.com"), SourceArn: aws.String(ruleARN), SourceAccount: aws.String(account)})
	if err != nil && !awsErrorCode(err, "ResourceConflictException") {
		return err
	}
	out, err := events.PutTargets(ctx, &eventbridge.PutTargetsInput{Rule: aws.String(name), Targets: []eventtypes.Target{{Id: aws.String("cleanup"), Arn: aws.String(functionARN)}}})
	if err != nil {
		return err
	}
	if out.FailedEntryCount != 0 {
		return fmt.Errorf("install cleanup target: %v", out.FailedEntries)
	}
	return nil
}
