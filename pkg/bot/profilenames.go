package bot

import (
	"context"
	"errors"
	"strconv"
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
	locks  sync.Map                 // profileKey -> *sync.Mutex
}

type profileOwner struct {
	none    bool   // the user has no profiles in this guild
	name    string // the name stored with their profiles
	busy    bool   // an update is running
	retryAt time.Time
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
// message checks again. Call it with the profile lock held.
func (b *Bot) profileDeleted(guildID, userID string) {
	p := &b.profileNames
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.owners, profileKey(guildID, userID))
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
	if o == nil {
		o = &profileOwner{}
		p.owners[key] = o
	} else if o.none || o.busy || o.name == name || time.Now().Before(o.retryAt) {
		p.mu.Unlock()
		return
	}
	o.busy = true
	p.mu.Unlock()

	go b.writeProfileName(guildID, guildStr, userStr, name, o)
}

func (b *Bot) writeProfileName(guildID snowflake.ID, guildStr, userStr, name string, o *profileOwner) {
	lock := b.profileLock(guildStr, userStr)
	lock.Lock()
	defer lock.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	found, failed := false, false
	for _, tmpl := range b.templates[guildID] {
		err := b.S3.SetProfileDisplayName(ctx, tmpl, guildStr, userStr, name)
		switch {
		case err == nil:
			found = true
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
	case found:
		o.name = name
	default:
		o.none = true
	}
}
