from typing import Dict, Optional
import tempfile
import json
import shutil
import os.path
from urllib.parse import urljoin, quote

from templates import Template

import httpx
import filetype  # type: ignore
import boto3

SPROBOT_S3_KEY = os.environ.get("SPROBOT_S3_KEY")
SPROBOT_S3_SECRET = os.environ.get("SPROBOT_S3_SECRET")
SPROBOT_S3_ENDPOINT = os.environ.get("SPROBOT_S3_ENDPOINT")

S3_CLIENT = boto3.client(
    "s3",
    aws_access_key_id=SPROBOT_S3_KEY,
    aws_secret_access_key=SPROBOT_S3_SECRET,
    endpoint_url=SPROBOT_S3_ENDPOINT,
)


def fetch_profile(
    template: Template, guild_id: int, user_id: int
) -> Optional[Dict[str, str]]:
    try:
        s3_path = os.path.join(
            "profiles",
            str(guild_id),
            template.Name,
            f"{user_id}.json",
        )

        obj = S3_CLIENT.get_object(
            Bucket="profile-bot",
            Key=s3_path,
        )

        return json.loads(obj["Body"].read())
    except S3_CLIENT.exceptions.NoSuchKey as e:
        # Normalize this to a simple KeyError
        raise KeyError("User profile not found")


async def save_profile(
    template: Template, guild_id: int, user_id: int, profile: Dict[str, str]
) -> None:

    # Step 1; We need to host the image somewhere safe
    for field in template.Fields:
        if not field.Image:
            continue

        maybeURL = profile.get(field.Name, None)
        if not maybeURL:
            continue

        if maybeURL.startswith(SPROBOT_S3_ENDPOINT):
            continue

        with tempfile.NamedTemporaryFile() as buf:
            # Save the (possible) image to a temp file

            async with httpx.AsyncClient() as httpclient:
                async with httpclient.stream("GET", maybeURL) as resp:
                    async for chunk in resp.aiter_bytes():
                        buf.write(chunk)
            buf.flush()

            # Try to verify that it is indeed an image
            kind = filetype.guess(buf.name)
            if not kind:
                del profile[field.Name]
                continue
            if not kind.mime.startswith("image/"):
                del profile[field.Name]
                continue

            s3_path = os.path.join(
                "images",
                str(guild_id),
                template.Name,
                f"{user_id}.{kind.extension}",
            )

            S3_CLIENT.upload_file(
                buf.name, "profile-bot", s3_path, ExtraArgs={"ACL": "public-read"}
            )

            s3_final_url = urljoin(
                SPROBOT_S3_ENDPOINT, urljoin("profile-bot/", quote(s3_path))
            )

            print(f"Saved image to: {s3_final_url}")

            # Now we replace the original one with our new hosted URL
            profile[field.Name] = s3_final_url

    s3_path = os.path.join(
        "profiles",
        str(guild_id),
        template.Name,
        f"{user_id}.json",
    )
    S3_CLIENT.put_object(
        Body=json.dumps(profile),
        Bucket="profile-bot",
        Key=s3_path,
    )
