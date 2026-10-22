package bot

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
	"github.com/sadbox/sprobot/pkg/sprobot"
)

const shortcutResponseSlots = 5
const maxShortcutsPerGuild = 200

// modalTitleMax is Discord's limit on a modal title, in UTF-16 code units.
const modalTitleMax = 45

// autocompleteMax is the most choices Discord accepts in an autocomplete
// response.
const autocompleteMax = 25

type shortcutEntry struct {
	Responses []string `json:"responses"`
}

type shortcutState struct {
	mu        sync.Mutex
	Shortcuts map[string]shortcutEntry `json:"shortcuts"`
	indices   map[string]int
}

// next returns name's next response, rotating through them from a random
// start. Caller holds st.mu.
func (st *shortcutState) next(name string) (string, bool) {
	entry, ok := st.Shortcuts[name]
	if !ok || len(entry.Responses) == 0 {
		return "", false
	}
	n := len(entry.Responses)
	idx, started := st.indices[name]
	if !started {
		idx = rand.IntN(n)
	}
	st.indices[name] = (idx + 1) % n
	return entry.Responses[idx%n], true
}

// set stores name's responses and restarts its rotation, returning the
// responses it replaced. A new shortcut past the guild limit is refused.
// Caller holds st.mu.
func (st *shortcutState) set(name string, responses []string) (replaced []string, ok bool) {
	old, exists := st.Shortcuts[name]
	if !exists && len(st.Shortcuts) >= maxShortcutsPerGuild {
		return nil, false
	}
	st.Shortcuts[name] = shortcutEntry{Responses: responses}
	st.indices[name] = 0
	return old.Responses, true
}

// remove deletes name, returning its responses. Caller holds st.mu.
func (st *shortcutState) remove(name string) (removed []string, ok bool) {
	old, exists := st.Shortcuts[name]
	if !exists {
		return nil, false
	}
	delete(st.Shortcuts, name)
	delete(st.indices, name)
	return old.Responses, true
}

// allResponses returns every response of every shortcut. Caller holds st.mu.
func (st *shortcutState) allResponses() []string {
	var out []string
	for _, entry := range st.Shortcuts {
		out = append(out, entry.Responses...)
	}
	return out
}

func (b *Bot) handleShortcut(e *events.ApplicationCommandInteractionCreate) {
	data, ok := e.Data.(discord.SlashCommandInteractionData)
	if !ok {
		return
	}

	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	name := data.String("shortcut")
	if name == "" {
		botutil.RespondEphemeral(e, "Please provide a shortcut name.")
		return
	}

	st := b.shortcuts.get(*guildID)
	if st == nil {
		botutil.RespondEphemeral(e, "No shortcuts configured.")
		return
	}

	st.mu.Lock()
	response, ok := st.next(name)
	st.mu.Unlock()
	if !ok {
		botutil.RespondEphemeral(e, fmt.Sprintf("Shortcut %q not found.", name))
		return
	}

	b.Log.Info("Shortcut used", "user_id", e.User().ID, "guild_id", *guildID, "shortcut", name)

	response = expandShortcutVars(response, e.User().ID)

	username := getNickOrName(e.Member())
	embed := discord.Embed{
		Footer: &discord.EmbedFooter{
			Text:    fmt.Sprintf("@%s used /s %s", username, name),
			IconURL: e.User().EffectiveAvatarURL(),
		},
	}
	// Re-hosted images are stored as bare keys; map them to their permanent
	// link first so they embed rather than posting the key as text.
	if u := b.S3.FileURL(response); isImageURL(u) {
		embed.Image = &discord.EmbedResource{URL: u}
	} else {
		embed.Description = response
	}
	e.CreateMessage(discord.MessageCreate{
		Embeds: []discord.Embed{embed},
	})
}

// expandShortcutVars replaces template placeholders in a shortcut response.
// Currently supports [user] → <@userID>.
func expandShortcutVars(response string, userID snowflake.ID) string {
	return strings.ReplaceAll(response, "[user]", fmt.Sprintf("<@%d>", userID))
}

