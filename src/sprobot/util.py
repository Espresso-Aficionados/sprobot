from __future__ import annotations

import random
import string
import typing
from dataclasses import dataclass
from typing import Any, Dict, List, Optional, Union

import cachetools
import cachetools.keys
import discord
from discord import app_commands
from templates import Template
from thefuzz import process  # type: ignore


def _compute_ttl(key: Any, value: Any, now: float) -> float:
    return now + (AUTOCOMPLETE_CACHE_TTL_MINUTES * 60)


AUTOCOMPLETE_CACHE_TTL_MINUTES = 60
AUTOCOMPLETE_CACHE_SIZE = 500
AUTOCOMPLETE_CACHE = cachetools.TLRUCache(
    maxsize=AUTOCOMPLETE_CACHE_SIZE,
    ttu=_compute_ttl,
)

GET_USERS_CACHE = cachetools.TTLCache(maxsize=500, ttl=60)  # type: ignore

SINGLE_USER_CACHE = cachetools.LRUCache(maxsize=500)  # type: ignore


@dataclass
class User:
    FullName: str
    Name: str
    Discriminator: str
    Id: int


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
    if type(person) == discord.Member:
        if person.nick:
            return person.nick
    return person.name


def _autocomplete_cache_key(interaction: discord.Interaction, current: str) -> Any:
    if interaction.guild:
        return cachetools.keys.hashkey(interaction.guild.id, current)
    else:
        return cachetools.keys.hashkey(current)


@typing.no_type_check_decorator
@cachetools.cached(cache=AUTOCOMPLETE_CACHE, key=_autocomplete_cache_key)
def filter_users(
    interaction: discord.Interaction, current: str
) -> List[app_commands.Choice[str]]:
    return [
        app_commands.Choice(name=name[0], value=name[0])
        for name in process.extract(current, _get_users(interaction).keys(), limit=10)
    ]


def get_single_user(interaction: discord.Interaction, name: str) -> Optional[User]:
    users = _get_users(interaction)
    if name in users:
        maybeuser = users[name]
        if maybeuser is None or type(maybeuser) == User:
            return maybeuser

    maybecache = SINGLE_USER_CACHE.get(name)
    if maybecache and type(maybecache) == User:
        return maybecache

    res = process.extractOne(name, users.keys(), score_cutoff=90)
    if res:
        SINGLE_USER_CACHE[name] = users[res[0]]
        maybeuser = res[0]
        if type(maybeuser) == User:
            return maybeuser
        else:
            return None
    else:
        return None


# Unexposed return type
def _get_users_key(interaction: discord.Interaction) -> Any:
    if interaction.guild:
        return cachetools.keys.hashkey(interaction.guild.id)
    else:
        return cachetools.keys.hashkey(None)


@typing.no_type_check_decorator
@cachetools.cached(cache=GET_USERS_CACHE, key=_get_users_key)
def _get_users(
    interaction: discord.Interaction,
) -> Dict[str, User]:
    choices: Dict[str, User] = dict()

    if not interaction.guild:
        return choices

    for member in interaction.guild.members:
        if member.nick:
            userinfo = User(
                FullName=member.nick + "#" + member.discriminator,
                Name=member.nick,
                Discriminator=member.discriminator,
                Id=member.id,
            )
            choices[userinfo.FullName] = userinfo
        userinfo = User(
            FullName=member.name + "#" + member.discriminator,
            Name=member.name,
            Discriminator=member.discriminator,
            Id=member.id,
        )
        choices[userinfo.FullName] = userinfo

    return choices
