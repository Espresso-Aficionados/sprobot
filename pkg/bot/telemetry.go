package bot

import (
	"context"
	"sync"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
	"github.com/sadbox/svc/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/Espresso-Aficionados/sprobot/pkg/botutil"
	"github.com/Espresso-Aficionados/sprobot/pkg/sprobot"
)

// sprobot's own metrics, besides botutil's, as Prometheus names them:
//
//	sprobot_profiles{template}   profiles stored, over the listed guilds
//	    (loadProfileOwners); template a template's short name
//	sprobot_profile_edits_total{template, outcome}   /edit saves
//	sprobot_profile_deletes_total{template, target, outcome}   deletes;
//	    target profile or image
//	sprobot_starboard_entries{state}   messages the starboard tracks; state
//	    posted (reposted) or pending (reacted to, below the threshold)
//	sprobot_starboard_posts_total   messages reposted to a starboard
//	sprobot_tickets_opened_total   ticket threads created
//	sprobot_shortcut_uses_total{shortcut}   /s uses; shortcut the name staff
//	    configured (/config shortcuts), only ever one that resolved to a
//	    configured shortcut, the first 50 names a process sees, the rest
//	    "other"; a removed shortcut's series lasts until restart
//	sprobot_temp_roles   temp-role timers running
//	sprobot_members   server members: the cached guilds' member_count, which
//	    disgo keeps current from member add and remove events (Server
//	    Members; docs/privileged-intents/sprobot.md)
//
// outcome is success or failure. The gauges are read from state at export,
// summed over the guilds this instance serves, and left out while one of
// them is unloaded.
const scope = "github.com/Espresso-Aficionados/sprobot/pkg/bot"

// sprobot's background loops, one Job each (sadbox_job_*{job_name}), made
// before telemetry.Start so its first export reports them at 0. The 30s
// heartbeat is botutil's gateway_ready.
var (
	retryFailedLoadsJob        = telemetry.NewJob("retry_failed_loads")         // 1m
	processTempRoleExpiriesJob = telemetry.NewJob("process_temp_role_expiries") // 1m
	saveTopPostersJob          = telemetry.NewJob("save_top_posters")           // 5m
	savePosterRoleJob          = telemetry.NewJob("save_poster_role")           // 5m
	saveShortcutsJob           = telemetry.NewJob("save_shortcuts")             // 5m
	saveTicketsJob             = telemetry.NewJob("save_tickets")               // 5m
	saveWelcomeJob             = telemetry.NewJob("save_welcome")               // 5m
	saveCachesJob              = telemetry.NewJob("save_caches")                // 5m
	saveStarboardJob           = telemetry.NewJob("save_starboard")             // 5m
	retryStarboardDeletesJob   = telemetry.NewJob("retry_starboard_deletes")    // 5m
	saveRenameLogsJob          = telemetry.NewJob("save_rename_logs")           // 5m
	saveTempRolesJob           = telemetry.NewJob("save_temp_roles")            // 5m
	pruneCorruptBackupsJob     = telemetry.NewJob("prune_corrupt_backups")      // 1h
	forumRemindersJob          = telemetry.NewJob("forum_reminders")            // 30m
)

var (
	templateKey = attribute.Key("template")
	targetKey   = attribute.Key("target")
	outcomeKey  = attribute.Key("outcome")
	stateKey    = attribute.Key("state")
	shortcutKey = attribute.Key("shortcut")
)

var (
	profileEdits   = botutil.NewCounter("sprobot.profile.edits", "{edit}", "Profile saves from /edit, by template and outcome.")
	profileDeletes = botutil.NewCounter("sprobot.profile.deletes", "{delete}", "Profile and profile image deletes, by template, target and outcome.")
	starboardPosts = botutil.NewCounter("sprobot.starboard.posts", "{post}", "Messages reposted to a starboard.")
	ticketsOpened  = botutil.NewCounter("sprobot.tickets.opened", "{ticket}", "Ticket threads created.")
	shortcutUses   = botutil.NewCounter("sprobot.shortcut.uses", "{use}", "Shortcut (/s) uses, by shortcut name.")
)

// maxShortcutLabels caps the shortcut label's values per process.
const maxShortcutLabels = 50

// labelCap hands out up to max distinct label values, then "other".
type labelCap struct {
	mu    sync.Mutex
	max   int
	names map[string]bool
}

func (c *labelCap) label(name string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.names[name] {
		return name
	}
	if len(c.names) >= c.max {
		return "other"
	}
	if c.names == nil {
		c.names = make(map[string]bool)
	}
	c.names[name] = true
	return name
}

var shortcutLabels = &labelCap{max: maxShortcutLabels}

// shortcutAttr is the label of a configured shortcut called name.
func shortcutAttr(name string) attribute.KeyValue {
	return shortcutKey.String(shortcutLabels.label(name))
}

