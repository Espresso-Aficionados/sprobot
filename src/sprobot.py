from dataclasses import dataclass
import traceback

import discord
from discord import app_commands

import boto3


# The guild in which this slash command will be registered.
# It is recommended to have a test guild to separate from your "production" bot
TEST_GUILD = discord.Object(1013566342345019512)

# client = boto3.client('s3', region_name='us-west-2')
# client.upload_file('images/image_0.jpg', 'mybucket', 'image_0.jpg')


@dataclass
class Field:
    Name: str
    Placeholder: str
    Style: discord.TextStyle
    Image: bool

# Images will be a link, users submit it, it gets uploaded to s3, we save that s3 link
def ImageField(name: str, placeholder, ) -> Field:
    return Field(name, P


@dataclass
class Template:
    fields: List[Field]


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


class EditProfile(discord.ui.Modal):
    # Our modal classes MUST subclass `discord.ui.Modal`,
    # but the title can be whatever you want.

    def __init__(self, formFill, title="Edit Profile", *args, **kwargs):
        # This must come before adding the children
        super().__init__(title=title, *args, **kwargs)

        # This will be a short input, where the user can enter their name
        # It will also have a placeholder, as denoted by the `placeholder` kwarg.
        # By default, it is required and is a short-style input which is exactly
        # what we want.
        self.add_item(
            discord.ui.TextInput(
                label="Name",
                placeholder="Your name here...",
            )
        )

        # This is a longer, paragraph style input, where user can submit feedback
        # Unlike the name, it is not required. If filled out, however, it will
        # only accept a maximum of 300 characters, as denoted by the
        # `max_length=300` kwarg.
        self.add_item(
            discord.ui.TextInput(
                label="What do you think of this new feature?",
                style=discord.TextStyle.long,
                placeholder="Type your feedback here...",
                required=False,
                default="This is a big test?",
                max_length=300,
            )
        )

    async def on_submit(self, interaction: discord.Interaction):
        for child in self.children:
            print(child.label, child.value)

        # TODO: Use the same tools to send the profile back to them, make it ephemeral
        await interaction.response.send_message(
            f"Thanks for your feedback, {interaction.user.name}!",
            ephemeral=True,
        )

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
    await interaction.response.send_modal(
        EditProfile(
            formFill=f"{interaction.user.name}#{interaction.user.discriminator}",
        )
    )

@client.tree.command(guild=TEST_GUILD, description="View yours or someone elses profile")
async def getprofile(interaction: discord.Interaction):
    # TODO: Accept username
    # TODO: ephemeral flag
    # Build an embed of the profile and send it! Make it ephemeral? 
    await interaction.response.send_message()


if __name__ == "__main__":
    client.run("NzY5MjkwMzU1NTcyODY3MDgy.GsMyp1.I6AVYxNbUIgDx5UCouLQeoHgBV-vtxsUEGrqAY")
