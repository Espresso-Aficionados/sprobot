import discord
from discord import app_commands

from templates import ProfileTemplate, RoasterTemplate
from commands import EditProfile

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

    async def on_ready(self) -> None:
        print(f"Logged in as {self.user}")
        print("------")

    async def setup_hook(self) -> None:
        # Sync the application command with Discord.
        await self.tree.sync(guild=TEST_GUILD)


client = MyClient()


@client.tree.command(guild=TEST_GUILD, description="Edit or Create your profile")
async def editprofile(interaction: discord.Interaction) -> None:
    # Send the modal with an instance of our `Feedback` class
    # Since modals require an interaction, they cannot be done as a response to a text command.
    # They can only be done as a response to either an application command or a button press.
    await interaction.response.send_modal(EditProfile(template=ProfileTemplate))


@client.tree.command(guild=TEST_GUILD, description="Edit or Create your profile")
async def editroaster(interaction: discord.Interaction) -> None:
    # Send the modal with an instance of our `Feedback` class
    # Since modals require an interaction, they cannot be done as a response to a text command.
    # They can only be done as a response to either an application command or a button press.
    await interaction.response.send_modal(EditProfile(template=RoasterTemplate))


@client.tree.command(
    guild=TEST_GUILD, description="View yours or someone elses profile"
)
async def getprofile(interaction: discord.Interaction) -> None:
    # TODO: Accept username
    # TODO: ephemeral flag
    # Build an embed of the profile and send it! Make it ephemeral?
    await interaction.response.send_message()


if __name__ == "__main__":
    client.run("NzY5MjkwMzU1NTcyODY3MDgy.GsMyp1.I6AVYxNbUIgDx5UCouLQeoHgBV-vtxsUEGrqAY")
