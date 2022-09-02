import traceback
import json
import typing
import sys
from datetime import datetime, timedelta
from typing import Dict, Any, List, Optional
from collections import defaultdict

import discord
from discord import app_commands
from thefuzz import process
import structlog
import cachetools

import backend
from templates import Template, all_templates
from util import build_embed_for_template, get_nick_or_name

AUTOCOMPLETE_CACHE_TTL = 5
AUTOCOMPLETE_CACHE_SIZE = 500

AUTOCOMPLETE_CACHE = cachetools.TLRUCache(
    maxsize=AUTOCOMPLETE_CACHE_SIZE,
    ttu=lambda _, v, n: n + timedelta(minutes=AUTOCOMPLETE_CACHE_TTL),
    timer=datetime.now,
)

GET_USERS_CACHE = cachetools.TTLCache(maxsize=500, ttl=60)


class EditProfile(discord.ui.Modal):
    # Our modal classes MUST subclass `discord.ui.Modal`,
    # but the title can be whatever you want.

    def __init__(
        self,
        template: Template,
        profile: Optional[Dict[str, str]],
        *args: Any,
        **kwargs: Any,
    ):
        # This must come before adding the children
        super().__init__(title="Edit Profile", *args, **kwargs)

        self.template = template

        if not profile:  # use an empty one if we didn't find one
            profile = dict()

        for field in template.Fields:
            self.add_item(
                discord.ui.TextInput(
                    label=field.Name,
                    placeholder=field.Placeholder,
                    style=field.Style,
                    max_length=1024,
                    required=False,
                    default=profile.get(field.Name),
                )
            )

    async def on_submit(self, interaction: discord.Interaction) -> None:
        log = structlog.get_logger()
        built_profile: Dict[str, str] = {}
        for child in self.children:
            if type(child) != discord.ui.TextInput:
                log.info(
                    "Unused Child",
                    child=child,
                    child_type=type(child),
                    user_id=interaction.user.id,
                    template=self.template.Name,
                    guild_id=interaction.guild.id,
                )
                continue
            built_profile[child.label] = child.value

        log.info(
            "Raw profile",
            profile=json.dumps(built_profile),
            user_id=interaction.user.id,
            template=self.template.Name,
            guild_id=interaction.guild.id,
        )

        weburl, error = await backend.save_profile(
            self.template, interaction.guild.id, interaction.user.id, built_profile
        )

        if error:
            await interaction.response.send_message(error, ephemeral=True)
            return

        await interaction.response.send_message(
            embed=build_embed_for_template(
                self.template, get_nick_or_name(interaction.user), built_profile
            )
        )

    @typing.no_type_check  # on_error from Modal doesnt match the type signature of it's parent
    async def on_error(
        self, interaction: discord.Interaction, error: Exception
    ) -> None:
        await interaction.response.send_message(
            "Oops! Something went wrong.", ephemeral=True
        )

        # Make sure we know what the error actually is
        traceback.print_exception(*sys.exc_info())


async def member_autocomplete(
    interaction: discord.Interaction,
    current: str,
) -> List[app_commands.Choice[str]]:
    if current == "":
        return []

    return _filter_users(interaction, current)


def _autocomplete_cache_key(interaction: discord.Interaction, current: str):
    if interaction.guild:
        return cachetools.keys.hashkey(interaction.guild.id, current)
    else:
        return cachetools.keys.hashkey(current)


@cachetools.cached(cache=AUTOCOMPLETE_CACHE, key=_autocomplete_cache_key)
def _filter_users(
    interaction: discord.Interaction, current: str
) -> List[app_commands.Choice[str]]:
    return [
        app_commands.Choice(name=name[0], value=name[0])
        for name in process.extract(current, _get_users(interaction), limit=10)
    ]


def get_single_user(interaction: discord.Interaction, name: str) -> Optional[str]:
    res = process.extractOne(name, _get_users(interaction), score_cutoff=90)
    if res:
        return res[0]
    else:
        return None


def _get_users_key(interaction: discord.Interaction):
    if interaction.guild:
        return cachetools.keys.hashkey(interaction.guild.id)
    else:
        return cachetools.keys.hashkey(None)


@cachetools.cached(cache=GET_USERS_CACHE, key=_get_users_key)
def _get_users(
    interaction: discord.Interaction,
) -> List[str]:
    if not interaction.guild:
        return []

    choices = []
    for member in interaction.guild.members:
        if member.nick:
            choices.append(member.nick + "#" + member.discriminator)
        choices.append(member.name + "#" + member.discriminator)

    return choices


