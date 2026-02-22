package threadbot

import "github.com/disgoorg/snowflake/v2"

func getGuildIDs(env string) []snowflake.ID {
	switch env {
	case "prod":
		return []snowflake.ID{
			726985544038612993,
		}
	case "dev":
		return []snowflake.ID{
			1013566342345019512,
		}
	default:
		return nil
	}
}
