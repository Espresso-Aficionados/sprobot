import asyncio
import os
from dataclasses import dataclass
from datetime import datetime, timedelta
from typing import Dict

import discord
import structlog
from commands import get_commands
from discord import app_commands


@dataclass
class ThreadHelpInfo:
    helper_id: int
    link_to_post: str
    max_thread_age: timedelta = timedelta(hours=24)
    history_limit: int = 50


def get_thread_help_config() -> Dict[int, ThreadHelpInfo]:
    env = os.environ.get("SPROBOT_ENV")
    if env == "prod":
        return {
            1019753326469980262: ThreadHelpInfo(
                helper_id=1020401507121774722,
                link_to_post="https://discord.com/channels/726985544038612993/727325278820368456/1020402429717663854",
            )
        }
    elif env == "dev":
        return {
            1019680268229021807: ThreadHelpInfo(
                helper_id=1015493549430685706,
                link_to_post="https://discord.com/channels/1013566342345019512/1019680095893471322/1020431232129048667",
                max_thread_age=timedelta(minutes=5),
                history_limit=5,
            )
        }
    else:
        return {}


class MyClient(discord.Client):
    def __init__(self) -> None:
        # Just default intents and a `discord.Client` instance
        # We don't need a `commands.Bot` instance because we are not
        # creating text-based commands.
        intents = discord.Intents.default()
        intents.members = True
        super().__init__(intents=intents)

        # We need an `discord.app_commands.CommandTree` instance
        # to register application commands (slash commands in this case)
        self.tree = app_commands.CommandTree(self)

    async def on_ready(self) -> None:
        log = structlog.get_logger()
        log.info(f"Logged in as {self.user}", user=self.user)
        print(
            f"Invite to server with: https://discord.com/api/oauth2/authorize?client_id={self.application_id}"
            "&permissions=277025639488&scope=bot"
        )

    async def setup_hook(self) -> None:
        for guild_id, commands in get_commands().items():
            guild = discord.Object(guild_id)
            for command in commands:
                self.tree.add_command(command, guild=guild)
            await self.tree.sync(guild=guild)

        self.skip_thread_list: Dict[int, str] = dict()
        self.bg_task = self.loop.create_task(self.send_forum_reminder())

    async def send_forum_reminder(self) -> None:
        while True:
            log = structlog.get_logger()
            try:
                await self._send_forum_reminder()
            except Exception:
                log.exception("Unhandled exception")
                await asyncio.sleep(10)

    async def _send_forum_reminder(self) -> None:
        await self.wait_until_ready()
        log = structlog.get_logger()
        while not self.is_closed():
            for channel_id, info in get_thread_help_config().items():
                channel = self.get_channel(channel_id)
                if not channel:
                    log.info(
                        f"Unknown channel to check for old forum posts: {channel_id}"
                    )
                    continue

                if type(channel) is not discord.ForumChannel:
                    log.info(
                        f"Channel {channel_id} is not a ForumChannel, it is a {type(channel)}"
                    )
                    continue

                log.info(
                    "Scanning for threads",
                    guild_name=channel.guild.name,
                    guild_id=channel.guild.id,
                    channel_name=channel.name,
                    channel_id=channel.id,
                )
                for thread in channel.threads:
                    if thread.id in self.skip_thread_list:
                        log.info(
                            f"Thread is in the skip_list, reason: {self.skip_thread_list[thread.id]}",
                            guild_name=channel.guild.name,
                            guild_id=channel.guild.id,
                            channel_name=channel.name,
                            channel_id=channel.id,
                            thread_id=thread.id,
                            thread_name=thread.name,
                        )
                        continue

                    if thread.archived or thread.locked:
                        log.info(
                            "Thread is locked, skipping",
                            guild_name=channel.guild.name,
                            guild_id=channel.guild.id,
                            channel_name=channel.name,
                            channel_id=channel.id,
                            thread_id=thread.id,
                            thread_name=thread.name,
                        )
                        continue

                    if not thread.created_at:
                        log.info(
                            "Thread doesnt have a created_at, should be impossible",
                            guild_name=channel.guild.name,
                            guild_id=channel.guild.id,
                            channel_name=channel.name,
                            channel_id=channel.id,
                            thread_id=thread.id,
                            thread_name=thread.name,
                        )
                        continue

                    now = datetime.now(thread.created_at.tzinfo)
                    thread_age = now - thread.created_at
                    if thread_age < info.max_thread_age:
                        log.info(
                            f"Thread is only {thread_age} old, waiting until it is {info.max_thread_age}, skipping",
                            guild_name=channel.guild.name,
                            guild_id=channel.guild.id,
                            channel_name=channel.name,
                            channel_id=channel.id,
                            thread_id=thread.id,
                            thread_name=thread.name,
                        )
                        continue

                    found_non_op_author = False
                    number_of_posts_searched = 0
                    async for message in thread.history(limit=info.history_limit):
                        number_of_posts_searched += 1
                        if message.author.id != thread.owner_id:
                            found_non_op_author = True

                    if found_non_op_author:
                        reason = "Thread has a reply from a non-op author, skipping"
                        log.info(
                            reason,
                            guild_name=channel.guild.name,
                            guild_id=channel.guild.id,
                            channel_name=channel.name,
                            channel_id=channel.id,
                            thread_id=thread.id,
                            thread_name=thread.name,
                        )
                        self.skip_thread_list[thread.id] = reason
                        continue

                    if number_of_posts_searched >= info.history_limit:
                        reason = f"Thread has too many reples (>{info.history_limit}), skipping"
                        log.info(
                            reason,
                            guild_name=channel.guild.name,
                            guild_id=channel.guild.id,
                            channel_name=channel.name,
                            channel_id=channel.id,
                            thread_id=thread.id,
                            thread_name=thread.name,
                        )
                        self.skip_thread_list[thread.id] = reason
                        continue

                    log.info(
                        "Sending help prompt",
                        guild_name=channel.guild.name,
                        guild_id=channel.guild.id,
                        channel_name=channel.name,
                        channel_id=channel.id,
                        thread_id=thread.id,
                        thread_name=thread.name,
                    )

                    help_message = (
                        "It looks like nobody has responded even though this thread has been open for a while. "
                        f"Maybe one of our <@&{info.helper_id}> could help?"
                    )

                    embed_to_send = discord.Embed()
                    embed_to_send.description = (
                        f"Want to be part of the <@&{info.helper_id}>? Sign up by reacting to this [post in #info]"
                        f"({info.link_to_post})"
                    )
                    await thread.send(content=help_message, embed=embed_to_send)

                    # Once we've sent the message, don't bother checking again
                    self.skip_thread_list[
                        thread.id
                    ] = f"Already sent a response to {thread.name}"

            await asyncio.sleep(30 * 60)  # Every 30 minutes


def main() -> None:
    client = MyClient()
    bot_token = os.environ.get("SPROBOT_DISCORD_TOKEN")
    if not bot_token:
        raise ValueError("Missing bot token: SPROBOT_DISCORD_TOKEN")
    client.run(bot_token)


if __name__ == "__main__":
    main()
