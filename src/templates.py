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
    fields: List[Field]
