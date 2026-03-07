package bot

import (
	"github.com/sadbox/sprobot/pkg/sprobot"
)

func (b *Bot) loadTemplates() {
	for gid, t := range sprobot.AllTemplates() {
		b.templates[gid] = t
	}
}

func (b *Bot) loadSelfroles() {
	for gid, c := range getSelfroleConfig() {
		b.selfroles[gid] = c
	}
}

func (b *Bot) loadTicketConfigs() {
	for gid, c := range getTicketConfig() {
		b.ticketConfigs[gid] = c
	}
}
