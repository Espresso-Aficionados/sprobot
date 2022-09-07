from __future__ import annotations

import json
import os.path
import sys
import tempfile
import time
import traceback
from typing import Dict, Optional, Tuple
from urllib.parse import quote, urljoin

import aioboto3  # type: ignore
import cachetools
import cachetools.keys
import filetype  # type: ignore
import httpx
import structlog
from templates import Template

SPROBOT_S3_KEY = os.environ.get("SPROBOT_S3_KEY")
SPROBOT_S3_SECRET = os.environ.get("SPROBOT_S3_SECRET")
SPROBOT_S3_ENDPOINT = os.environ.get("SPROBOT_S3_ENDPOINT")
SPROBOT_S3_BUCKET = os.environ.get("SPROBOT_S3_BUCKET")

SPROBOT_WEB_ENDPOINT = "http://173.255.193.219/"

PROFILE_CACHE = cachetools.LRUCache(maxsize=500)  # type: ignore


async def fetch_profile(
    template: Template, guild_id: int, user_id: int
) -> Dict[str, str]:

    log = structlog.get_logger()
    log.info(
        "Fetching profile",
        user_id=user_id,
        template=template.Name,
        guild_id=guild_id,
    )

    cache_key = cachetools.keys.hashkey(template.Name, guild_id, user_id)
    profile = PROFILE_CACHE.get(cache_key)
    if profile and type(profile) == dict:
        log.info(
            "Returning cached profile",
            user_id=user_id,
            template=template.Name,
            guild_id=guild_id,
            profile=profile,
        )
        return profile

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
                Bucket=SPROBOT_S3_BUCKET,
                Key=s3_path,
            )
            res = json.loads(await obj["Body"].read())

            log.info(
                f"s3 fetch time: {(time.time() - start) * 10**3}ms",
                user_id=user_id,
                template=template.Name,
                guild_id=guild_id,
            )
            PROFILE_CACHE[cache_key] = res
            if type(res) == dict:
                return res
        except s3.exceptions.NoSuchKey:
            # Normalize this to a simple KeyError
            raise KeyError("User profile not found")

    raise KeyError("User profile not found")


async def _get_image_s3_url(
    template: Template, guild_id: int, user_id: int, profile: Dict[str, str]
) -> Tuple[str, Optional[str]]:
    log = structlog.get_logger()

    maybeURL = profile.get(template.Image.Name, None)
    if not maybeURL:
        return "", None

    if not SPROBOT_S3_ENDPOINT:
        raise KeyError("SPROBOT_S3_ENDPOINT is undefined!")

    if maybeURL.startswith(SPROBOT_S3_ENDPOINT):
        return "", maybeURL

    with tempfile.NamedTemporaryFile() as buf:
        # Save the (possible) image to a temp file
        try:
            async with httpx.AsyncClient() as httpclient:
                async with httpclient.stream("GET", maybeURL) as resp:
                    async for chunk in resp.aiter_bytes():
                        buf.write(chunk)
        except Exception:
            error_for_user = (
                "Unable to fetch from the URL provided, make sure "
                "it's an image and try again. The rest of your "
                "profile has been saved."
            )
            traceback.print_exception(*sys.exc_info())
            return error_for_user, None

        # Try to verify that it is indeed an image
        kind = filetype.guess(buf.name)
        if not kind:
            error_for_user = (
                "Unable to determine what type of file you linked. "
                "The rest of your profile has been saved."
            )
            return error_for_user, None
        if not kind.mime.startswith("image/"):
            error_for_user = (
                f"It looks like you uploaded a {kind.mime}, but we can only use images. "
                "The rest of your profile has been saved. If this looked like a gif, discord probably "
                "used a mp4."
            )
            return error_for_user, None

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
                buf, SPROBOT_S3_BUCKET, s3_path, ExtraArgs={"ACL": "public-read"}
            )

            await s3.put_object_acl(
                ACL="public-read",
                Bucket=SPROBOT_S3_BUCKET,
                Key=s3_path,
            )

        s3_final_url = urljoin(
            SPROBOT_S3_ENDPOINT, urljoin(f"{SPROBOT_S3_BUCKET}/", quote(s3_path))
        )

        log.info(
            "Profile Image Saved",
            user_id=user_id,
            template=template.Name,
            guild_id=guild_id,
            profile=profile,
            s3_url=s3_final_url,
        )

        # Now we replace the original one with our new hosted URL
        return "", s3_final_url


async def delete_profile(
    template: Template,
    guild_id: int,
    user_id: int,
) -> None:
    log = structlog.get_logger()
    log.info(
        "Deleting profile",
        user_id=user_id,
        template=template.Name,
        guild_id=guild_id,
    )

    try:
        cache_key = cachetools.keys.hashkey(template.Name, guild_id, user_id)
        del PROFILE_CACHE[cache_key]
    except KeyError:
        pass

    s3_path = os.path.join(
        "profiles",
        str(guild_id),
        template.Name,
        f"{user_id}.json",
    )
    start = time.time()
    session = aioboto3.Session()
    async with session.client(
        "s3",
        aws_access_key_id=SPROBOT_S3_KEY,
        aws_secret_access_key=SPROBOT_S3_SECRET,
        endpoint_url=SPROBOT_S3_ENDPOINT,
    ) as s3:
        await s3.delete_object(
            Bucket=SPROBOT_S3_BUCKET,
            Key=s3_path,
        )
    log.info(
        f"s3 delete time: {(time.time() - start) * 10**3}ms",
        user_id=user_id,
        template=template.Name,
        guild_id=guild_id,
    )

    log.info(
        "Profile Deleted",
        user_id=user_id,
        template=template.Name,
        guild_id=guild_id,
    )


async def save_profile(
    template: Template, guild_id: int, user_id: int, profile: Dict[str, str]
) -> Tuple[str, Optional[str]]:
    log = structlog.get_logger()
    log.info(
        "Saving profile",
        user_id=user_id,
        template=template.Name,
        guild_id=guild_id,
        profile=profile,
    )

    error_for_user = None
    web_url = ""
    # Step 1; We need to host the image somewhere safe
    error_for_user, image_url = await _get_image_s3_url(
        template, guild_id, user_id, profile
    )
    if image_url:
        profile[template.Image.Name] = image_url
    else:
        try:
            del profile[template.Image.Name]
        except KeyError:
            pass

    s3_path = os.path.join(
        "profiles",
        str(guild_id),
        template.Name,
        f"{user_id}.json",
    )
    start = time.time()
    session = aioboto3.Session()
    async with session.client(
        "s3",
        aws_access_key_id=SPROBOT_S3_KEY,
        aws_secret_access_key=SPROBOT_S3_SECRET,
        endpoint_url=SPROBOT_S3_ENDPOINT,
    ) as s3:
        await s3.put_object(
            Body=json.dumps(profile),
            Bucket=SPROBOT_S3_BUCKET,
            Key=s3_path,
        )
    log.info(
        f"s3 write time: {(time.time() - start) * 10**3}ms",
        user_id=user_id,
        template=template.Name,
        guild_id=guild_id,
    )

    cache_key = cachetools.keys.hashkey(template.Name, guild_id, user_id)
    profile = PROFILE_CACHE[cache_key] = profile

    web_url = urljoin(
        SPROBOT_WEB_ENDPOINT, urljoin(f"{SPROBOT_S3_BUCKET}/", quote(s3_path))
    )
    log.info(
        "Profile Saved",
        user_id=user_id,
        template=template.Name,
        guild_id=guild_id,
        profile=profile,
        profile_url=web_url,
    )

    return web_url, error_for_user
