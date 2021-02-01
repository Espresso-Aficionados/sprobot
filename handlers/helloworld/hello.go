package helloworld

import (
	"fmt"
	"log"

	"github.com/bwmarrin/discordgo"
)

func Setup(ds *discordgo.Session) error {
	hw := &HelloWorlder{}
	ds.AddHandler(hw.onMessageCreate)
	return nil
}

type HelloWorlder struct{}

func (hw *HelloWorlder) onMessageCreate(ds *discordgo.Session, mc *discordgo.MessageCreate) {
	// Ignore all messages created by the Bot account itself
	if mc.Author.ID == ds.State.User.ID {
		return
	}

	log.Println("Channel ID:", mc.ChannelID)
	msg, err := ds.ChannelMessageSend(mc.ChannelID, fmt.Sprintf("Hello, <@%s>", mc.Author.ID))
	if err != nil {
		log.Printf("Error while sending message: %v", err)
		return
	}
	log.Printf("Sent Message: %+v\n", msg)
}
