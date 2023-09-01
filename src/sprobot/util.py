from __future__ import annotations

import random
import string
from typing import Dict, Union

import discord
from templates import Template


def build_embed_for_template(
    template: Template, username: str, profile: Dict[str, str]
) -> discord.Embed:
    # TODO: Make this a link to a little webpage for the profile
    embed = discord.Embed(
        title=f"{template.Name} for {username}",
        url="http://bot.espressoaf.com/",
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
