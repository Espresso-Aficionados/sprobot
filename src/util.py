from typing import Dict, Union
import random
import string

from templates import Template

import discord


def build_embed_for_template(
    template: Template, username: str, profile: Dict[str, str]
) -> discord.Embed:
    # TODO: Make this a link to a little webpage for the profile
    embed = discord.Embed(
        title=f"{template.Name} for {username}",
        url="https://github.com/Espresso-Aficionados/sprobot",
    )
    for field in template.Fields:
        field_content = profile.get(field.Name, None)
        if not field_content:
            continue
        # This is a hack to get around discord caching the URL when people change their profile pic
        if field.Image:
            embed.set_image(
                url=(
                    field_content
                    + "?"
                    + "".join(random.choice(string.ascii_letters) for i in range(10))
                )
            )
        else:
            embed.add_field(name=field.Name, value=field_content)

    embed.set_footer(
        text="sprobot",
        icon_url="https://profile-bot.us-southeast-1.linodeobjects.com/76916743.gif",
    )
    return embed


def get_nick_or_name(person: Union[discord.Member, discord.User]) -> str:
    if type(person) == discord.Member:
        if person.nick:
            return person.nick
    return person.name
