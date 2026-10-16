package bot

// /config migrate-links rewrites links in messages the bot posted while files
// were linked straight from the (now private) bucket, pointing them at the web
// server's stable routes. It is a one-off: remove it, and the
// linkMigrations field, once every guild has run it.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
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

// searchPageInterval paces the search scan. Back-to-back searches are
// answered 429; disgo waits those out, but pacing keeps them from piling up.
var searchPageInterval = 3 * time.Second

// searchProgressPages is how often the search scan logs its progress.
const searchProgressPages = 20

// rateLimitBackoff is how long to wait before each retry once disgo has given
// up on a 429.
var rateLimitBackoff = []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute}

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

	// Only mod log threads are unarchived for edits; messages in other
	// archived threads are left alone.
	modLogThreads   map[snowflake.ID]bool
	archivedThreads map[snowflake.ID]bool // archived threads outside the mod log, as of the scan
	inArchived      []string

	noAccess         []snowflake.ID // channels Discord wouldn't let the bot read
	noPrivateThreads int            // channels whose private threads couldn't be listed

	// search runs one search; nil means Discord's. Tests replace it.
	search func(discord.GuildMessagesSearch) (*discord.GuildMessagesSearchResult, error)
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
		m.queue(msg)
	}

	m.modLogThreads = map[snowflake.ID]bool{}
	m.archivedThreads = map[snowflake.ID]bool{}
	active := m.activeThreads()
	before := m.scanned
	m.scanModLog(consider, active)
	m.modLogScanned = m.scanned - before
	m.scanSearch(consider, active)
}

// queue plans msg's edit, setting it aside if it's in an archived thread
// outside the mod log.
func (m *linkMigration) queue(msg discord.Message) {
	update, ok := m.plan(msg)
	switch {
	case !ok:
	case m.archivedThreads[msg.ChannelID]:
		m.inArchived = append(m.inArchived, "<"+messageLink(m.guildID, msg.ChannelID, msg.ID)+">")
	default:
		m.pending = append(m.pending, pendingEdit{channelID: msg.ChannelID, messageID: msg.ID, update: update})
	}
}