// Every fixed series reads 0 from the first export. Shortcut names are
// seeded once the guilds are known (seedShortcutUses) and on creation.
func init() {
	starboardPosts.Seed()
	ticketsOpened.Seed()
	shortcutUses.Seed(shortcutKey.String("other"))
	seen := map[string]bool{}
	for _, tmpls := range sprobot.AllTemplates() {
		for _, tmpl := range tmpls {
			if seen[tmpl.ShortName] {
				continue
			}
			seen[tmpl.ShortName] = true
			for _, o := range []string{"success", "failure"} {
				profileEdits.Seed(templateKey.String(tmpl.ShortName), outcomeKey.String(o))
				for _, target := range []string{"profile", "image"} {
					profileDeletes.Seed(templateKey.String(tmpl.ShortName), targetKey.String(target), outcomeKey.String(o))
				}
			}
		}
	}
}

func outcome(err error) attribute.KeyValue {
	if err != nil {
		return outcomeKey.String("failure")
	}
	return outcomeKey.String("success")
}

// recordProfileEdit counts one /edit save of tmpl; err is why it failed.
func recordProfileEdit(tmpl sprobot.Template, err error) {
	profileEdits.Add(1, templateKey.String(tmpl.ShortName), outcome(err))
}

// recordProfileDelete counts one delete of tmpl's profile or image (target).
func recordProfileDelete(tmpl sprobot.Template, target string, err error) {
	profileDeletes.Add(1, templateKey.String(tmpl.ShortName), targetKey.String(target), outcome(err))
}

// seedShortcutUses makes every shortcut of the served guilds exported at 0,
// so its first use after a restart is counted.
func (b *Bot) seedShortcutUses() {
	b.shortcuts.each(func(_ snowflake.ID, st *shortcutState) {
		st.mu.Lock()
		defer st.mu.Unlock()
		for name := range st.Shortcuts {
			shortcutUses.Seed(shortcutAttr(name))
		}
	})
}

// registerStats registers the callback that reads the gauges above from
// the bot's state at each export. Run calls it; the registration ends with
// the process.
func (b *Bot) registerStats() (metric.Registration, error) {
	m := otel.Meter(scope)
	profiles, err := m.Int64ObservableGauge("sprobot.profiles",
		metric.WithUnit("{profile}"),
		metric.WithDescription("Profiles stored, by template."))
	if err != nil {
		return nil, err
	}
	starboard, err := m.Int64ObservableGauge("sprobot.starboard.entries",
		metric.WithUnit("{message}"),
		metric.WithDescription("Messages the starboard tracks, by state: posted or pending."))
	if err != nil {
		return nil, err
	}
	tempRoles, err := m.Int64ObservableGauge("sprobot.temp_roles",
		metric.WithUnit("{timer}"),
		metric.WithDescription("Temp-role timers running."))
	if err != nil {
		return nil, err
	}
	members, err := m.Int64ObservableGauge("sprobot.members",
		metric.WithUnit("{member}"),
		metric.WithDescription("Server members, over the served guilds."))
	if err != nil {
		return nil, err
	}
	return m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		served := b.servedGuilds()
		if counts, ok := b.profileNames.counts(served, b.templates); ok {
			for tmpl, n := range counts {
				o.ObserveInt64(profiles, int64(n), metric.WithAttributes(templateKey.String(tmpl)))
			}
		}
		if states, ok := b.starboard.servedLoaded(); ok {
			var posted, pending int
			for _, st := range states {
				st.mu.Lock()
				p, q := st.entryCounts()
				st.mu.Unlock()
				posted, pending = posted+p, pending+q
			}
			o.ObserveInt64(starboard, int64(posted), metric.WithAttributes(stateKey.String("posted")))
			o.ObserveInt64(starboard, int64(pending), metric.WithAttributes(stateKey.String("pending")))
		}
		if states, ok := b.tempRoles.servedLoaded(); ok {
			var n int
			for _, st := range states {
				st.mu.Lock()
				n += len(st.Entries)
				st.mu.Unlock()
			}
			o.ObserveInt64(tempRoles, int64(n))
		}
		if n, ok := memberCount(b.guildCache, served); ok {
			o.ObserveInt64(members, int64(n))
		}
		return nil
	}, profiles, starboard, tempRoles, members)
}

// memberCount sums the member counts of guildIDs from the guild cache. ok
// is false without a guild, or while one isn't cached.
func memberCount(guilds *cappedCache[discord.CacheGuild], guildIDs []snowflake.ID) (n int, ok bool) {
	if len(guildIDs) == 0 {
		return 0, false
	}
	for _, id := range guildIDs {
		g, cached := guilds.Get(id)
		if !cached {
			return 0, false
		}
		n += g.MemberCount
	}
	return n, true
}

// componentNames and modalNames are the custom-ID prefixes interactions are
// labelled by (botutil.TraceComponents, TraceModals): the routes of
// handleComponentInteraction and handleModalSubmit. A custom ID matching
// none is "other".
var (
	componentNames = []string{
		"selfrole", "ticket_open", "ticket_close", "ticket_close_yes", "ticket_close_no",
		"del_cancel", "del_profile", "del_image", "del_confirm_profile", "del_confirm_image",
	}
	modalNames = []string{"edit", "sconfig_set", "welcome_set", "modlog"}
)
