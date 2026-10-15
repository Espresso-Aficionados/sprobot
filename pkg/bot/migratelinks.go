package bot

// /config migrate-links rewrites links in messages the bot posted while files
// were linked straight from the (now private) bucket, pointing them at the web
// server's stable routes. It is a one-off: remove it, and the
// linkMigrations field, once every guild has run it.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
	"github.com/sadbox/sprobot/pkg/s3client"
)

// migrateLinksTimeout bounds a whole run, which edits at Discord's per-channel
// rate limit.
const migrateLinksTimeout = 6 * time.Hour

// migrateReportExamples caps each list in the report.
const migrateReportExamples = 10

// linkPattern matches URLs in message text and markdown links.
var linkPattern = regexp.MustCompile("https?://[^\\s<>()\\[\\]\"'|*`]+")

type pendingEdit struct {
	channelID, messageID snowflake.ID
	update               discord.MessageUpdate
}

type linkMigration struct {
	b       *Bot
	ctx     context.Context
	guildID snowflake.ID
	apply   bool

	profiles map[string]string // guild/template/user -> current image URL, "" if none
	pending  []pendingEdit

	scanned, modLogScanned int
	files, profileImages   int
	missingProfiles        int
	unknown                []string
	unknownCount           int
	edited                 int
	failed                 []string
	leftUnarchived         []snowflake.ID
	scanErrors             []string
	stopped                error
}

func (b *Bot) handleMigrateLinks(e *events.ApplicationCommandInteractionCreate) {
	data, ok := e.Data.(discord.SlashCommandInteractionData)
	if !ok || data.SubCommandName == nil || e.GuildID() == nil {
		return
	}
	if m := e.Member(); m == nil || !m.Permissions.Has(discord.PermissionManageGuild) {
		botutil.RespondEphemeral(e, "Migrating links needs the Manage Server permission.")
		return
	}
	apply := *data.SubCommandName == "run"
	guildID := *e.GuildID()
	channelID := e.Channel().ID()

	if _, running := b.linkMigrations.LoadOrStore(guildID, struct{}{}); running {
		botutil.RespondEphemeral(e, "A link migration is already running in this server.")
		return
	}
	b.Log.Info("Link migration started", "user_id", e.User().ID, "guild_id", guildID, "apply", apply)

	mode := "Scanning (dry run, nothing will be edited)"
	if apply {
		mode = "Migrating links"
	}
	botutil.RespondEphemeral(e, mode+". This can take a while; the report will be posted in this channel.")

	go func() {
		defer b.linkMigrations.Delete(guildID)
		defer func() {
			if r := recover(); r != nil {
				b.Log.Error("Link migration panicked", "panic", r)
			}
		}()

		ctx, cancel := context.WithTimeout(context.Background(), migrateLinksTimeout)
		defer cancel()
		go func() {
			select {
			case <-b.stop:
				cancel()
			case <-ctx.Done():
			}
		}()

		m := &linkMigration{b: b, ctx: ctx, guildID: guildID, apply: apply, profiles: map[string]string{}}
		m.scan()
		if apply {
			m.applyEdits()
		}
		b.Log.Info("Link migration finished", "guild_id", guildID, "apply", apply,
			"scanned", m.scanned, "to_edit", len(m.pending), "edited", m.edited, "failed", len(m.failed))

		if _, err := b.Client.Rest.CreateMessage(channelID, discord.MessageCreate{
			Content:         m.report(),
			AllowedMentions: &discord.AllowedMentions{},
		}); err != nil {
			b.Log.Error("Failed to post link migration report", "error", err)
		}
	}()
}

// scan collects edits for every bot message with links to migrate. The mod
// log forum is walked thread by thread, since its links matter most and search
// may miss old or unindexed threads; the rest of the guild is searched.
func (m *linkMigration) scan() {
	botID := m.b.Client.ID()
	seen := map[snowflake.ID]bool{}
	consider := func(msg discord.Message) {
		if msg.Author.ID != botID || seen[msg.ID] {
			return
		}
		seen[msg.ID] = true
		m.scanned++
		if update, ok := m.plan(msg); ok {
			m.pending = append(m.pending, pendingEdit{channelID: msg.ChannelID, messageID: msg.ID, update: update})
		}
	}

	before := m.scanned
	m.scanModLog(consider)
	m.modLogScanned = m.scanned - before
	m.scanSearch(consider)
}

