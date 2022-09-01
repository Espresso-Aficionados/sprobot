from templates import Template

from typing import Dict

import discord


def build_embed_for_template(
    template: Template, profile: Dict[str, str]
) -> discord.Embed:
    # TODO: Make this a link to a little webpage for the profile
    embed = discord.Embed(
        title=template.Name, url="https://github.com/Espresso-Aficionados/sprobot"
    )
    for field in template.Fields:
        field_content = profile.get(field.Name, None)
        if not field_content:
            continue
        if field.Image:
            embed.set_image(url=field_content)
        else:
            embed.add_field(name=field.Name, value=field_content)

    # TODO: Save this icon in the bucket
    embed.set_footer(
        text="sprobot",
        icon_url="https://avatars.githubusercontent.com/u/76916743?s=96&v=4",
    )
    return embed
