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
        if field.Image:
            embed.set_image(url=field_content)
        else:
            embed.add_field(name=field.Name, value=field_content)
    return embed
