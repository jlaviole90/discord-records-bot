package main

import (
	"database/sql"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	_ "github.com/lib/pq"
)

var (
	db    *sql.DB
	botID string
)

func main() {
	token := loadToken()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL environment variable is required")
	}

	var err error
	db, err = initDB(dbURL)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Close()

	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		log.Fatalf("Failed to create Discord session: %v", err)
	}

	dg.Identify.Intents = discordgo.IntentsGuilds |
		discordgo.IntentsGuildMessages |
		discordgo.IntentMessageContent

	dg.StateEnabled = true
	dg.State.MaxMessageCount = 100

	dg.AddHandler(onReady)
	dg.AddHandler(onMessageCreate)
	dg.AddHandler(onMessageUpdate)
	dg.AddHandler(onMessageDelete)
	dg.AddHandler(onMessageDeleteBulk)

	if err = dg.Open(); err != nil {
		log.Fatalf("Failed to open Discord connection: %v", err)
	}
	defer dg.Close()

	if raidPath := os.Getenv("RAID_MOUNT_PATH"); raidPath != "" {
		go monitorDiskSpace(dg, raidPath)
	}

	log.Println("Bot is running. Press Ctrl+C to exit.")
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM)
	<-sc
	log.Println("Shutting down...")
}

func loadToken() string {
	if tokenFile := os.Getenv("DISCORD_TOKEN_FILE"); tokenFile != "" {
		data, err := os.ReadFile(tokenFile)
		if err == nil {
			return strings.TrimSpace(string(data))
		}
		log.Printf("Warning: could not read token file %s: %v", tokenFile, err)
	}

	token := os.Getenv("DISCORD_TOKEN")
	if token == "" {
		log.Fatal("No Discord token provided. Set DISCORD_TOKEN or DISCORD_TOKEN_FILE.")
	}
	return token
}

func onReady(s *discordgo.Session, r *discordgo.Ready) {
	botID = r.User.ID
	log.Printf("Bot is ready as %s (ID: %s)", r.User.Username, r.User.ID)
}
