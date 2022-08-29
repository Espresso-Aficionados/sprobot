import traceback
import json
import typing
from typing import Dict

import discord
from discord import app_commands

from templates import Template, ProfileTemplate, RoasterTemplate

# import boto3


# The guild in which this slash command will be registered.
# It is recommended to have a test guild to separate from your "production" bot
TEST_GUILD = discord.Object(1013566342345019512)

# client = boto3.client('s3', region_name='us-west-2')
# client.upload_file('images/image_0.jpg', 'mybucket', 'image_0.jpg')


class MyClient(discord.Client):
    def __init__(self) -> None:
        # Just default intents and a `discord.Client` instance
        # We don't need a `commands.Bot` instance because we are not
        # creating text-based commands.
        intents = discord.Intents.default()
        super().__init__(intents=intents)

        # We need an `discord.app_commands.CommandTree` instance
        # to register application commands (slash commands in this case)
        self.tree = app_commands.CommandTree(self)

    async def on_ready(self):
        print(f"Logged in as {self.user} (ID: {self.user.id})")
        print("------")

    async def setup_hook(self) -> None:
        # Sync the application command with Discord.
        await self.tree.sync(guild=TEST_GUILD)


def build_embed_for_template(template: Template, profile: dict):
    embed = discord.Embed(title=template.Name)
    for field in template.Fields:
        field_content = profile.get(field.Name, None)
        if not field_content:
            continue
        embed.add_field(name=field.Name, value=field_content)
    return embed


class EditProfile(discord.ui.Modal):
    # Our modal classes MUST subclass `discord.ui.Modal`,
    # but the title can be whatever you want.

    def __init__(self, template, *args, **kwargs):
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

    async def on_submit(self, interaction: discord.Interaction):
        built_profile: Dict[str, str] = {}
        for child in self.children:
            if type(child) == discord.ui.TextInput:
                built_profile[child.label] = child.value

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


client = MyClient()


@client.tree.command(guild=TEST_GUILD, description="Edit or Create your profile")
async def editprofile(interaction: discord.Interaction):
    # Send the modal with an instance of our `Feedback` class
    # Since modals require an interaction, they cannot be done as a response to a text command.
    # They can only be done as a response to either an application command or a button press.
    await interaction.response.send_modal(EditProfile(template=ProfileTemplate))


@client.tree.command(guild=TEST_GUILD, description="Edit or Create your profile")
async def editroaster(interaction: discord.Interaction):
    # Send the modal with an instance of our `Feedback` class
    # Since modals require an interaction, they cannot be done as a response to a text command.
    # They can only be done as a response to either an application command or a button press.
    await interaction.response.send_modal(EditProfile(template=RoasterTemplate))


@client.tree.command(
    guild=TEST_GUILD, description="View yours or someone elses profile"
)
async def getprofile(interaction: discord.Interaction):
    # TODO: Accept username
    # TODO: ephemeral flag
    # Build an embed of the profile and send it! Make it ephemeral?
    await interaction.response.send_message()


if __name__ == "__main__":
    client.run("NzY5MjkwMzU1NTcyODY3MDgy.GsMyp1.I6AVYxNbUIgDx5UCouLQeoHgBV-vtxsUEGrqAY")
