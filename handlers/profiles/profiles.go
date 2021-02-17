package profiles

import (
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/bwmarrin/discordgo"
)

var (
	prefix      = "!"
	setCommands = []string{"set", "setprofile", "editprofile", "editespresso", "setespresso"}
	getCommands = []string{"get", "getprofile", "getespresso"}
)

func checkForCommand(toCheck string, commandPrefix string, commands []string) bool {
	for _, command := range commands {
		if strings.HasPrefix(commandPrefix+command, toCheck) {
			return true
		}
	}
	return false
}

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

func (p *Profiles) setProfile(ds *discordgo.Session, mc *discordgo.MessageCreate) {
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

func (p *Profiles) getProfile(ds *discordgo.Session, mc *discordgo.MessageCreate) {
	msg, err := ds.ChannelMessageSend(mc.ChannelID, fmt.Sprintf("Hello, <@%s>", mc.Author.ID))
	if err != nil {
		log.Printf("Error while sending message: %v", err)
		return
	}
	log.Printf("Sent Message: %+v\n", msg)
}

func (p *Profiles) onMessageCreate(ds *discordgo.Session, mc *discordgo.MessageCreate) {
	// Ignore all messages created by the Bot account itself
	if mc.Author.ID == ds.State.User.ID {
		return
	}

	log.Println(mc.Content)

	// Handle set vs. get
	if checkForCommand(mc.Content, prefix, setCommands) {
		go p.setProfile(ds, mc)
	} else if checkForCommand(mc.Content, prefix, getCommands) {
		go p.getProfile(ds, mc)
	}
}
