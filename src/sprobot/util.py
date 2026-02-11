from __future__ import annotations

import os
import random
import string
from typing import Dict, Optional, Union
from urllib.parse import quote

import discord
from templates import Template

SPROBOT_WEB_ENDPOINT = "http://bot.espressoaf.com/"


def _build_profile_url(template: Template, guild_id: int, user_id: int) -> str:
    bucket = os.environ.get("SPROBOT_S3_BUCKET", "")
    s3_path = f"profiles/{guild_id}/{template.Name}/{user_id}.json"
    return SPROBOT_WEB_ENDPOINT + quote(f"{bucket}/{s3_path}")


def build_embed_for_template(
    template: Template,
    username: str,
    profile: Dict[str, str],
    guild_id: Optional[int] = None,
    user_id: Optional[int] = None,
) -> discord.Embed:
    if guild_id and user_id:
        url = _build_profile_url(template, guild_id, user_id)
    else:
        url = SPROBOT_WEB_ENDPOINT

    embed = discord.Embed(
        title=f"{template.Name} for {username}",
        url=url,
        color=discord.Colour.from_rgb(103, 71, 54),
    )
    for field in template.Fields:
        field_content = profile.get(field.Name, None)
        if not field_content:
            continue
        embed.add_field(name=field.Name, value=field_content, inline=False)

    maybeimage = profile.get(template.Image.Name)
    if maybeimage:
        # This is a hack to get around discord caching the URL when people change their profile pic
        embed.set_image(
            url=(
                maybeimage
                + "?"
                + "".join(random.choice(string.ascii_letters) for i in range(10))
            )
        )
    else:
        embed.add_field(
            name="Want to add a profile image?",
            value=(
                "Check out the guide at https://espressoaf.com/"
                "guides/sprobot.html#saving-a-profile-image-via-right-click"
            ),
        )

    embed.set_footer(
        text="sprobot",
        icon_url="https://profile-bot.us-southeast-1.linodeobjects.com/76916743.gif",
    )
    return embed


def get_nick_or_name(person: Union[discord.Member, discord.User]) -> str:
    if type(person) is discord.Member:
        if person.nick:
            return person.nick
    return person.name
