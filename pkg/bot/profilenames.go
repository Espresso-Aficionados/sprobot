package bot

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/Espresso-Aficionados/sprobot/pkg/s3client"
	"github.com/Espresso-Aficionados/sprobot/pkg/sprobot"
)

// profileNameRetry is how long to wait before retrying a failed name update,
// so a persistent S3 error doesn't cost a write on every message.
const profileNameRetry = 10 * time.Minute

// profileNames keeps the display name stored with each profile current, from
// its owner's messages, so link previews of profile pages name the right
// person. It also backfills profiles saved before names were stored.
type profileNames struct {
	mu     sync.Mutex
	owners map[string]*profileOwner // profileKey -> what we know of this user's profiles
	listed map[snowflake.ID]bool    // guilds whose stored profiles have been listed
	locks  sync.Map                 // profileKey -> *sync.Mutex
	// stored is whether each profile is stored, from the listing and the
	// saves and deletes since (false: deleted); sprobot_profiles counts it.
	stored map[storedProfileKey]bool
}

// storedProfileKey is one user's profile in a guild; template is its short name.
type storedProfileKey struct{ guild, template, user string }

type profileOwner struct {
	none      bool     // the user has no profiles in this guild
	templates []string // templates the user has a profile in; nil means check every one
	name      string   // the name stored with their profiles
	busy      bool     // an update is running
	retryAt   time.Time
}

func profileKey(guildID, userID string) string {
	return guildID + "/" + userID
}

// profileLock serializes writes to one user's profiles in a guild, so a name
// update can't overwrite a concurrent edit or recreate a deleted profile.
func (b *Bot) profileLock(guildID, userID string) *sync.Mutex {
	mu, _ := b.profileNames.locks.LoadOrStore(profileKey(guildID, userID), &sync.Mutex{})
	return mu.(*sync.Mutex)
}

// profileSaved records that the user has a profile stored with name. Call it
// with the profile lock held.
func (b *Bot) profileSaved(guildID, userID, name string) {
	p := &b.profileNames
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.owners == nil {
		p.owners = make(map[string]*profileOwner)
	}
	p.owners[profileKey(guildID, userID)] = &profileOwner{name: name}
}

// profileDeleted forgets what we know of the user's profiles; their next
// message checks every template again. Call it with the profile lock held.
func (b *Bot) profileDeleted(guildID, userID string) {
	p := &b.profileNames
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.owners == nil {
		p.owners = make(map[string]*profileOwner)
	}
	// Kept rather than removed: in a listed guild, a missing entry means "no
	// profiles", and the user may still have one in another template.
	p.owners[profileKey(guildID, userID)] = &profileOwner{}
}

// profileStored records whether the user's profile of the template (its
// short name) is stored, after a save or delete. Call it with the profile
// lock held.
func (b *Bot) profileStored(guildID, template, userID string, stored bool) {
	p := &b.profileNames
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stored == nil {
		p.stored = make(map[storedProfileKey]bool)
	}
	p.stored[storedProfileKey{guildID, template, userID}] = stored
}

// counts is the number of profiles stored in the guilds of guildIDs, by
// template short name, every template of theirs present. ok is false unless
// each of them with templates has been listed and one has.
func (p *profileNames) counts(guildIDs []snowflake.ID, templates map[snowflake.ID][]sprobot.Template) (counts map[string]int, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	counts = make(map[string]int)
	guilds := make(map[string]bool)
	for _, id := range guildIDs {
		if len(templates[id]) == 0 {
			continue
		}
		if !p.listed[id] {
			return nil, false
		}
		guilds[id.String()] = true
		for _, tmpl := range templates[id] {
			counts[tmpl.ShortName] = 0
		}
	}
	for k, stored := range p.stored {
		if stored && guilds[k.guild] {
			counts[k.template]++
		}
	}
	return counts, len(guilds) > 0
}

// profileOwnersFromKeys maps the stored profile keys of a guild,
// profiles/{guild}/{template}/{user}.json, to each user's templates. Keys of
// templates the guild doesn't use are ignored.
func profileOwnersFromKeys(guildID string, keys []string, templates []string) map[string][]string {
	owners := make(map[string][]string)
	for _, key := range keys {
		rest, ok := strings.CutPrefix(key, "profiles/"+guildID+"/")
		if !ok {
			continue
		}
		tmpl, file, ok := strings.Cut(rest, "/")
		userID, isJSON := strings.CutSuffix(file, ".json")
		if !ok || !isJSON || !slices.Contains(templates, tmpl) {
			continue
		}
		if _, err := snowflake.Parse(userID); err != nil {
			continue
		}
		owners[userID] = append(owners[userID], tmpl)
	}
	return owners
}

