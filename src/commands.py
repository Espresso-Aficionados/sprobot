import traceback
import json
import typing
from typing import Dict, Any, List, Optional

import discord
from discord import app_commands
from fuzzywuzzy import process

import backend
from templates import Template, all_templates
from util import build_embed_for_template


class EditProfile(discord.ui.Modal):
    # Our modal classes MUST subclass `discord.ui.Modal`,
    # but the title can be whatever you want.

    def __init__(self, template: Template, *args: Any, **kwargs: Any):
        # This must come before adding the children
        super().__init__(title="Edit Profile", *args, **kwargs)

        self.template = template

        for field in template.Fields:
            self.add_item(
                discord.ui.TextInput(
                    label=field.Name,
                    placeholder=field.Placeholder,
                    style=field.Style,
                    max_length=1024,
                    required=False,
                )
            )

    async def on_submit(self, interaction: discord.Interaction) -> None:
        built_profile: Dict[str, str] = {}
        for child in self.children:
            if type(child) != discord.ui.TextInput:
                continue
            built_profile[child.label] = child.value

        backend.save_profile(self.template, built_profile)

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
        traceback.print_tb(error.__traceback__)


async def member_autocomplete(
    interaction: discord.Interaction,
    current: str,
) -> List[app_commands.Choice[str]]:
    if current == "":
        return []
    if not interaction.guild:
        return []

    # This should probably be cached somewhere, or disabled, especially for big guilds
    choices = []
    for member in interaction.guild.members:
        if member.nick:
            choices.append(member.nick + "#" + member.discriminator)
        choices.append(member.name + "#" + member.discriminator)

    return [
        app_commands.Choice(name=name[0], value=name[0])
        for name in process.extract(current, choices, limit=5)
    ]


def get_commands() -> List[discord.app_commands.Command[Any, Any, Any]]:
    results = []
    for template in all_templates:

        def geteditfunc(
            template: Template,
        ) -> discord.app_commands.Command[Any, Any, Any]:
            async def editfunc(interaction: discord.Interaction) -> None:
                await interaction.response.send_modal(EditProfile(template=template))

            return (
                app_commands.command(
                    name="edit" + template.ShortName,
                    description=template.Description,
                )
            )(editfunc)

        results.append(geteditfunc(template))

        def getgetfunc(
            template: Template,
        ) -> discord.app_commands.Command[Any, Any, Any]:
            @app_commands.autocomplete(name=member_autocomplete)
            async def editfunc(
                interaction: discord.Interaction, name: Optional[str]
            ) -> None:
                await interaction.response.send_modal(
                    EditProfile(template=template)
                )  # TODO: this should be GetProfile

            return (
                app_commands.command(
                    name="get" + template.ShortName,
                    description=template.Description,
                )
            )(editfunc)

        results.append(getgetfunc(template))

    return results
