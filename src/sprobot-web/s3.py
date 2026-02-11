from __future__ import annotations

import json
import os
from typing import Dict, Optional

import boto3
import botocore.exceptions


class S3Client:
    def __init__(self) -> None:
        self.bucket = os.environ.get("SPROBOT_S3_BUCKET", "")
        self.client = boto3.client(
            "s3",
            aws_access_key_id=os.environ.get("SPROBOT_S3_KEY", ""),
            aws_secret_access_key=os.environ.get("SPROBOT_S3_SECRET", ""),
            endpoint_url=os.environ.get("SPROBOT_S3_ENDPOINT", ""),
        )

    def fetch_profile(
        self, guild_id: str, template_name: str, user_id: str
    ) -> Optional[Dict[str, str]]:
        s3_path = f"profiles/{guild_id}/{template_name}/{user_id}.json"
        try:
            obj = self.client.get_object(Bucket=self.bucket, Key=s3_path)
            return json.loads(obj["Body"].read())
        except botocore.exceptions.ClientError as e:
            if e.response["Error"]["Code"] == "NoSuchKey":
                return None
            raise
