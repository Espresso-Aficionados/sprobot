from dataclasses import dataclass
from typing import Optional

@dataclass
class Link:
    shortcut: str
    link: str
    hints: Optional[list[str]] = None


wiki_links = [
    Link("coffee-reccomendations", "https://espressoaf.com/recommendations/coffee.html", hints=["beans"]),
    Link("entry-level-machines", "https://espressoaf.com/recommendations/entry-machines.html"),
    Link("midrange-machines", "https://espressoaf.com/recommendations/midrange-machines.html"),
    Link("entry-level-grinders", "https://espressoaf.com/recommendations/entry-grinders.html"),
    Link("midrange-grinders", "https://espressoaf.com/recommendations/midrange-grinders.html"),
    Link("coffee-scales", "https://espressoaf.com/recommendations/coffee-scales.html", hints=["weight"]),
    Link("titan-grinders", "https://espressoaf.com/recommendations/titan-grinders.html"),
    Link("dialing-in-beginner", "https://espressoaf.com/guides/beginner.html"),
    Link("puck-prep", "https://espressoaf.com/guides/puckprep.html", hints=["wdt"]),
    Link("espresso-profiling", "https://espressoaf.com/guides/profiling.html", hints=["profiles"]),
    Link("water", "https://espressoaf.com/guides/water.html"),
    Link("reccomended-accessories", "https://espressoaf.com/guides/accessories.html"),
    Link("machine-maintenance", "https://espressoaf.com/guides/maintenance.html"),
    Link("coffee-storage", "https://espressoaf.com/guides/storage.html"),
    Link("burr-alignment", "https://espressoaf.com/guides/alignment.html"),
    Link("preferential-extraction", "https://espressoaf.com/guides/preferential-extraction.html"),
    Link("machine-restoration-basics", "https://espressoaf.com/guides/restoration.html"),
    Link("sprobot-guide", "https://espressoaf.com/guides/sprobot.html"),
    Link("latte-art-basics", "https://espressoaf.com/guides/latteart.html"),
    Link("burr-catalog", "https://espressoaf.com/guides/burr_catalog.html"),
    Link("espresso-machine-types", "https://espressoaf.com/info/machine-types.html"),
    Link("lever-machines", "https://espressoaf.com/info/levers.html"),
    Link("glossary", "https://espressoaf.com/info/Glossary.html"),
    Link("bripe-life", "https://espressoaf.com/info/bripe.html"),
    Link("pressure-and-flow", "https://espressoaf.com/info/flow_and_pressure.html"),
    Link("extraction-theory-evenness", "https://espressoaf.com/info/extraction_evenness_theory.html"),
    Link("vario-burr-alignment", "https://espressoaf.com/manufacturers/baratza/alignment.html"),
    Link("vario-dissassembly", "https://espressoaf.com/manufacturers/baratza/vario_disassembly.html"),
    Link("vario-retention", "https://espressoaf.com/manufacturers/baratza/vario_retention.html"),
    Link("bdb-needle-valve", "https://espressoaf.com/manufacturers/breville/needle.html"),
    Link("bdb-rotary-pump", "https://espressoaf.com/manufacturers/breville/rotary.html"),
    Link("bdb-slayer-mod", "https://espressoaf.com/manufacturers/breville/slayermod.html"),
    Link("bbe-max-shot-time", "https://espressoaf.com/manufacturers/breville/images/max_shot_time.html"),
    Link("bbe-opv-reroute", "https://espressoaf.com/manufacturers/breville/opv_water.html"),
    Link("infuser-dimmer-mod", "https://espressoaf.com/manufacturers/breville/dimmer.html"),
    Link("breville-pi-mode", "https://espressoaf.com/manufacturers/breville/preinfusion.html"),
    Link("la-pavoni-lever-basics", "https://espressoaf.com/manufacturers/la-pavoni/lever-basics.html"),
    Link("uniterra-nomad-info", "https://espressoaf.com/manufacturers/uniterra/nomad.html"),
    Link("homepage", "https://espressoaf.com", hints=["eaf", "wiki"]),
]
