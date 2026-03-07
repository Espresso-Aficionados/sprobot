package sprobot

import "github.com/disgoorg/snowflake/v2"

type TextStyle int

const (
	TextStyleShort TextStyle = iota
	TextStyleLong
)

type Field struct {
	Name        string    `json:"name"`
	Placeholder string    `json:"placeholder"`
	Style       TextStyle `json:"style"`
}

type Template struct {
	Name        string  `json:"name"`
	ShortName   string  `json:"short_name"`
	Description string  `json:"description"`
	Fields      []Field `json:"fields"`
	Image       Field   `json:"image"`
}

var ProfileTemplate = Template{
	Name:        "Coffee Setup",
	ShortName:   "profile",
	Description: "Edit or Create your profile",
	Fields: []Field{
		{"Machine", "A description of your machine(s).", TextStyleLong},
		{"Grinder", "A description of your grinder(s).", TextStyleLong},
		{"Favorite Beans", "What are your favorite beans / roasts?", TextStyleLong},
		{"Location", "Where are you located?", TextStyleShort},
	},
	Image: Field{
		"Gear Picture",
		"Please put a link to an image of your machine here!",
		TextStyleShort,
	},
}

var RoasterTemplate = Template{
	Name:        "Roasting Setup",
	ShortName:   "roaster",
	Description: "Edit or Create your roasting profile",
	Fields: []Field{
		{"Roasting Machine", "A description of your machine(s).", TextStyleLong},
		{"Favorite Greens", "What are your favorite greens to work with?", TextStyleLong},
		{"Website", "Link to your website.", TextStyleShort},
		{"Location", "What are you located?", TextStyleShort},
	},
	Image: Field{
		"Gear Picture",
		"Please put a link to an image of your machine here!",
		TextStyleShort,
	},
}

func AllTemplates() map[snowflake.ID][]Template {
	return map[snowflake.ID][]Template{
		726985544038612993:  {ProfileTemplate, RoasterTemplate},
		1013566342345019512: {ProfileTemplate, RoasterTemplate},
	}
}
