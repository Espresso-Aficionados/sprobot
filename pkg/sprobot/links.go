package sprobot

type Link struct {
	Shortcut string
	URL      string
	Hints    []string
}

var WikiLinks = []Link{
	{"coffee-reccomendations", "https://espressoaf.com/recommendations/coffee.html", []string{"beans"}},
	{"entry-level-machines", "https://espressoaf.com/recommendations/entry-machines.html", nil},
	{"midrange-machines", "https://espressoaf.com/recommendations/midrange-machines.html", nil},
	{"entry-level-grinders", "https://espressoaf.com/recommendations/entry-grinders.html", nil},
	{"midrange-grinders", "https://espressoaf.com/recommendations/midrange-grinders.html", nil},
	{"coffee-scales", "https://espressoaf.com/recommendations/coffee-scales.html", []string{"weight"}},
	{"titan-grinders", "https://espressoaf.com/recommendations/titan-grinders.html", nil},
	{"dialing-in-beginner", "https://espressoaf.com/guides/beginner.html", nil},
	{"puck-prep", "https://espressoaf.com/guides/puckprep.html", []string{"wdt"}},
	{"espresso-profiling", "https://espressoaf.com/guides/profiling.html", []string{"profiles"}},
	{"water", "https://espressoaf.com/guides/water.html", nil},
	{"reccomended-accessories", "https://espressoaf.com/guides/accessories.html", nil},
	{"machine-maintenance", "https://espressoaf.com/guides/maintenance.html", nil},
	{"coffee-storage", "https://espressoaf.com/guides/storage.html", nil},
	{"burr-alignment", "https://espressoaf.com/guides/alignment.html", nil},
	{"preferential-extraction", "https://espressoaf.com/guides/preferential-extraction.html", nil},
	{"machine-restoration-basics", "https://espressoaf.com/guides/restoration.html", nil},
	{"sprobot-guide", "https://espressoaf.com/guides/sprobot.html", nil},
	{"latte-art-basics", "https://espressoaf.com/guides/latteart.html", nil},
	{"burr-catalog", "https://espressoaf.com/guides/burr_catalog.html", nil},
	{"espresso-machine-types", "https://espressoaf.com/info/machine-types.html", nil},
	{"lever-machines", "https://espressoaf.com/info/levers.html", nil},
	{"glossary", "https://espressoaf.com/info/Glossary.html", nil},
	{"bripe-life", "https://espressoaf.com/info/bripe.html", nil},
	{"pressure-and-flow", "https://espressoaf.com/info/flow_and_pressure.html", nil},
	{"extraction-theory-evenness", "https://espressoaf.com/info/extraction_evenness_theory.html", nil},
	{"vario-burr-alignment", "https://espressoaf.com/manufacturers/baratza/alignment.html", nil},
	{"vario-dissassembly", "https://espressoaf.com/manufacturers/baratza/vario_disassembly.html", nil},
	{"vario-retention", "https://espressoaf.com/manufacturers/baratza/vario_retention.html", nil},
	{"bdb-needle-valve", "https://espressoaf.com/manufacturers/breville/needle.html", nil},
	{"bdb-rotary-pump", "https://espressoaf.com/manufacturers/breville/rotary.html", nil},
	{"bdb-slayer-mod", "https://espressoaf.com/manufacturers/breville/slayermod.html", nil},
	{"bbe-max-shot-time", "https://espressoaf.com/manufacturers/breville/images/max_shot_time.html", nil},
	{"bbe-opv-reroute", "https://espressoaf.com/manufacturers/breville/opv_water.html", nil},
	{"infuser-dimmer-mod", "https://espressoaf.com/manufacturers/breville/dimmer.html", nil},
	{"breville-pi-mode", "https://espressoaf.com/manufacturers/breville/preinfusion.html", nil},
	{"la-pavoni-lever-basics", "https://espressoaf.com/manufacturers/la-pavoni/lever-basics.html", nil},
	{"uniterra-nomad-info", "https://espressoaf.com/manufacturers/uniterra/nomad.html", nil},
	{"homepage", "https://espressoaf.com", []string{"eaf", "wiki"}},
}