// loadProfileOwners lists each guild's stored profiles, so that name updates
// read only profiles that exist instead of probing every message author. A
// guild whose listing fails keeps probing.
func (b *Bot) loadProfileOwners(ctx context.Context, guildIDs []snowflake.ID) {
	for _, guildID := range guildIDs {
		var templates []string
		shortNames := make(map[string]string)
		for _, tmpl := range b.templates[guildID] {
			templates = append(templates, tmpl.Name)
			shortNames[tmpl.Name] = tmpl.ShortName
		}
		if len(templates) == 0 {
			continue
		}
		guildStr := strconv.FormatInt(int64(guildID), 10)

		ctx, cancel := context.WithTimeout(ctx, time.Minute)
		objects, err := b.S3.ListObjects(ctx, "profiles/"+guildStr+"/")
		cancel()
		if err != nil {
			b.Log.ErrorContext(ctx, "Failed to list profiles; checking authors one by one", "guild_id", guildStr, "error", err)
			continue
		}
		keys := make([]string, len(objects))
		for i, o := range objects {
			keys[i] = o.Key
		}
		owners := profileOwnersFromKeys(guildStr, keys, templates)

		p := &b.profileNames
		p.mu.Lock()
		if p.owners == nil {
			p.owners = make(map[string]*profileOwner)
		}
		if p.listed == nil {
			p.listed = make(map[snowflake.ID]bool)
		}
		if p.stored == nil {
			p.stored = make(map[storedProfileKey]bool)
		}
		for userStr, tmpls := range owners {
			// Anything recorded since the listing was taken is newer.
			key := profileKey(guildStr, userStr)
			if p.owners[key] == nil {
				p.owners[key] = &profileOwner{templates: tmpls}
			}
			for _, tmpl := range tmpls {
				k := storedProfileKey{guildStr, shortNames[tmpl], userStr}
				if _, recorded := p.stored[k]; !recorded {
					p.stored[k] = true
				}
			}
		}
		p.listed[guildID] = true
		p.mu.Unlock()
		b.Log.InfoContext(ctx, "Listed profile owners", "guild_id", guildStr, "owners", len(owners))
	}
}

// messageDisplayName is the author's name in the guild, resolved the same way
// as the member name /edit stores.
func messageDisplayName(msg discord.Message) string {
	if msg.Member != nil {
		return discord.Member{Nick: msg.Member.Nick, User: msg.Author}.EffectiveName()
	}
	return msg.Author.EffectiveName()
}

// syncProfileName updates the author's stored profile name if it has changed.
// The S3 work runs in the background; the common case is a map lookup.
func (b *Bot) syncProfileName(ctx context.Context, guildID snowflake.ID, msg discord.Message) {
	if msg.WebhookID != nil || len(b.templates[guildID]) == 0 {
		return
	}
	guildStr := strconv.FormatInt(int64(guildID), 10)
	userStr := strconv.FormatInt(int64(msg.Author.ID), 10)
	name := messageDisplayName(msg)

	p := &b.profileNames
	p.mu.Lock()
	if p.owners == nil {
		p.owners = make(map[string]*profileOwner)
	}
	key := profileKey(guildStr, userStr)
	o := p.owners[key]
	switch {
	case o == nil && p.listed[guildID]:
		// Not in the listing, and no profile saved since.
		p.owners[key] = &profileOwner{none: true}
		p.mu.Unlock()
		return
	case o == nil:
		o = &profileOwner{}
		p.owners[key] = o
	case o.none || o.busy || o.name == name || time.Now().Before(o.retryAt):
		p.mu.Unlock()
		return
	}
	o.busy = true
	templates := o.templates
	p.mu.Unlock()

	go b.writeProfileName(context.WithoutCancel(ctx), guildID, guildStr, userStr, name, o, templates)
}

// writeProfileName stores name in the user's profiles of the given templates,
// or of every template when templates is nil.
func (b *Bot) writeProfileName(ctx context.Context, guildID snowflake.ID, guildStr, userStr, name string, o *profileOwner, templates []string) {
	lock := b.profileLock(guildStr, userStr)
	lock.Lock()
	defer lock.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var found []string
	failed := false
	for _, tmpl := range b.templates[guildID] {
		if templates != nil && !slices.Contains(templates, tmpl.Name) {
			continue
		}
		err := b.S3.SetProfileDisplayName(ctx, tmpl, guildStr, userStr, name)
		switch {
		case err == nil:
			found = append(found, tmpl.Name)
		case errors.Is(err, s3client.ErrNotFound):
		default:
			failed = true
			b.Log.ErrorContext(ctx, "Failed to update profile name", "error", err, "guild_id", guildStr, "user_id", userStr, "template", tmpl.Name)
		}
	}

	p := &b.profileNames
	p.mu.Lock()
	defer p.mu.Unlock()
	o.busy = false
	switch {
	case failed:
		o.retryAt = time.Now().Add(profileNameRetry)
	case len(found) > 0:
		o.name = name
		o.templates = found
	default:
		o.none = true
	}
}
