package profiles

import (
	"fmt"
	"log"
	"sync"

	"github.com/bwmarrin/discordgo"
)

func Setup(ds *discordgo.Session) error {
	p := &Profiles{
		userMu:     &sync.RWMutex{},
		userToChan: make(map[string]string),
	}
	ds.AddHandler(p.onMessageCreate)
	return nil
}

type UserProfile struct {
	Machine       string
	Grinder       string
	FavoriteBeans string
	Pronouns      string
	Picture       string // special case? // MessageAttachment might be what I need for this
}

type Profiles struct {
	userMu     *sync.RWMutex // protects
	userToChan map[string]string

	// Database
	// Pic Source?
}

// GetProfile(userID) -> UserProfile
// handle .setprofile
// 	1. Get Current Profile
// 	2. Iterate through UserProfile, updating each field
// 	3. Handle timeouts for profile updates

// Consider a cancel context to propogate errors through to people

// Return the private channel ID for a user
func (p *Profiles) getChannelForUser(ds *discordgo.Session, userID string) (string, error) {
	p.userMu.RLock()
	chanID, ok := p.userToChan[userID]
	if ok {
		p.userMu.RUnlock()
		return chanID, nil
	}
	p.userMu.RUnlock()

	log.Println("Unable to find in cache, creating a new channel!")
	userChannel, err := ds.UserChannelCreate(userID)
	if err != nil {
		return "", fmt.Errorf("Error creating user channel: %w", err)
	}

	p.userMu.Lock() // Update our channel cache for the user
	defer p.userMu.Unlock()
	p.userToChan[userID] = userChannel.ID
	return userChannel.ID, nil
}

func (p *Profiles) onMessageCreate(ds *discordgo.Session, mc *discordgo.MessageCreate) {
	// Ignore all messages created by the Bot account itself
	if mc.Author.ID == ds.State.User.ID {
		return
	}

	userChannel, err := p.getChannelForUser(ds, mc.Author.ID)
	if err != nil {
		log.Println("Error creating new user channel: %w", err)
		return
	}
	log.Println("Private Channel ID:", userChannel)
	msg, err := ds.ChannelMessageSend(userChannel, fmt.Sprintf("Hello, <@%s>", mc.Author.ID))
	if err != nil {
		log.Printf("Error while sending message: %v", err)
		return
	}
	log.Printf("Sent Message: %+v\n", msg)
}