func (m *linkMigration) modLogForum() snowflake.ID {
	st := m.b.modLog.get(m.guildID)
	if st == nil {
		return 0
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.ChannelID
}

func (m *linkMigration) eventLogChannel() snowflake.ID {
	st := m.b.eventLog.get(m.guildID)
	if st == nil {
		return 0
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.ChannelID
}

// activeThreads lists the guild's active threads by parent channel.
func (m *linkMigration) activeThreads() map[snowflake.ID][]snowflake.ID {
	var active *discord.GuildActiveThreads
	err := m.call("listing active threads", func() (err error) {
		active, err = m.b.Client.Rest.GetActiveGuildThreads(m.guildID, rest.WithCtx(m.ctx))
		return err
	})
	if err != nil {
		m.scanError("listing active threads", err)
		return nil
	}
	byParent := map[snowflake.ID][]snowflake.ID{}
	for _, t := range active.Threads {
		if p := t.ParentID(); p != nil {
			byParent[*p] = append(byParent[*p], t.ID())
		}
	}
	return byParent
}

// listArchivedThreads lists channelID's archived threads, returning those
// listed before any error.
func (m *linkMigration) listArchivedThreads(channelID snowflake.ID, private bool) ([]snowflake.ID, error) {
	var threads []snowflake.ID
	before := time.Time{}
	for {
		var page *discord.GetThreads
		err := m.call("listing archived threads", func() (err error) {
			if private {
				page, err = m.b.Client.Rest.GetPrivateArchivedThreads(channelID, before, 100, rest.WithCtx(m.ctx))
			} else {
				page, err = m.b.Client.Rest.GetPublicArchivedThreads(channelID, before, 100, rest.WithCtx(m.ctx))
			}
			return err
		})
		if err != nil {
			return threads, err
		}
		for _, t := range page.Threads {
			threads = append(threads, t.ID())
		}
		if !page.HasMore || len(page.Threads) == 0 {
			return threads, nil
		}
		before = page.Threads[len(page.Threads)-1].ThreadMetadata.ArchiveTimestamp
	}
}

// isAccessError reports whether Discord refused a request because the bot
// can't see the channel or lacks a permission there.
func isAccessError(err error) bool {
	var restErr *rest.Error
	return errors.As(err, &restErr) &&
		(restErr.Code == rest.JSONErrorCodeMissingAccess || restErr.Code == rest.JSONErrorCodeLackPermissionsToPerformAction)
}

func (m *linkMigration) scanModLog(consider func(discord.Message), active map[snowflake.ID][]snowflake.ID) {
	forumID := m.modLogForum()
	if forumID == 0 {
		return
	}
	archived, err := m.listArchivedThreads(forumID, false)
	if err != nil {
		m.scanError("listing archived mod log threads", err)
	}
	threads := append(active[forumID], archived...)

	for _, threadID := range threads {
		m.modLogThreads[threadID] = true
		var oldest snowflake.ID
		for {
			var msgs []discord.Message
			err := m.call("reading mod log threads", func() (err error) {
				msgs, err = m.b.Client.Rest.GetMessages(threadID, 0, oldest, 0, 100, rest.WithCtx(m.ctx))
				return err
			})
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

// searchChannels lists what the search covers: every channel the bot can read
// except the event log, whose embeds are most of the bot's messages and never
// carry bucket links, and the mod log forum, which scanModLog reads in full.
// Threads are listed explicitly, since Discord doesn't document whether a
// parent channel filter includes them.
func (m *linkMigration) searchChannels(active map[snowflake.ID][]snowflake.ID) []snowflake.ID {
	skip := map[snowflake.ID]bool{m.eventLogChannel(): true, m.modLogForum(): true}
	self, ok := m.b.Client.Caches.SelfMember(m.guildID)
	if !ok {
		m.scanError("listing channels", errors.New("the bot's member isn't cached"))
		return nil
	}

	var ids []snowflake.ID
	for ch := range m.b.Client.Caches.ChannelsForGuild(m.guildID) {
		if skip[ch.ID()] {
			continue
		}
		switch ch.Type() {
		case discord.ChannelTypeGuildCategory, discord.ChannelTypeGuildDirectory,
			discord.ChannelTypeGuildNewsThread, discord.ChannelTypeGuildPublicThread, discord.ChannelTypeGuildPrivateThread:
			continue
		}
		if !m.b.Client.Caches.MemberPermissionsInChannel(ch, self).Has(discord.PermissionViewChannel, discord.PermissionReadMessageHistory) {
			continue
		}
		var archived []snowflake.ID
		switch ch.Type() {
		case discord.ChannelTypeGuildText, discord.ChannelTypeGuildNews, discord.ChannelTypeGuildForum, discord.ChannelTypeGuildMedia:
			public, err := m.listArchivedThreads(ch.ID(), false)
			if isAccessError(err) {
				// The cached permissions said otherwise, but Discord won't
				// let the bot read here; leave it out so it can't fail the
				// rest of its search batch.
				m.noAccess = append(m.noAccess, ch.ID())
				continue
			}
			if err != nil {
				m.scanError(fmt.Sprintf("listing archived threads in <#%d>", ch.ID()), err)
			}
			archived = public
		}
		if ch.Type() == discord.ChannelTypeGuildText {
			// Listing private threads needs Manage Threads; without it the
			// public ones are still searched.
			private, err := m.listArchivedThreads(ch.ID(), true)
			switch {
			case isAccessError(err):
				m.noPrivateThreads++
			case err != nil:
				m.scanError(fmt.Sprintf("listing private archived threads in <#%d>", ch.ID()), err)
			}
			archived = append(archived, private...)
		}
		ids = append(ids, ch.ID())
		ids = append(ids, active[ch.ID()]...)
		for _, id := range archived {
			m.archivedThreads[id] = true
		}
		ids = append(ids, archived...)
	}
	return ids
}

// searchBatch is how many channels one search filters on. The API allows 500,
// but each ID adds up to 31 bytes to the query string ("channel_id=", 19
// digits and "&") and Discord rejects request lines over 4094 bytes. 100
// comes to about 3.3KB; TestSearchBatchFitsRequestLine checks it.
const searchBatch = 100

func (m *linkMigration) scanSearch(consider func(discord.Message), active map[snowflake.ID][]snowflake.ID) {
	channels := m.searchChannels(active)
	// An empty channel filter would search everything, event log included.
	if len(channels) == 0 {
		m.scanError("searching the server", errors.New("no channels to search"))
		return
	}
	m.b.Log.Info("Link migration searching", "guild_id", m.guildID, "channels", len(channels))
	for start := 0; start < len(channels); start += searchBatch {
		batch := channels[start:min(start+searchBatch, len(channels))]
		m.b.Log.Info("Link migration search batch", "guild_id", m.guildID, "from", start, "count", len(batch), "of", len(channels))
		if !m.searchIn(consider, batch) {
			return
		}
	}
}

// searchIn pages through the bot's messages in channels. A failed search is
// recorded and the next batch still runs; it reports false only once the run
// is cancelled.
func (m *linkMigration) searchIn(consider func(discord.Message), channels []snowflake.ID) bool {
	includeNSFW := true
	query := discord.GuildMessagesSearch{
		AuthorIDs:   []snowflake.ID{m.b.Client.ID()},
		ChannelIDs:  channels,
		IncludeNSFW: &includeNSFW,
		Limit:       25,
	}
	for page := 1; ; page++ {
		if !m.sleep(searchPageInterval) {
			m.scanError("searching the server", m.ctx.Err())
			return false
		}
		search := m.search
		if search == nil {
			search = m.discordSearch
		}
		var res *discord.GuildMessagesSearchResult
		err := m.call("searching the server", func() (err error) {
			res, err = search(query)
			return err
		})
		if isAccessError(err) {
			// One unreadable channel fails the whole batch; split it to
			// find which, and search the rest.
			if len(channels) == 1 {
				m.noAccess = append(m.noAccess, channels[0])
				return true
			}
			half := len(channels) / 2
			return m.searchIn(consider, channels[:half]) && m.searchIn(consider, channels[half:])
		}
		if err != nil {
			m.scanError(fmt.Sprintf("searching a batch of %d channels", len(channels)), err)
			return m.ctx.Err() == nil
		}
		if page%searchProgressPages == 0 {
			m.b.Log.Info("Link migration search progress", "guild_id", m.guildID, "pages", page, "scanned", m.scanned, "before", query.MaxID.Time())
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
			return true
		}
	}
}

// searchEndpoint builds the search request for q. disgo's own encoding joins
// list filters with commas, which Discord rejects for more than one channel
// ("is not snowflake"); it wants the parameter repeated, once per value. Only
// the fields the migration uses are encoded.
func searchEndpoint(guildID snowflake.ID, q discord.GuildMessagesSearch) *rest.CompiledEndpoint {
	values := url.Values{}
	for _, id := range q.AuthorIDs {
		values.Add("author_id", id.String())
	}
	for _, id := range q.ChannelIDs {
		values.Add("channel_id", id.String())
	}
	if q.IncludeNSFW != nil {
		values.Set("include_nsfw", strconv.FormatBool(*q.IncludeNSFW))
	}
	if q.Limit != 0 {
		values.Set("limit", strconv.Itoa(q.Limit))
	}
	if q.MaxID != 0 {
		values.Set("max_id", q.MaxID.String())
	}
	endpoint := rest.SearchGuildMessages.Compile(nil, guildID)
	endpoint.URL += "?" + values.Encode()
	return endpoint
}

// discordSearch runs one search through disgo's rate-limited client, waiting
// out a 202 while the guild is still being indexed, as disgo's
// SearchGuildMessages does.
func (m *linkMigration) discordSearch(q discord.GuildMessagesSearch) (*discord.GuildMessagesSearchResult, error) {
	for {
		var res discord.GuildMessagesSearchResult
		if err := m.b.Client.Rest.Do(searchEndpoint(m.guildID, q), nil, &res, rest.WithCtx(m.ctx)); err != nil {
			return nil, err
		}
		if res.Code != int(rest.JSONErrorCodeIndexNotYetAvailable) {
			return &res, nil
		}
		wait := time.Duration(res.RetryAfter) * time.Second
		if wait <= 0 {
			wait = time.Second
		}
		if !m.sleep(wait) {
			return nil, m.ctx.Err()
		}
	}
}

// call runs fn, and if Discord is still answering 429 once disgo has spent
// its own retries, backs off and tries again rather than failing the step.
func (m *linkMigration) call(what string, fn func() error) error {
	err := fn()
	for _, wait := range rateLimitBackoff {
		if !isRateLimited(err) {
			return err
		}
		m.b.Log.Warn("Link migration rate limited; backing off", "step", what, "wait", wait)
		if !m.sleep(wait) {
			return m.ctx.Err()
		}
		err = fn()
	}
	return err
}

// sleep waits d, reporting false if the run is cancelled first.
func (m *linkMigration) sleep(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-m.ctx.Done():
		return false
	}
}

func isRateLimited(err error) bool {
	var restErr *rest.Error
	return errors.As(err, &restErr) && restErr.Response != nil && restErr.Response.StatusCode == http.StatusTooManyRequests
}

func (m *linkMigration) scanError(what string, err error) {
	m.b.Log.Error("Link migration scan failed", "step", what, "error", err)
	m.scanErrors = append(m.scanErrors, fmt.Sprintf("%s: %s", what, reportError(err)))
}

// reportErrorLen caps an error in the report; REST errors can carry a whole
// HTML page.
const reportErrorLen = 200

func reportError(err error) string {
	s := err.Error()
	if len(s) <= reportErrorLen {
		return s
	}
	return strings.ToValidUTF8(s[:reportErrorLen], "") + "…"
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
			if isArchivedThreadError(err) {
				// Only mod log threads are reopened; anywhere else, a thread
				// archived since the scan is left as it is.
				if !m.modLogThreads[channelID] {
					m.inArchived = append(m.inArchived, "<"+messageLink(m.guildID, channelID, p.messageID)+">")
					continue
				}
				if !unarchived {
					if err = m.setArchived(channelID, false); err == nil {
						unarchived = true
						err = m.edit(p)
					}
				}
			}
			if err != nil {
				m.b.Log.Error("Link migration edit failed", "channel_id", channelID, "message_id", p.messageID, "error", err)
				m.failed = append(m.failed, fmt.Sprintf("<%s> (%s)", messageLink(m.guildID, channelID, p.messageID), reportError(err)))
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
	return m.call("editing messages", func() error {
		_, err := m.b.Client.Rest.UpdateMessage(p.channelID, p.messageID, p.update, rest.WithCtx(m.ctx))
		return err
	})
}

func (m *linkMigration) setArchived(channelID snowflake.ID, archived bool) error {
	return m.call("archiving threads", func() error {
		_, err := m.b.Client.Rest.UpdateChannel(channelID, discord.GuildThreadUpdate{Archived: &archived}, rest.WithCtx(m.ctx))
		return err
	})
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
	list("Left alone in archived threads outside the mod log", m.inArchived, len(m.inArchived))
	list("Failed edits", m.failed, len(m.failed))
	list("The scan was incomplete", m.scanErrors, len(m.scanErrors))
	if len(m.noAccess) > 0 {
		fmt.Fprintf(&sb, "\nSkipped %d channels the bot can't read:", len(m.noAccess))
		for i, id := range m.noAccess {
			if i == migrateReportExamples {
				sb.WriteString(" …")
				break
			}
			fmt.Fprintf(&sb, " <#%d>", id)
		}
		sb.WriteString("\n")
	}
	if m.noPrivateThreads > 0 {
		fmt.Fprintf(&sb, "Couldn't list private archived threads in %d channels (needs Manage Threads).\n", m.noPrivateThreads)
	}
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
