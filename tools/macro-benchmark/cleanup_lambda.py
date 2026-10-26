"""Per-session reaper. Retries via EventBridge until EC2 cleanup succeeds."""
import os
from datetime import datetime, timedelta, timezone

import boto3
from botocore.exceptions import ClientError

PURPOSE = "pyroscope-macro-benchmark"


def ignore_missing(client, method, missing, **kwargs):
    try:
        return getattr(client, method)(**kwargs)
    except ClientError as error:
        if error.response["Error"]["Code"] not in missing:
            raise


def handler(event, context):
    run = os.environ["RUN_ID"]
    name = os.environ["CLEANUP_NAME"]
    ec2 = boto3.client("ec2")
    filters = [{"Name": "tag:Purpose", "Values": [PURPOSE]},
               {"Name": "tag:RunID", "Values": [run]}]
    instances = []
    for page in ec2.get_paginator("describe_instances").paginate(Filters=filters):
        for reservation in page["Reservations"]:
            instances.extend(reservation["Instances"])
    now = datetime.now(timezone.utc)
    live = [i for i in instances if i["State"]["Name"] != "terminated"]
    # Use EC2's immutable launch timestamp, never controller reconnect time.
    if any(now < i["LaunchTime"] + timedelta(hours=32) for i in live):
        return
    if not instances and now < datetime.fromisoformat(os.environ["ORPHAN_DEADLINE"]):
        return
    ids = [i["InstanceId"] for i in live]
    if ids:
        ec2.terminate_instances(InstanceIds=ids)
        # A subsequent scheduled invocation handles termination/ENI delays.
        return
    for page in ec2.get_paginator("describe_security_groups").paginate(Filters=filters):
        for group in page["SecurityGroups"]:
            ignore_missing(ec2, "delete_security_group", ["InvalidGroup.NotFound"],
                           GroupId=group["GroupId"])
    for key in ec2.describe_key_pairs(Filters=filters)["KeyPairs"]:
        ignore_missing(ec2, "delete_key_pair", ["InvalidKeyPair.NotFound"],
                       KeyPairId=key["KeyPairId"])
    # Only remove the retry trigger after all EC2 resources are gone.
    events = boto3.client("events")
    result = events.remove_targets(Rule=name, Ids=["cleanup"])
    if result.get("FailedEntryCount", 0):
        raise RuntimeError(result["FailedEntries"])
    ignore_missing(events, "delete_rule", ["ResourceNotFoundException"], Name=name)
    ignore_missing(boto3.client("lambda"), "delete_function",
                   ["ResourceNotFoundException"], FunctionName=name)
    # Keep execution permissions until the function and trigger are deleted.
    iam = boto3.client("iam")
    ignore_missing(iam, "delete_role_policy", ["NoSuchEntity"],
                   RoleName=name, PolicyName=name)
    ignore_missing(iam, "delete_role", ["NoSuchEntity"], RoleName=name)
