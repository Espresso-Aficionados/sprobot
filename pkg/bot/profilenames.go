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

	"github.com/sadbox/sprobot/pkg/s3client"
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
}

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
func (b *Bot) loadProfileOwners(guildIDs []snowflake.ID) {
	for _, guildID := range guildIDs {
		var templates []string
		for _, tmpl := range b.templates[guildID] {
			templates = append(templates, tmpl.Name)
		}
		if len(templates) == 0 {
			continue
		}
		guildStr := strconv.FormatInt(int64(guildID), 10)

		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		objects, err := b.S3.ListObjects(ctx, "profiles/"+guildStr+"/")
		cancel()
		if err != nil {
			b.Log.Error("Failed to list profiles; checking authors one by one", "guild_id", guildStr, "error", err)
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
		for userStr, tmpls := range owners {
			// Anything recorded since the listing was taken is newer.
			key := profileKey(guildStr, userStr)
			if p.owners[key] == nil {
				p.owners[key] = &profileOwner{templates: tmpls}
			}
		}
		p.listed[guildID] = true
		p.mu.Unlock()
		b.Log.Info("Listed profile owners", "guild_id", guildStr, "owners", len(owners))
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
func (b *Bot) syncProfileName(guildID snowflake.ID, msg discord.Message) {
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

	go b.writeProfileName(guildID, guildStr, userStr, name, o, templates)
}

// writeProfileName stores name in the user's profiles of the given templates,
// or of every template when templates is nil.
func (b *Bot) writeProfileName(guildID snowflake.ID, guildStr, userStr, name string, o *profileOwner, templates []string) {
	lock := b.profileLock(guildStr, userStr)
	lock.Lock()
	defer lock.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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
			b.Log.Error("Failed to update profile name", "error", err, "guild_id", guildStr, "user_id", userStr, "template", tmpl.Name)
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
