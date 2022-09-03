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
            "Location",
            "What are you located?",
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
            "Roasting Machine",
            "A description of your machine(s).",
            discord.TextStyle.long,
            False,
        ),
        Field(
            "Favorite Greens",
            "What are your favorite greens to work with?",
            discord.TextStyle.long,
            False,
        ),
        Field(
            "Website",
            "Link to your website.",
            discord.TextStyle.short,
            False,
        ),
        Field(
            "Location",
            "What are you located?",
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

all_templates = {
    1013566342345019512: [ProfileTemplate, RoasterTemplate],
    726985544038612993: [ProfileTemplate, RoasterTemplate],
}