func (m *linkMigration) scanModLog(consider func(discord.Message)) {
	st := m.b.modLog.get(m.guildID)
	if st == nil {
		return
	}
	st.mu.Lock()
	forumID := st.ChannelID
	st.mu.Unlock()
	if forumID == 0 {
		return
	}

	var threads []snowflake.ID
	active, err := m.b.Client.Rest.GetActiveGuildThreads(m.guildID, rest.WithCtx(m.ctx))
	if err != nil {
		m.scanError("listing active mod log threads", err)
	} else {
		for _, t := range active.Threads {
			if p := t.ParentID(); p != nil && *p == forumID {
				threads = append(threads, t.ID())
			}
		}
	}
	before := time.Time{}
	for {
		page, err := m.b.Client.Rest.GetPublicArchivedThreads(forumID, before, 0, rest.WithCtx(m.ctx))
		if err != nil {
			m.scanError("listing archived mod log threads", err)
			break
		}
		for _, t := range page.Threads {
			threads = append(threads, t.ID())
		}
		if !page.HasMore || len(page.Threads) == 0 {
			break
		}
		before = page.Threads[len(page.Threads)-1].ThreadMetadata.ArchiveTimestamp
	}

	for _, threadID := range threads {
		var oldest snowflake.ID
		for {
			msgs, err := m.b.Client.Rest.GetMessages(threadID, 0, oldest, 0, 100, rest.WithCtx(m.ctx))
			if err != nil {
				m.scanError(fmt.Sprintf("reading <#%d>", threadID), err)
				break
			}
			for _, msg := range msgs {
				consider(msg)
			}
			if len(msgs) < 100 {
				break
			}
			oldest = msgs[len(msgs)-1].ID
		}
	}
}

func (m *linkMigration) scanSearch(consider func(discord.Message)) {
	includeNSFW := true
	query := discord.GuildMessagesSearch{
		AuthorIDs:   []snowflake.ID{m.b.Client.ID()},
		IncludeNSFW: &includeNSFW,
		Limit:       25,
	}
	for {
		res, err := m.b.Client.Rest.SearchGuildMessages(m.ctx, m.guildID, query)
		if err != nil {
			m.scanError("searching the server", err)
			return
		}
		// Page by moving the upper bound below the oldest result. Pages can
		// come up short, so stop only when the bound stops moving.
		prev := query.MaxID
		for _, msg := range res.Messages {
			consider(msg)
			if query.MaxID == 0 || msg.ID < query.MaxID {
				query.MaxID = msg.ID
			}
		}
		if query.MaxID == prev {
			return
		}
	}
}

func (m *linkMigration) scanError(what string, err error) {
	m.b.Log.Error("Link migration scan failed", "step", what, "error", err)
	m.scanErrors = append(m.scanErrors, fmt.Sprintf("%s: %v", what, err))
}

// plan returns the edit that migrates msg's links, if it has any.
func (m *linkMigration) plan(msg discord.Message) (discord.MessageUpdate, bool) {
	content, changed := m.rewrite(msg.Content)

	var embeds []discord.Embed
	for _, e := range msg.Embeds {
		// Only rich embeds can be sent back; the others were generated from
		// links in the content, and Discord rebuilds them after the edit.
		if e.Type != "" && e.Type != discord.EmbedTypeRich {
			continue
		}
		ne, n := m.rewriteEmbed(e)
		changed += n
		embeds = append(embeds, ne)
	}
	if changed == 0 {
		return discord.MessageUpdate{}, false
	}

	update := discord.MessageUpdate{AllowedMentions: &discord.AllowedMentions{}}
	if content != msg.Content {
		update.Content = &content
	}
	if len(embeds) > 0 {
		update.Embeds = &embeds
	}
	return update, true
}

