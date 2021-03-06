package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/andersfylling/disgord"
	"github.com/hitecherik/Tabulatron/internal/db"
	"github.com/hitecherik/Tabulatron/internal/hermes"
	"github.com/hitecherik/Tabulatron/internal/multiroom"
	"github.com/hitecherik/Tabulatron/internal/roundrunner"
	"github.com/hitecherik/Tabulatron/internal/rounds"
	"github.com/hitecherik/Tabulatron/internal/util"
	"github.com/hitecherik/Tabulatron/pkg/tabbycat"
	"github.com/joho/godotenv"
)

type options struct {
	round          rounds.Rounds
	db             db.Database
	botTokens      []string
	tabbycatApiKey string
	tabbycatUrl    string
	tabbycatSlug   string
	verbose        bool
	categories     multiroom.Categories
}

var opts options

func bail(err error) {
	if err != nil {
		panic(err.Error())
	}
}

func verbose(format string, a ...interface{}) {
	if opts.verbose {
		fmt.Printf(format, a...)
	}
}

func init() {
	var envFile string

	flag.StringVar(&envFile, "env", ".env", "file to read environment variables from")
	flag.Var(&opts.round, "round", "a round to run")
	flag.Var(&opts.db, "db", "SQLite3 database representing the tournament")
	flag.BoolVar(&opts.verbose, "verbose", false, "print additional output")
	flag.Var(&opts.categories, "categories", "path to the categories TOML document")
	flag.Parse()

	if len(opts.round) == 0 {
		fmt.Fprintln(os.Stderr, "please specify at least one round")
		os.Exit(2)
	}

	bail(godotenv.Load(envFile))

	opts.tabbycatApiKey = os.Getenv("TABBYCAT_API_KEY")
	opts.tabbycatUrl = os.Getenv("TABBYCAT_URL")
	opts.tabbycatSlug = os.Getenv("TABBYCAT_SLUG")

	opts.botTokens = []string{os.Getenv("DISCORD_BOT_TOKEN")}
	for i := 1; true; i++ {
		token := os.Getenv(fmt.Sprintf("DISCORD_HELPER_%v", i))

		if token == "" {
			break
		}

		opts.botTokens = append(opts.botTokens, token)
	}

	bail(opts.db.SetIfNotExists(fmt.Sprintf("%v.db", opts.tabbycatSlug)))
}

func main() {
	clients := make([]*hermes.Hermes, 0, len(opts.botTokens))
	for _, token := range opts.botTokens {
		client := disgord.New(disgord.Config{
			BotToken: token,
		})
		go client.StayConnectedUntilInterrupted(context.Background())

		h := hermes.New(client)
		clients = append(clients, h)

		go h.Listen()
	}

	var rooms []tabbycat.Room
	tabbycat := tabbycat.New(opts.tabbycatApiKey, opts.tabbycatUrl, opts.tabbycatSlug)

	for _, round := range opts.round {
		r, err := tabbycat.GetDraw(round)
		bail(err)
		rooms = append(rooms, r...)
	}

	verbose("Fetched %v pairings\n", len(rooms))

	venues, err := tabbycat.GetVenues()
	bail(err)
	venueMap := roundrunner.BuildVenueMap(venues)

	verbose("Fetched %v venues\n", len(venues))

	messageCounter := 0

	for _, room := range rooms {
		venueName := venueMap[room.VenueId]
		category, err := opts.categories.Lookup(venueName)
		if err != nil {
			log.Print(err.Error())
		}

		for i, team := range room.TeamIds {
			discords, urlKeys, err := opts.db.ParticipantsFromTeamId(team)
			bail(err)

			snowflakes, err := util.StringsToSnowflakes(discords)
			bail(err)

			for j, snowflake := range snowflakes {
				message := fmt.Sprintf(
					"In this round, you will be speaking in **%v** in room **%v**.%v",
					room.SideNames[i],
					venueName,
					addLinks(tabbycat, category.Url, urlKeys[j]),
				)

				clients[messageCounter%len(clients)].SendMessage(snowflake, message)
				messageCounter += 1
			}
		}

		judgeIds := append([]string{room.ChairId}, append(room.PanellistIds, room.TraineeIds...)...)
		discords, urlKeys, err := opts.db.DiscordFromParticipantIds(judgeIds)
		bail(err)

		for j, discord := range discords {
			if discord == "" {
				log.Printf("Adjudicator %v has no discord ID.\n", judgeIds[j])
				continue
			}

			snowflake, err := util.StringToSnowflake(discord)
			bail(err)

			position := "a panellist"
			if j == 0 {
				position = "the chair"
			} else if j > len(room.PanellistIds) {
				position = "a trainee"
			}

			message := fmt.Sprintf(
				"In this round, you will be judging as **%v** in room **%v**.%v",
				position,
				venueName,
				addLinks(tabbycat, category.Url, urlKeys[j]),
			)

			clients[messageCounter%len(clients)].SendMessage(snowflake, message)
			messageCounter += 1
		}

		verbose("Queued messages for room %v\n", venueName)
	}

	for _, h := range clients {
		h.Wait()
	}
}

func addLinks(tabbycat *tabbycat.Tabbycat, venueUrl string, urlKey string) string {
	links := ""

	if venueUrl != "" {
		links = fmt.Sprintf("\n\nThe link to your Zoom room is %v.", venueUrl)
	}

	if urlKey != "" {
		privateUrl := tabbycat.PrivateUrlFromKey(urlKey)
		links = fmt.Sprintf("%v\n\nYour private URL is %v.", links, privateUrl)
	}

	return links
}
