import discord
from discord import app_commands

from commands import get_commands

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


def main() -> None:
    client = MyClient()

    for command in get_commands():
        client.tree.add_command(command, guild=TEST_GUILD)

    client.run("NzY5MjkwMzU1NTcyODY3MDgy.GsMyp1.I6AVYxNbUIgDx5UCouLQeoHgBV-vtxsUEGrqAY")


if __name__ == "__main__":
    main()
