from __future__ import annotations

import json
import sys
import traceback
import typing
from enum import Enum
from typing import Any, Dict, List, Optional, Union

import backend
import discord
import structlog
import util
from discord import app_commands
from templates import Template, all_templates


def _getdeletefunc(template: Template) -> discord.app_commands.Command[Any, Any, Any]:
    @app_commands.command(
        name="delete" + template.ShortName,
        description="Delete profile or profile image",
    )
    async def deletefunc(interaction: discord.Interaction) -> None:
        if not interaction.guild:
            return
        view = DeleteProfile(template)
        log = structlog.get_logger()
        log.info(
            "Processing delete",
            nick=f"{util.get_nick_or_name(interaction.user)}#{interaction.user.discriminator}",
            user_id=interaction.user.id,
            template=template.Name,
            guild_id=interaction.guild.id,
        )
        await interaction.response.send_message(
            "What would you like to delete?", ephemeral=True, view=view
        )

    return deletefunc


class DeleteState(Enum):
    Called = 1
    WaitForConfirm = 2
    Confirmed = 3
    Cancelled = 4
    Deleted = 5
    Error = 6


class DeleteProfile(discord.ui.View):
    def __init__(self, template: Template) -> None:
        super().__init__()
        self._state = DeleteState.Called
        self._template = template
        self.log = structlog.get_logger()

        delete_full_profile_button = discord.ui.Button(
            style=discord.ButtonStyle.primary,
            label="Delete Entire Profile",
            custom_id="delete_profile",
        )  # type: ignore
        delete_full_profile_button.callback = self.delete_entire_profile  # type: ignore
        self.add_item(delete_full_profile_button)

        delete_profile_image_button = discord.ui.Button(
            style=discord.ButtonStyle.primary,
            label="Delete Profile Picture",
            custom_id="delete_image",
        )  # type: ignore
        delete_profile_image_button.callback = self.delete_profile_image  # type: ignore
        self.add_item(delete_profile_image_button)

        cancel_button = discord.ui.Button(
            label="Cancel", style=discord.ButtonStyle.grey, custom_id="cancel"
        )  # type: ignore
        cancel_button.callback = self.cancel  # type: ignore
        self.add_item(cancel_button)

    async def _update_message(self, interaction: discord.Interaction) -> None:
        self.clear_items()
        if self._state == DeleteState.WaitForConfirm:
            confirm_button = discord.ui.Button(
                label="Confirm",
                style=discord.ButtonStyle.danger,
                custom_id="confirm",
            )  # type: ignore
            confirm_button.callback = self.confirm  # type: ignore
            self.add_item(confirm_button)

            cancel_button = discord.ui.Button(
                label="Cancel", style=discord.ButtonStyle.grey, custom_id="cancel"
            )  # type: ignore
            cancel_button.callback = self.cancel  # type: ignore
            self.add_item(cancel_button)

            await interaction.response.edit_message(content="Are you sure?", view=self)

        elif self._state == DeleteState.Cancelled:
            await interaction.response.edit_message(content="No worries!", view=self)
            self.stop()

        elif self._state == DeleteState.Deleted:
            await interaction.response.edit_message(content="Deleted!", view=self)
            self.stop()

        elif self._state == DeleteState.Deleted:
            await interaction.response.edit_message(content=self._error, view=self)
            self.stop()

    async def cancel(self, interaction: discord.Interaction) -> None:
        self._state = DeleteState.Cancelled
        await self._update_message(interaction)

    async def confirm(self, interaction: discord.Interaction) -> None:
        self._state = DeleteState.Confirmed
        await self._next(interaction)

    async def delete_entire_profile(self, interaction: discord.Interaction) -> None:
        if not interaction.guild:
            raise TypeError("No Guild Found")
        if not interaction.user:
            raise TypeError("No User Found")
        if self._state == DeleteState.Called:
            self._state = DeleteState.WaitForConfirm
            self._next = self.delete_entire_profile
            await self._update_message(interaction)
        elif self._state == DeleteState.Confirmed:
            await backend.s3_backend.delete_profile(
                self._template,
                interaction.guild.id,
                interaction.user.id,
            )
            self._state = DeleteState.Deleted
            await self._update_message(interaction)

    async def delete_profile_image(self, interaction: discord.Interaction) -> None:
        if not interaction.guild:
            raise TypeError("No Guild Found")
        if not interaction.user:
            raise TypeError("No User Found")
        if self._state == DeleteState.Called:
            self._state = DeleteState.WaitForConfirm
            self._next = self.delete_profile_image
            await self._update_message(interaction)
        elif self._state == DeleteState.Confirmed:
            user_profile = None
            try:
                user_profile = await backend.s3_backend.fetch_profile(
                    self._template, interaction.guild.id, interaction.user.id
                )
            except KeyError:
                self._state = DeleteState.Deleted
                await self._update_message(interaction)
                return

            if user_profile:
                if self._template.Image.Name in user_profile:
                    del user_profile[self._template.Image.Name]

            # This might have been the only field
            if user_profile:
                _, self._error = await backend.s3_backend.save_profile(
                    self._template,
                    interaction.guild.id,
                    interaction.user.id,
                    user_profile,
                )

                if self._error:
                    self._state = DeleteState.Error
                    await self._update_message(interaction)
                    return
            else:
                await backend.s3_backend.delete_profile(
                    self._template,
                    interaction.guild.id,
                    interaction.user.id,
                )

            self._state = DeleteState.Deleted
            await self._update_message(interaction)


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

        self.saved_image_url = profile.get(template.Image.Name)

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
        if not interaction.guild:
            raise TypeError("No Guild Found")
        if not interaction.user:
            raise TypeError("No User Found")
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

        weburl, error = await backend.s3_backend.save_profile(
            self.template, interaction.guild.id, interaction.user.id, built_profile
        )

        if error:
            await interaction.response.send_message(error, ephemeral=True)
            return

        await interaction.response.send_message(
            embed=util.build_embed_for_template(
                self.template, util.get_nick_or_name(interaction.user), built_profile
            ),
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


def _getgetfunc(
    template: Template,
) -> discord.app_commands.Command[Any, Any, Any]:
    @app_commands.command(
        name="get" + template.ShortName,
        description=template.Description,
    )
    async def getfunc(
        interaction: discord.Interaction, name: Optional[discord.Member]
    ) -> None:
        if not interaction.guild:
            raise TypeError("No Guild Found")
        if not interaction.user:
            raise TypeError("No User Found")

        log = structlog.get_logger()
        log.info(
            "Processing getprofile",
            nick=f"{util.get_nick_or_name(interaction.user)}#{interaction.user.discriminator}",
            user_id=interaction.user.id,
            template=template.Name,
            guild_id=interaction.guild.id,
        )
        try:
            user_id: int = 0
            user_name: str = ""

            if name:
                user_id = name.id
                user_name = util.get_nick_or_name(name)
            else:
                user_id = interaction.user.id
                user_name = util.get_nick_or_name(interaction.user)

            user_profile = await backend.s3_backend.fetch_profile(
                template, interaction.guild.id, user_id
            )
            await interaction.response.send_message(
                embed=util.build_embed_for_template(template, user_name, user_profile),
            )

        except KeyError:
            if name:
                user_name = util.get_nick_or_name(name)
                message = f"Whoops! Unable to find a profile for {user_name}."
            else:
                message = (
                    "Whoops! Unable to find a profile for you. "
                    f"To set one up run /edit{template.ShortName}"
                )
            await interaction.response.send_message(
                message,
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
    async def editfunc(
        interaction: discord.Interaction, image: Optional[discord.Attachment]
    ) -> None:
        if not interaction.guild:
            raise TypeError("No Guild Found")
        if not interaction.user:
            raise TypeError("No User Found")
        log = structlog.get_logger()
        log.info(
            "Processing edit",
            nick=f"{util.get_nick_or_name(interaction.user)}#{interaction.user.discriminator}",
            user_id=interaction.user.id,
            template=template.Name,
            guild_id=interaction.guild.id,
        )
        user_profile = dict()
        try:
            user_profile = await backend.s3_backend.fetch_profile(
                template, guild_id, interaction.user.id
            )
        except KeyError:  # It's ok if we don't get anything
            pass

        if image:
            user_profile[template.Image.Name] = image.proxy_url

        await interaction.response.send_modal(EditProfile(template, user_profile))

    return editfunc


def _getgetmenu(
    guild_id: int, template: Template
) -> List[discord.app_commands.ContextMenu]:
    async def _getprofileint(
        interaction: discord.Interaction,
        author: Union[discord.Member, discord.User],
    ) -> None:
        if not interaction.guild:
            raise TypeError("No Guild Found")
        if not interaction.user:
            raise TypeError("No User Found")
        log = structlog.get_logger()
        log.info(
            "Processing getprofile context menu",
            nick=f"{util.get_nick_or_name(interaction.user)}#{interaction.user.discriminator}",
            user_id=interaction.user.id,
            template=template.Name,
            guild_id=interaction.guild.id,
        )

        try:
            user_profile = await backend.s3_backend.fetch_profile(
                template, guild_id, author.id
            )

            await interaction.response.send_message(
                embed=util.build_embed_for_template(
                    template, util.get_nick_or_name(author), user_profile
                ),
            )
        except KeyError:
            if author.id == interaction.user.id:
                await interaction.response.send_message(
                    f"Whoops! Unable to find a profile for you. To set one up run /edit{template.ShortName}",
                    ephemeral=True,
                )
            else:
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


def _getsavecommand(template: Template) -> discord.app_commands.Command[Any, Any, Any]:
    @app_commands.command(
        name="save" + template.ShortName + "image",
        description=f"Add a profile image to {template.Name}",
    )
    async def saveimage(
        interaction: discord.Interaction, image: discord.Attachment
    ) -> None:
        if not interaction.guild:
            raise TypeError("No Guild Found")
        if not interaction.user:
            raise TypeError("No User Found")

        user_profile = None
        try:
            user_profile = await backend.s3_backend.fetch_profile(
                template, interaction.guild.id, interaction.user.id
            )
        except KeyError:  # It's ok if we don't get anything
            pass
        if not user_profile:
            user_profile = dict()

        user_profile[template.Image.Name] = image.proxy_url

        web_url, error = await backend.s3_backend.save_profile(
            template, interaction.guild.id, interaction.user.id, user_profile
        )

        if error:
            await interaction.response.send_message(error, ephemeral=True)
            return

        await interaction.response.send_message(
            embed=util.build_embed_for_template(
                template, util.get_nick_or_name(interaction.user), user_profile
            ),
            ephemeral=True,
        )

    return saveimage


def _getsavemenu(guild_id: int, template: Template) -> discord.app_commands.ContextMenu:
    @app_commands.context_menu(
        name=f"Save as {template.Name} Image",
    )
    async def saveimage(
        interaction: discord.Interaction, message: discord.Message
    ) -> None:
        if not interaction.guild:
            raise TypeError("No Guild Found")
        if not interaction.user:
            raise TypeError("No User Found")
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
                user_profile = await backend.s3_backend.fetch_profile(
                    template, guild_id, interaction.user.id
                )
            except KeyError:  # It's ok if we don't get anything
                pass
            if not user_profile:
                user_profile = dict()

            found_attachments = 0
            found_video_error = ""
            for attachment in message.attachments:
                if attachment.content_type and attachment.content_type.startswith(
                    "image/"
                ):
                    log.info(
                        "Found image in attachments",
                        image_url=attachment.proxy_url,
                        user_id=interaction.user.id,
                        template=template.Name,
                        guild_id=interaction.guild.id,
                    )
                    found_attachments += 1
                    user_profile[template.Image.Name] = attachment.proxy_url
                elif attachment.content_type and attachment.content_type.startswith(
                    "video/"
                ):
                    found_video_error = (
                        f"It looks like that attachment was a "
                        f"video ({attachment.content_type}), "
                        "we can only use images. Discord often "
                        "uses mp4s instead of gifs."
                    )

            for embed in message.embeds:
                if embed.image:
                    if embed.image.proxy_url:
                        found_attachments += 1
                        user_profile[template.Image.Name] = embed.image.proxy_url
                        log.info(
                            "Found image in embeds",
                            image_url=embed.image.proxy_url,
                            user_id=interaction.user.id,
                            template=template.Name,
                            guild_id=interaction.guild.id,
                        )
                elif embed.video:
                    found_video_error = (
                        "It looks like that attachment was a "
                        "video, unfortunately we can only use images. "
                        "Discord will often use a mp4 instead of a gif."
                    )

            if found_video_error and found_attachments == 0:
                await interaction.response.send_message(
                    found_video_error, ephemeral=True
                )
                return

            if found_attachments > 1:
                await interaction.response.send_message(
                    (
                        f"I found {found_attachments} images in that post, but I'm not sure which one to use! "
                        "Please make a post with just a single image."
                    ),
                    ephemeral=True,
                )
                return

            if found_attachments == 0:
                await interaction.response.send_message(
                    "I didn't find an image to save in that post :(", ephemeral=True
                )
                return

            web_url, error = await backend.s3_backend.save_profile(
                template, interaction.guild.id, interaction.user.id, user_profile
            )

            if error:
                await interaction.response.send_message(error, ephemeral=True)
                return

            await interaction.response.send_message(
                embed=util.build_embed_for_template(
                    template, util.get_nick_or_name(interaction.user), user_profile
                ),
                ephemeral=True,
            )

        except Exception:
            await interaction.response.send_message(
                "Oops! Something went wrong.", ephemeral=True
            )
            traceback.print_exception(*sys.exc_info())

    return saveimage


def get_commands() -> Dict[
    int,
    List[
        Union[
            discord.app_commands.ContextMenu,
            discord.app_commands.Command[Any, Any, Any],
        ]
    ],
]:
    results: Dict[
        int,
        List[
            Union[
                discord.app_commands.ContextMenu,
                discord.app_commands.Command[Any, Any, Any],
            ]
        ],
    ] = dict()
    for guild_id, templates in all_templates().items():
        results[guild_id] = []
        for template in templates:
            results[guild_id].append(_geteditfunc(guild_id, template))
            results[guild_id].append(_getgetfunc(template))
            for cmd in _getgetmenu(guild_id, template):
                results[guild_id].append(cmd)
            results[guild_id].append(_getsavemenu(guild_id, template))
            results[guild_id].append(_getsavecommand(template))
            results[guild_id].append(_getdeletefunc(template))

    return results
