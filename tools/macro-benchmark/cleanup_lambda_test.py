import importlib.util
import os
from pathlib import Path
import sys
import types
import unittest
from datetime import datetime, timedelta, timezone
from unittest.mock import MagicMock, patch

# Keep tests runnable without installing the AWS Python SDK locally.
boto3 = types.ModuleType("boto3")
boto3.client = MagicMock()
sys.modules["boto3"] = boto3
exceptions = types.ModuleType("botocore.exceptions")


class ClientError(Exception):
    def __init__(self, code):
        self.response = {"Error": {"Code": code}}


exceptions.ClientError = ClientError
sys.modules["botocore.exceptions"] = exceptions
spec = importlib.util.spec_from_file_location("cleanup", Path(__file__).with_name("cleanup_lambda.py"))
cleanup = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cleanup)


class CleanupTest(unittest.TestCase):
    def setUp(self):
        self.clients = {name: MagicMock() for name in ("ec2", "events", "lambda", "iam")}
        boto3.client.side_effect = self.clients.__getitem__
        self.ec2 = self.clients["ec2"]
        self.instance = {"InstanceId": "i-test", "State": {"Name": "running"},
                         "LaunchTime": datetime.now(timezone.utc) - timedelta(hours=33)}
        self.env = patch.dict(os.environ, {
            "RUN_ID": "test", "CLEANUP_NAME": "test-cleanup",
            "ORPHAN_DEADLINE": (datetime.now(timezone.utc) - timedelta(hours=33)).isoformat(),
        })
        self.env.start()
        self.addCleanup(self.env.stop)
        self.ec2.get_paginator.side_effect = lambda operation: types.SimpleNamespace(
            paginate=lambda **kwargs: [{"Reservations": [{"Instances": [self.instance]}]}]
            if operation == "describe_instances" else [{"SecurityGroups": [{"GroupId": "sg-test"}]}])
        self.ec2.describe_key_pairs.return_value = {"KeyPairs": [{"KeyPairId": "key-test"}]}
        self.clients["events"].remove_targets.return_value = {"FailedEntryCount": 0}

    def test_does_not_destroy_young_host(self):
        self.instance["LaunchTime"] = datetime.now(timezone.utc) - timedelta(hours=31)
        cleanup.handler({}, None)
        self.ec2.terminate_instances.assert_not_called()
        self.clients["lambda"].delete_function.assert_not_called()

    def test_terminates_expired_host_before_cleanup(self):
        cleanup.handler({}, None)
        self.ec2.terminate_instances.assert_called_once_with(InstanceIds=["i-test"])
        self.ec2.delete_security_group.assert_not_called()
        self.clients["events"].delete_rule.assert_not_called()

    def test_terminated_host_cleans_everything_even_before_deadline(self):
        self.instance["State"]["Name"] = "terminated"
        self.instance["LaunchTime"] = datetime.now(timezone.utc)
        cleanup.handler({}, None)
        self.ec2.delete_security_group.assert_called_once_with(GroupId="sg-test")
        self.ec2.delete_key_pair.assert_called_once_with(KeyPairId="key-test")
        self.clients["events"].delete_rule.assert_called_once_with(Name="test-cleanup")
        self.clients["lambda"].delete_function.assert_called_once_with(FunctionName="test-cleanup")
        self.clients["iam"].delete_role.assert_called_once_with(RoleName="test-cleanup")

    def test_group_dependency_retains_retry_trigger(self):
        self.instance["State"]["Name"] = "terminated"
        self.ec2.delete_security_group.side_effect = ClientError("DependencyViolation")
        with self.assertRaises(ClientError):
            cleanup.handler({}, None)
        self.clients["events"].remove_targets.assert_not_called()
        self.clients["lambda"].delete_function.assert_not_called()

    def test_missing_group_is_idempotent(self):
        self.instance["State"]["Name"] = "terminated"
        self.ec2.delete_security_group.side_effect = ClientError("InvalidGroup.NotFound")
        cleanup.handler({}, None)
        self.clients["lambda"].delete_function.assert_called_once()

    def test_orphan_cleanup(self):
        self.ec2.get_paginator.side_effect = lambda operation: types.SimpleNamespace(
            paginate=lambda **kwargs: [{"Reservations": []}] if operation == "describe_instances"
            else [{"SecurityGroups": [{"GroupId": "sg-test"}]}])
        os.environ["ORPHAN_DEADLINE"] = (datetime.now(timezone.utc) + timedelta(hours=1)).isoformat()
        cleanup.handler({}, None)
        self.ec2.delete_security_group.assert_not_called()
        os.environ["ORPHAN_DEADLINE"] = (datetime.now(timezone.utc) - timedelta(hours=1)).isoformat()
        cleanup.handler({}, None)
        self.clients["lambda"].delete_function.assert_called_once()


if __name__ == "__main__":
    unittest.main()
