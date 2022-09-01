import traceback
import json
import typing
import sys
from datetime import datetime, timedelta
from typing import Dict, Any, List, Optional
from collections import defaultdict

import discord
from discord import app_commands
from fuzzywuzzy import process
import cachetools

import backend
from templates import Template, all_templates
from util import build_embed_for_template

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
        self, guild_id: int, user_id: int, template: Template, *args: Any, **kwargs: Any
    ):
        # This must come before adding the children
        super().__init__(title="Edit Profile", *args, **kwargs)

        self.template = template

        user_profile = dict()
        try:
            user_profile = backend.fetch_profile(template, guild_id, user_id)
        except KeyError:  # It's ok if we don't get anything
            pass

        for field in template.Fields:
            self.add_item(
                discord.ui.TextInput(
                    label=field.Name,
                    placeholder=field.Placeholder,
                    style=field.Style,
                    max_length=1024,
                    required=False,
                    default=user_profile.get(field.Name),
                )
            )

    async def on_submit(self, interaction: discord.Interaction) -> None:
        built_profile: Dict[str, str] = {}
        for child in self.children:
            if type(child) != discord.ui.TextInput:
                continue
            built_profile[child.label] = child.value

        await backend.save_profile(
            self.template, interaction.guild.id, interaction.user.id, built_profile
        )

        # Save the profile? Download the image, verify with filetype, save to s3, then save the new URL in the profile
        print(json.dumps(built_profile))

        await interaction.response.send_message(
            embed=build_embed_for_template(self.template, built_profile),
            ephemeral=True,
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


def get_commands() -> Dict[int, List[discord.app_commands.Command[Any, Any, Any]]]:
    results = defaultdict(list)
    for guild_id, templates in all_templates.items():
        for template in templates:

            def geteditfunc(
                template: Template,
            ) -> discord.app_commands.Command[Any, Any, Any]:
                async def editfunc(interaction: discord.Interaction) -> None:
                    await interaction.response.send_modal(
                        EditProfile(guild_id, interaction.user.id, template=template)
                    )

                return (
                    app_commands.command(
                        name="edit" + template.ShortName,
                        description=template.Description,
                    )
                )(editfunc)

            results[guild_id].append(geteditfunc(template))

            def getgetfunc(
                template: Template,
            ) -> discord.app_commands.Command[Any, Any, Any]:
                @app_commands.autocomplete(name=member_autocomplete)
                async def getfunc(
                    interaction: discord.Interaction, name: Optional[str]
                ) -> None:
                    try:
                        user_id = None
                        if name:
                            possible_name_and_discrim = get_single_user(
                                interaction, name
                            )
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

                        else:
                            user_id = interaction.user.id

                        if not user_id:
                            if not name:
                                await interaction.response.send_message(
                                    f"Whoops! Unable to find a profile for you.",
                                    ephemeral=True,
                                )
                            else:
                                await interaction.response.send_message(
                                    f"Whoops! Unable to find a id for {name}.",
                                    ephemeral=True,
                                )

                        user_profile = backend.fetch_profile(
                            template, interaction.guild.id, user_id
                        )
                        await interaction.response.send_message(
                            embed=build_embed_for_template(template, user_profile),
                        )
                    except KeyError:
                        await interaction.response.send_message(
                            f"Whoops! Unable to find a profile for {name}.",
                            ephemeral=True,
                        )
                    except Exception as error:
                        await interaction.response.send_message(
                            "Oops! Something went wrong.", ephemeral=True
                        )
                        traceback.print_exception(*sys.exc_info())

                return (
                    app_commands.command(
                        name="get" + template.ShortName,
                        description=template.Description,
                    )
                )(getfunc)

            results[guild_id].append(getgetfunc(template))

    return results
