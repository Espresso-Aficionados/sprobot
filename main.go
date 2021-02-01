package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/sadbox/sprobot/handlers/profiles"

	"github.com/bwmarrin/discordgo"
)

const (
	commandPrefix = "."
)

func onMessageCreate(ds *discordgo.Session, mc *discordgo.MessageCreate) {
	// Ignore all messages created by the Bot account itself
	if mc.Author.ID == ds.State.User.ID {
		return
	}

	log.Println("Channel ID:", mc.ChannelID)
	msg, err := ds.ChannelMessageSend(mc.ChannelID, "hey bud")
	if err != nil {
		log.Printf("Error while sending message: %v", err)
		return
	}
	log.Printf("Sent Message: %+v\n", msg)

	err = ds.MessageReactionAdd(mc.ChannelID, mc.ID, ":upvote:769321990283984957")
	if err != nil {
		log.Printf("Error while reacting to message: %v", err)
		return
	}
	log.Println("Reacted to message!")

	err = ds.MessageReactionAdd(mc.ChannelID, mc.ID, ":downvote:769322019929063435")
	if err != nil {
		log.Printf("Error while reacting to message: %v", err)
		return
	}
	log.Println("Reacted to message!")

}

func main() {
	session, err := discordgo.New("Bot " + os.Getenv("DG_TOKEN"))
	if err != nil {
		log.Fatal(err)
	}

	session.StateEnabled = true
	session.Debug = true

	err = session.Open()
	if err != nil {
		log.Fatal(err)
	}

	// helloworld.Setup(session)
	profiles.Setup(session)
	// session.AddHandler(onMessageCreate)
	// Wait for a CTRL-C
	log.Printf(`Now running. Press CTRL-C to exit.`)
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt, os.Kill)
	<-sc

	session.Close()
}
