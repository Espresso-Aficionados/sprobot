from templates import Template

from typing import Dict

import discord


def build_embed_for_template(
    template: Template, profile: Dict[str, str]
) -> discord.Embed:
    embed = discord.Embed(title=template.Name)
    for field in template.Fields:
        field_content = profile.get(field.Name, None)
        if not field_content:
            continue
        embed.add_field(name=field.Name, value=field_content)
    embed.set_image(url="https://i.imgur.com/uwl3sj9.jpeg")
    return embed