def _getgetfunc(
    template: Template,
) -> discord.app_commands.Command[Any, Any, Any]:
    @app_commands.command(
        name="get" + template.ShortName,
        description=template.Description,
    )
    @app_commands.autocomplete(name=member_autocomplete)
    async def getfunc(interaction: discord.Interaction, name: Optional[str]) -> None:
        log = structlog.get_logger()
        log.info(
            "Processing getprofile",
            nick=f"{get_nick_or_name(interaction.user)}#{interaction.user.discriminator}",
            user_id=interaction.user.id,
            template=template.Name,
            guild_id=interaction.guild.id,
        )
        try:
            user_id = None
            user_name = None
            if name:
                possible_name_and_discrim = get_single_user(interaction, name)
                (
                    possible_name,
                    possible_discrim,
                ) = possible_name_and_discrim.rsplit("#", 1)
                possible_member = discord.utils.get(
                    interaction.guild.members,
                    name=possible_name,
                    discriminator=possible_discrim,
                )
                if possible_member:
                    user_id = possible_member.id
                    user_name = get_nick_or_name(possible_member)

            else:
                user_id = interaction.user.id
                user_name = get_nick_or_name(interaction.user)

            if not user_id:
                if not name:
                    await interaction.response.send_message(
                        "Whoops! Unable to find a profile for you.",
                        ephemeral=True,
                    )
                else:
                    await interaction.response.send_message(
                        f"Whoops! Unable to find a id for {name}.",
                        ephemeral=True,
                    )

            user_profile = await backend.fetch_profile(
                template, interaction.guild.id, user_id
            )
            await interaction.response.send_message(
                embed=build_embed_for_template(template, user_name, user_profile),
            )
        except KeyError:
            await interaction.response.send_message(
                f"Whoops! Unable to find a profile for {name}.",
                ephemeral=True,
            )
        except Exception:
            await interaction.response.send_message(
                "Oops! Something went wrong.", ephemeral=True
            )
            traceback.print_exception(*sys.exc_info())

    return getfunc


def _geteditfunc(
    guild_id: int, template: Template
) -> discord.app_commands.Command[Any, Any, Any]:
    @app_commands.command(
        name="edit" + template.ShortName,
        description=template.Description,
    )
    async def editfunc(interaction: discord.Interaction) -> None:
        log = structlog.get_logger()
        log.info(
            "Processing edit",
            nick=f"{get_nick_or_name(interaction.user)}#{interaction.user.discriminator}",
            user_id=interaction.user.id,
            template=template.Name,
            guild_id=interaction.guild.id,
        )
        user_profile = None
        try:
            user_profile = await backend.fetch_profile(
                template, guild_id, interaction.user.id
            )
        except KeyError:  # It's ok if we don't get anything
            pass

        await interaction.response.send_modal(EditProfile(template, user_profile))

    return editfunc


def _getsavemenu(
    guild_id: int, template: Template
) -> discord.app_commands.Command[Any, Any, Any]:
    @app_commands.context_menu(
        name=f"Save as {template.Name} Image",
    )
    async def saveimage(
        interaction: discord.Interaction, message: discord.Message
    ) -> None:
        try:
            log = structlog.get_logger()
            log.info(
                "Processing saveimage",
                nick=f"{get_nick_or_name(interaction.user)}#{interaction.user.discriminator}",
                user_id=interaction.user.id,
                template=template.Name,
                guild_id=interaction.guild.id,
            )

            user_profile = None
            try:
                user_profile = await backend.fetch_profile(
                    template, guild_id, interaction.user.id
                )
            except KeyError:  # It's ok if we don't get anything
                pass
            if not user_profile:
                user_profile = dict()

            found_attachment = False
            found_video_error = ""
            for attachment in message.attachments:
                if attachment.content_type.startswith("image/"):
                    log.info(
                        "Found image in attachments",
                        image_url=attachment.proxy_url,
                        user_id=interaction.user.id,
                        template=template.Name,
                        guild_id=interaction.guild.id,
                    )
                    for field in template.Fields:
                        if field.Image:
                            user_profile[field.Name] = attachment.proxy_url
                elif attachment.content_type.startswith("video/"):
                    found_video_error = (
                        f"It looks like that attachment was a "
                        f"video ({attachment.content_type}), "
                        "we can only use images."
                    )

            for embed in message.embeds:
                if embed.image:
                    if embed.image.proxy_url:
                        for field in template.Fields:
                            if field.Image:
                                user_profile[field.Name] = embed.image.proxy_url
                                log.info(
                                    "Found image in embeds",
                                    image_url=embed.image.proxy_url,
                                    user_id=interaction.user.id,
                                    template=template.Name,
                                    guild_id=interaction.guild.id,
                                )

            if found_video_error and not found_attachment:
                await interaction.response.send_message(
                    found_video_error, ephemeral=True
                )
                return

            if not found_attachment:
                await interaction.response.send_message(
                    "I didn't find an image to save in that post :(", ephemeral=True
                )
                return

            web_url, error = await backend.save_profile(
                template, interaction.guild.id, interaction.user.id, user_profile
            )

            if error:
                await interaction.response.send_message(error, ephemeral=True)
                return

            await interaction.response.send_message(
                embed=build_embed_for_template(
                    template, get_nick_or_name(interaction.user), user_profile
                ),
            )

        except Exception:
            await interaction.response.send_message(
                "Oops! Something went wrong.", ephemeral=True
            )
            traceback.print_exception(*sys.exc_info())

    return saveimage


def get_commands() -> Dict[int, List[discord.app_commands.Command[Any, Any, Any]]]:
    results = defaultdict(list)
    for guild_id, templates in all_templates.items():
        for template in templates:
            results[guild_id].append(_geteditfunc(guild_id, template))
            results[guild_id].append(_getgetfunc(template))
            results[guild_id].append(_getsavemenu(guild_id, template))

    return results
