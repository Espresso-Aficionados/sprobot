package bot

import (
	"github.com/sadbox/sprobot/pkg/sprobot"
)

func (b *Bot) loadTemplates() {
	if tmpls := sprobot.AllTemplates(b.Env); tmpls != nil {
		for gid, t := range tmpls {
			b.templates[gid] = t
		}
	}
}

func (b *Bot) loadSelfroles() {
	if cfgs := getSelfroleConfig(b.Env); cfgs != nil {
		for gid, c := range cfgs {
			b.selfroles[gid] = c
		}
	}
}

func (b *Bot) loadTicketConfigs() {
	if cfgs := getTicketConfig(b.Env); cfgs != nil {
		for gid, c := range cfgs {
			b.ticketConfigs[gid] = c
		}
	}
}
