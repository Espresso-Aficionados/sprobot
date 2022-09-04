import traceback
import json
import typing
import sys
from typing import Dict, Any, List, Optional, Union
from collections import defaultdict

import discord
from discord import app_commands
import structlog

import backend
import util
from templates import Template, all_templates


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

        if profile:
            self.saved_image_url = profile[template.Image.Name]
        else:
            self.saved_image_url = None

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

        if self.saved_image_url:
            built_profile[self.template.Image.Name] = self.saved_image_url

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
            embed=util.build_embed_for_template(
                self.template, util.get_nick_or_name(interaction.user), built_profile
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


async def _member_autocomplete(
    interaction: discord.Interaction,
    current: str,
) -> List[app_commands.Choice[str]]:
    if current == "":
        return []

    return util.filter_users(interaction, current)


def _getgetfunc(
    template: Template,
) -> discord.app_commands.Command[Any, Any, Any]:
    @app_commands.command(
        name="get" + template.ShortName,
        description=template.Description,
    )
    @app_commands.autocomplete(name=_member_autocomplete)
    async def getfunc(interaction: discord.Interaction, name: Optional[str]) -> None:
        log = structlog.get_logger()
        log.info(
            "Processing getprofile",
            nick=f"{util.get_nick_or_name(interaction.user)}#{interaction.user.discriminator}",
            user_id=interaction.user.id,
            template=template.Name,
            guild_id=interaction.guild.id,
        )
        try:
            user_id = None
            user_name = None
            if name:
                possible_member = util.get_single_user(interaction, name)
                if possible_member:
                    user_id = possible_member.Id
                    user_name = possible_member.Name
            else:
                user_id = interaction.user.id
                user_name = util.get_nick_or_name(interaction.user)

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
                embed=util.build_embed_for_template(template, user_name, user_profile),
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
            nick=f"{util.get_nick_or_name(interaction.user)}#{interaction.user.discriminator}",
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


def _getgetmenu(
    guild_id: int, template: Template
) -> List[discord.app_commands.Command[Any, Any, Any]]:
    async def _getprofileint(
        interaction: discord.Interaction,
        author: Union[discord.Member, discord.User],
    ):
        log = structlog.get_logger()
        log.info(
            "Processing getprofile context menu",
            nick=f"{util.get_nick_or_name(interaction.user)}#{interaction.user.discriminator}",
            user_id=interaction.user.id,
            template=template.Name,
            guild_id=interaction.guild.id,
        )

        print(author.name, author.id)

        try:
            user_profile = await backend.fetch_profile(template, guild_id, author.id)

            await interaction.response.send_message(
                embed=util.build_embed_for_template(
                    template, util.get_nick_or_name(author), user_profile
                ),
            )
        except KeyError:
            await interaction.response.send_message(
                f"Whoops! Unable to find a {template.Name} profile for {util.get_nick_or_name(author)}.",
                ephemeral=True,
            )

    @app_commands.context_menu(
        name=f"Get {template.Name} Profile",
    )
    async def getprofilemessage(
        interaction: discord.Interaction,
        message: discord.Message,
    ) -> None:
        await _getprofileint(interaction, message.author)

    @app_commands.context_menu(
        name=f"Get {template.Name} Profile",
    )
    async def getprofileuser(
        interaction: discord.Interaction,
        user: Union[discord.Member, discord.User],
    ) -> None:
        await _getprofileint(interaction, user)

    return [getprofilemessage, getprofileuser]


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
                nick=f"{util.get_nick_or_name(interaction.user)}#{interaction.user.discriminator}",
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
                    found_attachment = True
                    user_profile[template.Image.Name] = attachment.proxy_url
                elif attachment.content_type.startswith("video/"):
                    found_video_error = (
                        f"It looks like that attachment was a "
                        f"video ({attachment.content_type}), "
                        "we can only use images."
                    )

            for embed in message.embeds:
                if embed.image:
                    if embed.image.proxy_url:
                        found_attachment = True
                        user_profile[template.Image.Name] = embed.image.proxy_url
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
                embed=util.build_embed_for_template(
                    template, util.get_nick_or_name(interaction.user), user_profile
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
            for cmd in _getgetmenu(guild_id, template):
                results[guild_id].append(cmd)
            results[guild_id].append(_getsavemenu(guild_id, template))

    return results
