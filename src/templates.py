from dataclasses import dataclass
from typing import List

import discord


@dataclass
class Field:
    Name: str
    Placeholder: str
    Style: discord.TextStyle
    Image: bool


@dataclass
class Template:
    Name: str
    ShortName: str
    Description: str
    Fields: List[Field]


ProfileTemplate = Template(
    Name="Coffee Setup",
    ShortName="profile",
    Description="Edit or Create your profile",
    Fields=[
        Field(
            "Machine",
            "A description of your machine(s).",
            discord.TextStyle.long,
            False,
        ),
        Field(
            "Grinder",
            "A description of your grinder(s).",
            discord.TextStyle.long,
            False,
        ),
        Field(
            "Favorite Beans",
            "What are your favorite beans / roasts?",
            discord.TextStyle.long,
            False,
        ),
        Field(
            "Pronouns",
            "What are your preferred pronouns?",
            discord.TextStyle.short,
            False,
        ),
        Field(
            "Gear Picture",
            "Please put a link to an image of your machine here!",
            discord.TextStyle.short,
            True,
        ),
    ],
)

RoasterTemplate = Template(
    Name="Roasting Setup",
    ShortName="roaster",
    Description="Edit or Create your profile",
    Fields=[
        Field(
            "Roaster",
            "A description of your machine(s).",
            discord.TextStyle.long,
            False,
        ),
        Field(
            "Gear Picture",
            "Please put a link to an image of your machine here!",
            discord.TextStyle.short,
            True,
        ),
    ],
)

all_templates = [ProfileTemplate, RoasterTemplate]