// rewriteEmbed copies the sendable parts of e with its links migrated.
func (m *linkMigration) rewriteEmbed(e discord.Embed) (discord.Embed, int) {
	changed := 0
	rw := func(s string) string {
		out, n := m.rewrite(s)
		changed += n
		return out
	}
	out := discord.Embed{
		Title:       e.Title,
		Description: rw(e.Description),
		URL:         rw(e.URL),
		Timestamp:   e.Timestamp,
		Color:       e.Color,
	}
	if e.Author != nil {
		out.Author = &discord.EmbedAuthor{Name: e.Author.Name, URL: rw(e.Author.URL), IconURL: rw(e.Author.IconURL)}
	}
	if e.Footer != nil {
		out.Footer = &discord.EmbedFooter{Text: e.Footer.Text, IconURL: rw(e.Footer.IconURL)}
	}
	if e.Image != nil {
		out.Image = &discord.EmbedResource{URL: rw(e.Image.URL)}
	}
	if e.Thumbnail != nil {
		out.Thumbnail = &discord.EmbedResource{URL: rw(e.Thumbnail.URL)}
	}
	for _, f := range e.Fields {
		out.Fields = append(out.Fields, discord.EmbedField{Name: f.Name, Value: rw(f.Value), Inline: f.Inline})
	}
	return out, changed
}

// rewrite replaces bucket links in s, returning the new text and how many
// file or profile image links it replaced. The old avatar is replaced too but
// not counted, so it never causes an edit on its own.
func (m *linkMigration) rewrite(s string) (string, int) {
	changed := 0
	out := linkPattern.ReplaceAllStringFunc(s, func(raw string) string {
		link := m.b.S3.ResolveLegacyLink(raw)
		// Only resolve this guild's files: another guild's profile would put
		// its current image in this guild's message.
		if (link.Kind == s3client.LegacyFile || link.Kind == s3client.LegacyProfileImage) && link.GuildID != m.guildID.String() {
			link.Kind = s3client.LegacyUnknown
		}
		switch link.Kind {
		case s3client.LegacyFile:
			m.files++
		case s3client.LegacyProfileImage:
			// A deleted profile has nothing to point at; leave the old link
			// so the report can say what was there.
			current := m.profileImageURL(link)
			if current == "" {
				m.missingProfiles++
				return raw
			}
			m.profileImages++
			changed++
			return current
		case s3client.LegacyAvatar:
			return link.NewURL
		case s3client.LegacyUnknown:
			m.unknownCount++
			if len(m.unknown) < migrateReportExamples {
				m.unknown = append(m.unknown, "<"+raw+">")
			}
			return raw
		default:
			return raw
		}
		changed++
		return link.NewURL
	})
	return out, changed
}

// profileImageURL returns the embed URL for the profile's current image, the
// same one /getprofile uses, or "" when the profile is gone or has no image.
func (m *linkMigration) profileImageURL(link s3client.LegacyLink) string {
	k := link.GuildID + "/" + link.TemplateName + "/" + link.UserID
	if u, ok := m.profiles[k]; ok {
		return u
	}
	u := ""
	if gid, err := snowflake.Parse(link.GuildID); err == nil {
		for _, tmpl := range m.b.templates[gid] {
			if tmpl.Name != link.TemplateName {
				continue
			}
			profile, err := m.b.S3.FetchProfileSimple(m.ctx, link.GuildID, link.TemplateName, link.UserID)
			if err != nil {
				if !errors.Is(err, s3client.ErrNotFound) {
					m.b.Log.Error("Link migration: fetching profile failed", "profile", k, "error", err)
				}
				break
			}
			u = m.b.S3.ProfileImageURL(tmpl, link.GuildID, link.UserID, profile)
		}
	}
	m.profiles[k] = u
	return u
}

