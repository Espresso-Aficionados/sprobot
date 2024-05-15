from __future__ import annotations

import copy
import json
import os.path
import random
import string
import sys
import tempfile
import time
import traceback
from typing import Any, Dict, Optional, Tuple
from urllib.parse import quote, urljoin, urlparse

import aioboto3  # type: ignore
import cachetools
import cachetools.keys
import filetype  # type: ignore
import httpx
import structlog
from templates import Template

SPROBOT_WEB_ENDPOINT = "http://bot.espressoaf.com/"


class S3Backend:
    def __init__(self) -> None:
        self.sprobot_s3_key = os.environ.get("SPROBOT_S3_KEY", "")
        if not self.sprobot_s3_key:
            raise KeyError("SPROBOT_S3_KEY env var is undefined!", "")

        self.sprobot_s3_secret = os.environ.get("SPROBOT_S3_SECRET", "")
        if not self.sprobot_s3_secret:
            raise KeyError("SPROBOT_S3_SECRET env var is undefined!", "")

        self.sprobot_s3_endpoint = os.environ.get("SPROBOT_S3_ENDPOINT", "")
        if not self.sprobot_s3_endpoint:
            raise KeyError("SPROBOT_S3_ENDPOINT env var is undefined!", "")

        self.sprobot_s3_bucket = os.environ.get("SPROBOT_S3_BUCKET", "")
        if not self.sprobot_s3_bucket:
            raise KeyError("SPROBOT_S3_BUCKET env var is undefined!")

        self.profile_cache = cachetools.LRUCache(maxsize=500)  # type: ignore
        self.profile_cache_total_hits = 1
        self.profile_cache_misses = 1
        self.log = structlog.get_logger()

    def _get_hit_precent(self) -> str:
        hit_pct = self.profile_cache_total_hits / (
            self.profile_cache_total_hits + self.profile_cache_misses
        )
        return f"{hit_pct:.1%}"

    def _get_from_cache(self, cache_key: Any) -> Optional[Dict[str, str]]:
        self.profile_cache_total_hits += 1
        profile = self.profile_cache.get(cache_key)
        message = ""
        if not profile:
            message = "Cache Miss"
            self.profile_cache_misses += 1
        else:
            message = "Cache Hit"

        self.log.info(
            message,
            cache_total_hits=self.profile_cache_total_hits,
            cache_misses=self.profile_cache_misses,
            cache_hit_pct=self._get_hit_precent(),
            cache_curr_size=self.profile_cache.currsize,
            cache_max_size=self.profile_cache.maxsize,
        )

        return copy.deepcopy(profile)

    async def fetch_profile(
        self, template: Template, guild_id: int, user_id: int
    ) -> Dict[str, str]:
        self.log.info(
            "Fetching profile",
            user_id=user_id,
            template=template.Name,
            guild_id=guild_id,
        )

        cache_key = cachetools.keys.hashkey(template.Name, guild_id, user_id)
        profile = self._get_from_cache(cache_key)
        if profile and type(profile) is dict:
            self.log.info(
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
            aws_access_key_id=self.sprobot_s3_key,
            aws_secret_access_key=self.sprobot_s3_secret,
            endpoint_url=self.sprobot_s3_endpoint,
        ) as s3:  # type: ignore
            try:
                start = time.time()
                obj = await s3.get_object(
                    Bucket=self.sprobot_s3_bucket,
                    Key=s3_path,
                )
                res = json.loads(await obj["Body"].read())

                self.log.info(
                    f"s3 fetch time: {(time.time() - start) * 10**3}ms",
                    user_id=user_id,
                    template=template.Name,
                    guild_id=guild_id,
                )
                cache_key = cachetools.keys.hashkey(template.Name, guild_id, user_id)
                self.profile_cache[cache_key] = res
                if type(res) is dict:
                    return res
            except s3.exceptions.NoSuchKey:
                # Normalize this to a simple KeyError
                raise KeyError("User profile not found")

        raise KeyError("User profile not found")

    async def _get_image_s3_url(
        self, template: Template, guild_id: int, user_id: int, profile: Dict[str, str]
    ) -> Tuple[str, Optional[str]]:
        maybeURL = profile.get(template.Image.Name, None)
        if not maybeURL:
            return "", None

        if maybeURL.startswith(self.sprobot_s3_endpoint):
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
                aws_access_key_id=self.sprobot_s3_key,
                aws_secret_access_key=self.sprobot_s3_secret,
                endpoint_url=self.sprobot_s3_endpoint,
            ) as s3:  # type: ignore
                await s3.upload_fileobj(
                    buf,
                    self.sprobot_s3_bucket,
                    s3_path,
                    ExtraArgs={"ACL": "public-read"},
                )

                await s3.put_object_acl(
                    ACL="public-read",
                    Bucket=self.sprobot_s3_bucket,
                    Key=s3_path,
                )

            s3_final_url = urljoin(
                self.sprobot_s3_endpoint,
                urljoin(f"{self.sprobot_s3_bucket}/", quote(s3_path)),
            )

            self.log.info(
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
        self,
        template: Template,
        guild_id: int,
        user_id: int,
    ) -> None:
        self.log.info(
            "Deleting profile",
            user_id=user_id,
            template=template.Name,
            guild_id=guild_id,
        )

        try:
            cache_key = cachetools.keys.hashkey(template.Name, guild_id, user_id)
            del self.profile_cache[cache_key]
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
            aws_access_key_id=self.sprobot_s3_key,
            aws_secret_access_key=self.sprobot_s3_secret,
            endpoint_url=self.sprobot_s3_endpoint,
        ) as s3:  # type: ignore
            await s3.delete_object(
                Bucket=self.sprobot_s3_bucket,
                Key=s3_path,
            )
        self.log.info(
            f"s3 delete time: {(time.time() - start) * 10**3}ms",
            user_id=user_id,
            template=template.Name,
            guild_id=guild_id,
        )

        self.log.info(
            "Profile Deleted",
            user_id=user_id,
            template=template.Name,
            guild_id=guild_id,
        )

    # Returns permalink to file/image
    async def save_mod_image(self, guild_id: int, url: str) -> str:
        self.log.info(
            "Saving file to mod log",
            guild_id=guild_id,
        )

        with tempfile.NamedTemporaryFile() as buf:
            # Save the (possible) image to a temp file
            try:
                async with httpx.AsyncClient() as httpclient:
                    async with httpclient.stream("GET", url) as resp:
                        async for chunk in resp.aiter_bytes():
                            buf.write(chunk)
            except Exception:
                self.log.info("Unable to fetch from the link provided", url=url)
                traceback.print_exception(*sys.exc_info())
                return url

            random_id = "".join(
                random.choices(string.ascii_letters + string.digits, k=30)
            )
            url_path = urlparse(url).path
            extension = os.path.splitext(url_path)[1]
            s3_path = os.path.join(
                "mod_files",
                str(guild_id),
                f"{random_id}.{extension}",
            )

            buf.seek(0)
            session = aioboto3.Session()
            async with session.client(
                "s3",
                aws_access_key_id=self.sprobot_s3_key,
                aws_secret_access_key=self.sprobot_s3_secret,
                endpoint_url=self.sprobot_s3_endpoint,
            ) as s3:  # type: ignore
                await s3.upload_fileobj(
                    buf,
                    self.sprobot_s3_bucket,
                    s3_path,
                    ExtraArgs={"ACL": "public-read"},
                )

                await s3.put_object_acl(
                    ACL="public-read",
                    Bucket=self.sprobot_s3_bucket,
                    Key=s3_path,
                )

            s3_final_url = urljoin(
                self.sprobot_s3_endpoint,
                urljoin(f"{self.sprobot_s3_bucket}/", quote(s3_path)),
            )

            self.log.info(
                "Mod Image Saved",
                guild_id=guild_id,
                s3_url=s3_final_url,
            )

            # Now we replace the original one with our new hosted URL
            return s3_final_url

    async def save_profile(
        self, template: Template, guild_id: int, user_id: int, profile: Dict[str, str]
    ) -> Tuple[str, Optional[str]]:
        self.log.info(
            "Saving profile",
            user_id=user_id,
            template=template.Name,
            guild_id=guild_id,
            profile=profile,
        )

        error_for_user = None
        web_url = ""
        # Step 1; We need to host the image somewhere safe
        error_for_user, image_url = await self._get_image_s3_url(
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
            aws_access_key_id=self.sprobot_s3_key,
            aws_secret_access_key=self.sprobot_s3_secret,
            endpoint_url=self.sprobot_s3_endpoint,
        ) as s3:  # type: ignore
            await s3.put_object(
                Body=json.dumps(profile),
                Bucket=self.sprobot_s3_bucket,
                Key=s3_path,
            )
        self.log.info(
            f"s3 write time: {(time.time() - start) * 10**3}ms",
            user_id=user_id,
            template=template.Name,
            guild_id=guild_id,
        )

        cache_key = cachetools.keys.hashkey(template.Name, guild_id, user_id)
        self.profile_cache[cache_key] = profile

        web_url = urljoin(
            SPROBOT_WEB_ENDPOINT, urljoin(f"{self.sprobot_s3_bucket}/", quote(s3_path))
        )
        self.log.info(
            "Profile Saved",
            user_id=user_id,
            template=template.Name,
            guild_id=guild_id,
            profile=profile,
            profile_url=web_url,
        )

        return web_url, error_for_user


s3_backend = S3Backend()
