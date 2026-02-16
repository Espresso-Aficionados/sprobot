package sprobot

type TextStyle int

const (
	TextStyleShort TextStyle = iota
	TextStyleLong
)

type Field struct {
	Name        string
	Placeholder string
	Style       TextStyle
}

type Template struct {
	Name        string
	ShortName   string
	Description string
	Fields      []Field
	Image       Field
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
	Description: "Edit or Create your profile",
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

func AllTemplates(env string) map[int64][]Template {
	switch env {
	case "dev":
		return map[int64][]Template{
			1013566342345019512: {ProfileTemplate, RoasterTemplate},
		}
	case "prod":
		return map[int64][]Template{
			726985544038612993: {ProfileTemplate, RoasterTemplate},
		}
	default:
		return nil
	}
}