// applyEdits makes the planned edits, unarchiving each archived thread once
// and archiving it again afterwards.
func (m *linkMigration) applyEdits() {
	byChannel := map[snowflake.ID][]pendingEdit{}
	var order []snowflake.ID
	for _, p := range m.pending {
		if _, ok := byChannel[p.channelID]; !ok {
			order = append(order, p.channelID)
		}
		byChannel[p.channelID] = append(byChannel[p.channelID], p)
	}

	for _, channelID := range order {
		unarchived := false
		for _, p := range byChannel[channelID] {
			if err := m.ctx.Err(); err != nil {
				m.stopped = err
				return
			}
			err := m.edit(p)
			if isArchivedThreadError(err) && !unarchived {
				if err = m.setArchived(channelID, false); err == nil {
					unarchived = true
					err = m.edit(p)
				}
			}
			if err != nil {
				m.b.Log.Error("Link migration edit failed", "channel_id", channelID, "message_id", p.messageID, "error", err)
				m.failed = append(m.failed, fmt.Sprintf("<%s> (%v)", messageLink(m.guildID, channelID, p.messageID), err))
				continue
			}
			m.edited++
		}
		if unarchived {
			if err := m.setArchived(channelID, true); err != nil {
				m.b.Log.Error("Link migration: re-archiving thread failed", "channel_id", channelID, "error", err)
				m.leftUnarchived = append(m.leftUnarchived, channelID)
			}
		}
	}
}

func (m *linkMigration) edit(p pendingEdit) error {
	_, err := m.b.Client.Rest.UpdateMessage(p.channelID, p.messageID, p.update, rest.WithCtx(m.ctx))
	return err
}

func (m *linkMigration) setArchived(channelID snowflake.ID, archived bool) error {
	_, err := m.b.Client.Rest.UpdateChannel(channelID, discord.GuildThreadUpdate{Archived: &archived}, rest.WithCtx(m.ctx))
	return err
}

func isArchivedThreadError(err error) bool {
	var restErr *rest.Error
	return errors.As(err, &restErr) && restErr.Code == rest.JSONErrorCodeOperationOnArchivedThread
}

func (m *linkMigration) report() string {
	var sb strings.Builder
	if m.apply {
		sb.WriteString("**Link migration finished**\n")
	} else {
		sb.WriteString("**Link migration dry run** (nothing was edited)\n")
	}
	fmt.Fprintf(&sb, "Scanned %d bot messages (%d in the mod log).\n", m.scanned, m.modLogScanned)
	fmt.Fprintf(&sb, "Messages to update: %d, with %d file links and %d profile image links.\n",
		len(m.pending), m.files, m.profileImages)
	if m.apply {
		fmt.Fprintf(&sb, "Edited %d, failed %d.\n", m.edited, len(m.failed))
	}
	if m.missingProfiles > 0 {
		fmt.Fprintf(&sb, "Left %d profile image links alone: the profile was deleted or has no image.\n", m.missingProfiles)
	}
	if m.stopped != nil {
		fmt.Fprintf(&sb, "Stopped before finishing (%v); run it again to continue.\n", m.stopped)
	}
	list := func(title string, items []string, total int) {
		if total == 0 {
			return
		}
		fmt.Fprintf(&sb, "\n%s (%d):\n", title, total)
		for i, s := range items {
			if i == migrateReportExamples {
				break
			}
			fmt.Fprintf(&sb, "- %s\n", s)
		}
	}
	list("Bucket links left alone (no stable route, or another server's file)", m.unknown, m.unknownCount)
	list("Failed edits", m.failed, len(m.failed))
	list("The scan was incomplete", m.scanErrors, len(m.scanErrors))
	if len(m.leftUnarchived) > 0 {
		sb.WriteString("\nCouldn't re-archive:")
		for _, id := range m.leftUnarchived {
			fmt.Fprintf(&sb, " <#%d>", id)
		}
		sb.WriteString("\n")
	}
	return truncateReport(sb.String(), 2000)
}

// truncateReport cuts s at a line break to fit in limit bytes.
func truncateReport(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	const more = "\n…"
	cut := strings.LastIndexByte(s[:limit-len(more)], '\n')
	if cut < 0 {
		cut = 0
	}
	return s[:cut] + more
}