// isImageURL returns true if s is a single HTTP(S) URL ending in a common image extension.
func isImageURL(s string) bool {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
		return false
	}
	if strings.ContainsAny(s, " \n\r\t") {
		return false
	}
	// Strip query string / fragment before checking extension.
	path := s
	if i := strings.IndexAny(path, "?#"); i != -1 {
		path = path[:i]
	}
	path = strings.ToLower(path)
	for _, ext := range []string{".png", ".jpg", ".jpeg", ".gif", ".webp"} {
		if strings.HasSuffix(path, ext) {
			return true
		}
	}
	return false
}

// shortcutChoices returns the shortcut names containing current, ignoring
// case, sorted and capped at Discord's autocomplete limit.
func shortcutChoices(shortcuts map[string]shortcutEntry, current string) []string {
	current = strings.ToLower(current)
	var names []string
	for name := range shortcuts {
		if strings.Contains(strings.ToLower(name), current) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	if len(names) > autocompleteMax {
		names = names[:autocompleteMax]
	}
	return names
}

func (b *Bot) handleShortcutAutocomplete(e *events.AutocompleteInteractionCreate) {
	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	st := b.shortcuts.get(*guildID)
	if st == nil {
		e.AutocompleteResult(nil)
		return
	}

	st.mu.Lock()
	names := shortcutChoices(st.Shortcuts, e.Data.String("shortcut"))
	st.mu.Unlock()

	choices := make([]discord.AutocompleteChoice, 0, len(names))
	for _, name := range names {
		choices = append(choices, discord.AutocompleteChoiceString{Name: name, Value: name})
	}
	e.AutocompleteResult(choices)
}

func (b *Bot) handleShortcutConfig(e *events.ApplicationCommandInteractionCreate) {
	data, ok := e.Data.(discord.SlashCommandInteractionData)
	if !ok {
		return
	}

	subCmd := data.SubCommandName
	if subCmd == nil {
		return
	}

	switch *subCmd {
	case "set":
		b.handleShortcutConfigSet(e)
	case "remove":
		b.handleShortcutConfigRemove(e)
	case "list":
		b.handleShortcutConfigList(e)
	}
}

// shortcutModalTitle is the set modal's title, with the name cut short to
// fit Discord's limit (a longer title makes Discord reject the modal).
func shortcutModalTitle(name string) string {
	title := []rune("Set shortcut: " + name)
	if utf16Len(title) <= modalTitleMax {
		return string(title)
	}
	for utf16Len(title)+1 > modalTitleMax {
		title = title[:len(title)-1]
	}
	return string(title) + "…"
}

func utf16Len(r []rune) int {
	n := 0
	for _, c := range r {
		n += utf16.RuneLen(c)
	}
	return n
}

func (b *Bot) handleShortcutConfigSet(e *events.ApplicationCommandInteractionCreate) {
	data, ok := e.Data.(discord.SlashCommandInteractionData)
	if !ok {
		return
	}

	name := data.String("shortcut")
	if name == "" {
		botutil.RespondEphemeral(e, "Please provide a shortcut name.")
		return
	}

	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	b.Log.Info("Shortcut config set", "user_id", e.User().ID, "guild_id", *guildID, "shortcut", name)

	// Pre-fill with existing responses if the shortcut already exists
	var prefills [shortcutResponseSlots]string
	st := b.shortcuts.get(*guildID)
	if st != nil {
		st.mu.Lock()
		if entry, ok := st.Shortcuts[name]; ok {
			for i, r := range entry.Responses {
				if i >= shortcutResponseSlots {
					break
				}
				prefills[i] = r
			}
		}
		st.mu.Unlock()
	}

	var components []discord.LayoutComponent
	for i := range shortcutResponseSlots {
		required := i == 0
		components = append(components, discord.NewLabel(
			fmt.Sprintf("Response %d", i+1),
			discord.TextInputComponent{
				CustomID: fmt.Sprintf("response_%d", i),
				Style:    discord.TextInputStyleParagraph,
				Required: required,
				Value:    prefills[i],
			},
		))
	}

	err := e.Modal(discord.ModalCreate{
		CustomID:   fmt.Sprintf("sconfig_set_%s", name),
		Title:      shortcutModalTitle(name),
		Components: components,
	})
	if err != nil {
		b.Log.Error("Failed to respond with shortcut config modal", "error", err)
	}
}

// nonBlankResponses drops empty and whitespace-only responses.
func nonBlankResponses(texts []string) []string {
	var responses []string
	for _, text := range texts {
		if strings.TrimSpace(text) != "" {
			responses = append(responses, text)
		}
	}
	return responses
}

func (b *Bot) handleShortcutConfigSetModal(e *events.ModalSubmitInteractionCreate) {
	customID := e.Data.CustomID
	name := strings.TrimPrefix(customID, "sconfig_set_")

	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	// Defer: re-hosting images below can outlast the 3-second response window.
	if err := e.DeferCreateMessage(true); err != nil {
		b.Log.Error("Failed to defer shortcut config response", "error", err)
		return
	}
	respond := func(content string) { b.ephemeralFollowup(e.Token(), content) }

	var texts []string
	for i := range shortcutResponseSlots {
		texts = append(texts, e.Data.Text(fmt.Sprintf("response_%d", i)))
	}
	responses := nonBlankResponses(texts)

	if len(responses) == 0 {
		respond("No responses provided. Shortcut not saved.")
		return
	}

	// Re-host image URLs to S3 so they don't expire.
	ctx, cancel := context.WithTimeout(context.Background(), botutil.DeferredWorkBudget)
	defer cancel()
	guildStr := fmt.Sprintf("%d", *guildID)
	for i, resp := range responses {
		if !isImageURL(resp) {
			continue
		}
		if key, ok := b.storedFileKey(resp); ok {
			// Reuse one of this guild's own shortcut files; copy any other
			// saved file. Shortcut files are deleted once their own guild's
			// shortcuts stop using them, a check that never looks at other
			// guilds or features, so a shared key could vanish from under us.
			if kind, gid, _, _ := sprobot.ParseFileKey(key); kind == "shortcut" && gid == guildStr {
				responses[i] = key
				continue
			}
			copied, err := b.S3.CopyShortcutFile(ctx, guildStr, key)
			if err != nil {
				b.Log.Error("Failed to copy saved file for shortcut", "error", err)
				continue // keep original URL
			}
			responses[i] = copied
			continue
		}
		s3URL, err := b.S3.SaveShortcutImage(ctx, guildStr, strings.TrimSpace(resp))
		if err != nil {
			b.Log.Error("Failed to re-host shortcut image", "error", err)
			continue // keep original URL
		}
		responses[i] = s3URL
	}

	st := b.shortcuts.get(*guildID)
	if st == nil {
		respond("Something went wrong.")
		return
	}

	st.mu.Lock()
	replaced, ok := st.set(name, responses)
	remaining := st.allResponses()
	st.mu.Unlock()
	if !ok {
		respond(fmt.Sprintf("Maximum of %d shortcuts reached.", maxShortcutsPerGuild))
		return
	}

	if err := b.shortcuts.persist(*guildID); err != nil {
		b.Log.Error("Failed to save shortcut data", "guild_id", *guildID, "error", err)
		respond("Failed to save shortcut.")
		return
	}
	b.deleteUnusedShortcutFiles(ctx, guildStr, replaced, remaining)

	b.Log.Info("Shortcut saved", "user_id", e.User().ID, "guild_id", *guildID, "shortcut", name, "responses", len(responses))

	respond(fmt.Sprintf("Shortcut %q saved with %d response(s).", name, len(responses)))
}

// storedFileKey returns the bucket key of a file we store, given a bare key,
// an old bucket URL, or the file's web link (its /view page included).
func (b *Bot) storedFileKey(ref string) (string, bool) {
	u, err := url.Parse(b.S3.FileURL(strings.TrimSpace(ref)))
	if err != nil || u.Host == "" {
		return "", false
	}
	segs := strings.Split(u.Path, "/")
	if segs[len(segs)-1] == "view" {
		segs = segs[:len(segs)-1]
	}
	if len(segs) < 4 || segs[len(segs)-4] != "files" {
		return "", false
	}
	n := len(segs)
	key, ok := sprobot.FileKey(segs[n-3], segs[n-2], segs[n-1])
	if !ok {
		return "", false
	}
	// Only our own web endpoint serves this key at this address.
	if b.S3.FileURL(key) != u.Scheme+"://"+u.Host+strings.Join(segs, "/") {
		return "", false
	}
	return key, true
}

// unusedShortcutFiles returns the keys of this guild's saved shortcut files
// that old refers to and no remaining response does.
func unusedShortcutFiles(old, remaining []string, guildID string, fileKey func(string) (string, bool)) []string {
	inUse := make(map[string]bool)
	for _, r := range remaining {
		if key, ok := fileKey(r); ok {
			inUse[key] = true
		}
	}
	var unused []string
	for _, r := range old {
		key, ok := fileKey(r)
		if !ok || inUse[key] || slices.Contains(unused, key) {
			continue
		}
		if kind, gid, _, ok := sprobot.ParseFileKey(key); ok && kind == "shortcut" && gid == guildID {
			unused = append(unused, key)
		}
	}
	return unused
}

// deleteUnusedShortcutFiles removes, best effort, the saved files that old
// responses used and remaining ones don't.
func (b *Bot) deleteUnusedShortcutFiles(ctx context.Context, guildID string, old, remaining []string) {
	keys := unusedShortcutFiles(old, remaining, guildID, b.storedFileKey)
	if len(keys) == 0 {
		return
	}
	if err := b.S3.DeleteKeys(ctx, keys...); err != nil {
		b.Log.Error("Failed to delete unused shortcut files", "guild_id", guildID, "error", err)
		return
	}
	b.Log.Info("Deleted unused shortcut files", "guild_id", guildID, "count", len(keys))
}

// ephemeralFollowup answers a deferred interaction.
func (b *Bot) ephemeralFollowup(token, content string) {
	botutil.FollowupEphemeral(b.Client, token, content)
}

func (b *Bot) handleShortcutConfigRemove(e *events.ApplicationCommandInteractionCreate) {
	data, ok := e.Data.(discord.SlashCommandInteractionData)
	if !ok {
		return
	}

	name := data.String("shortcut")
	if name == "" {
		botutil.RespondEphemeral(e, "Please provide a shortcut name.")
		return
	}

	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	b.Log.Info("Shortcut config remove", "user_id", e.User().ID, "guild_id", *guildID, "shortcut", name)

	st := b.shortcuts.get(*guildID)
	if st == nil {
		botutil.RespondEphemeral(e, "No shortcuts configured.")
		return
	}

	st.mu.Lock()
	removed, ok := st.remove(name)
	remaining := st.allResponses()
	st.mu.Unlock()
	if !ok {
		botutil.RespondEphemeral(e, fmt.Sprintf("Shortcut %q not found.", name))
		return
	}

	if err := b.shortcuts.persist(*guildID); err != nil {
		b.Log.Error("Failed to save shortcut data", "guild_id", *guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to save shortcut.")
		return
	}

	botutil.RespondEphemeral(e, fmt.Sprintf("Shortcut %q removed.", name))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	b.deleteUnusedShortcutFiles(ctx, guildIDStr(e), removed, remaining)
}

func (b *Bot) handleShortcutConfigList(e *events.ApplicationCommandInteractionCreate) {
	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	b.Log.Info("Shortcut config list", "user_id", e.User().ID, "guild_id", *guildID)

	st := b.shortcuts.get(*guildID)
	if st == nil {
		botutil.RespondEphemeral(e, "No shortcuts configured.")
		return
	}

	st.mu.Lock()
	if len(st.Shortcuts) == 0 {
		st.mu.Unlock()
		botutil.RespondEphemeral(e, "No shortcuts configured.")
		return
	}
	var lines []string
	for name, entry := range st.Shortcuts {
		lines = append(lines, fmt.Sprintf("**%s** — %d response(s)", name, len(entry.Responses)))
	}
	st.mu.Unlock()

	botutil.RespondEphemeral(e, strings.Join(lines, "\n"))
}
