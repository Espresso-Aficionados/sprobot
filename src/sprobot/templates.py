from __future__ import annotations

import os
from dataclasses import dataclass
from typing import Dict, List

import discord


@dataclass
class Field:
    Name: str
    Placeholder: str
    Style: discord.TextStyle


@dataclass
class Template:
    Name: str
    ShortName: str
    Description: str
    Fields: List[Field]
    Image: Field


ProfileTemplate = Template(
    Name="Coffee Setup",
    ShortName="profile",
    Description="Edit or Create your profile",
    Fields=[
        Field(
            "Machine",
            "A description of your machine(s).",
            discord.TextStyle.long,
        ),
        Field(
            "Grinder",
            "A description of your grinder(s).",
            discord.TextStyle.long,
        ),
        Field(
            "Favorite Beans",
            "What are your favorite beans / roasts?",
            discord.TextStyle.long,
        ),
        Field(
            "Pronouns",
            "What are your preferred pronouns?",
            discord.TextStyle.short,
        ),
        Field(
            "Location",
            "Where are you located?",
            discord.TextStyle.short,
        ),
    ],
    Image=Field(
        "Gear Picture",
        "Please put a link to an image of your machine here!",
        discord.TextStyle.short,
    ),
)

RoasterTemplate = Template(
    Name="Roasting Setup",
    ShortName="roaster",
    Description="Edit or Create your profile",
    Fields=[
        Field(
            "Roasting Machine",
            "A description of your machine(s).",
            discord.TextStyle.long,
        ),
        Field(
            "Favorite Greens",
            "What are your favorite greens to work with?",
            discord.TextStyle.long,
        ),
        Field(
            "Website",
            "Link to your website.",
            discord.TextStyle.short,
        ),
        Field(
            "Location",
            "What are you located?",
            discord.TextStyle.short,
        ),
    ],
    Image=Field(
        "Gear Picture",
        "Please put a link to an image of your machine here!",
        discord.TextStyle.short,
    ),
)


def all_templates() -> Dict[int, List[Template]]:
    env = os.environ.get("SPROBOT_ENV")
    if env == "dev":
        return {
            1013566342345019512: [ProfileTemplate, RoasterTemplate],
        }
    elif env == "prod":
        return {
            726985544038612993: [ProfileTemplate, RoasterTemplate],
        }
    else:
        return {}
