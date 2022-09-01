from typing import Dict, Optional
import tempfile
import json
import time
import os.path
from urllib.parse import urljoin, quote

from templates import Template

import httpx
import filetype  # type: ignore
import aioboto3

SPROBOT_S3_KEY = os.environ.get("SPROBOT_S3_KEY")
SPROBOT_S3_SECRET = os.environ.get("SPROBOT_S3_SECRET")
SPROBOT_S3_ENDPOINT = os.environ.get("SPROBOT_S3_ENDPOINT")

# session = aioboto3.Session()

# S3_CLIENT = aioboto3.client(
# "s3",
# aws_access_key_id=SPROBOT_S3_KEY,
# aws_secret_access_key=SPROBOT_S3_SECRET,
# endpoint_url=SPROBOT_S3_ENDPOINT,
# )


async def fetch_profile(
    template: Template, guild_id: int, user_id: int
) -> Optional[Dict[str, str]]:

    s3_path = os.path.join(
        "profiles",
        str(guild_id),
        template.Name,
        f"{user_id}.json",
    )

    session = aioboto3.Session()
    async with session.client(
        "s3",
        aws_access_key_id=SPROBOT_S3_KEY,
        aws_secret_access_key=SPROBOT_S3_SECRET,
        endpoint_url=SPROBOT_S3_ENDPOINT,
    ) as s3:
        try:
            start = time.time()
            obj = await s3.get_object(
                Bucket="profile-bot",
                Key=s3_path,
            )
            res = json.loads(await obj["Body"].read())
            print("s3 fetch time:", (time.time() - start) * 10**3, "ms")
            return res
        except s3.exceptions.NoSuchKey:
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

            buf.seek(0)
            session = aioboto3.Session()
            async with session.client(
                "s3",
                aws_access_key_id=SPROBOT_S3_KEY,
                aws_secret_access_key=SPROBOT_S3_SECRET,
                endpoint_url=SPROBOT_S3_ENDPOINT,
            ) as s3:

                await s3.upload_fileobj(
                    buf, "profile-bot", s3_path, ExtraArgs={"ACL": "public-read"}
                )

                await s3.put_object_acl(
                    ACL="public-read",
                    Bucket="profile-bot",
                    Key=s3_path,
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
    session = aioboto3.Session()
    async with session.client(
        "s3",
        aws_access_key_id=SPROBOT_S3_KEY,
        aws_secret_access_key=SPROBOT_S3_SECRET,
        endpoint_url=SPROBOT_S3_ENDPOINT,
    ) as s3:
        await s3.put_object(
            Body=json.dumps(profile),
            Bucket="profile-bot",
            Key=s3_path,
        )
