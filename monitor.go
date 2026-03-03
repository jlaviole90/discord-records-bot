package main

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
)

func monitorDiskSpace(s *discordgo.Session, path string) {
	threshold := 90.0
	if t := os.Getenv("DISK_WARN_THRESHOLD"); t != "" {
		if v, err := strconv.ParseFloat(t, 64); err == nil {
			threshold = v
		}
	}

	channelName := os.Getenv("DISK_WARN_CHANNEL")
	if channelName == "" {
		channelName = "bot-alerts"
	}

	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	warned := false

	for {
		usedPct, totalGB, freeGB, err := getDiskUsage(path)
		if err != nil {
			log.Printf("Error checking disk usage for %s: %v", path, err)
		} else {
			log.Printf("Disk usage [%s]: %.1f%% used (%.1f GB free of %.1f GB)", path, usedPct, freeGB, totalGB)

			if usedPct >= threshold && !warned {
				warned = true
				broadcastDiskWarning(s, channelName, usedPct, freeGB, totalGB)
			} else if usedPct < threshold-5 {
				warned = false
			}
		}

		<-ticker.C
	}
}

func getDiskUsage(path string) (usedPct, totalGB, freeGB float64, err error) {
	var stat syscall.Statfs_t
	if err = syscall.Statfs(path, &stat); err != nil {
		return
	}

	total := stat.Blocks * uint64(stat.Bsize)
	free := stat.Bavail * uint64(stat.Bsize)
	used := total - free

	totalGB = float64(total) / (1 << 30)
	freeGB = float64(free) / (1 << 30)
	usedPct = (float64(used) / float64(total)) * 100
	return
}

func broadcastDiskWarning(s *discordgo.Session, channelName string, usedPct, freeGB, totalGB float64) {
	msg := fmt.Sprintf(
		"⚠️ **RAID Disk Space Warning**\nUsage is at **%.1f%%** — only **%.1f GB** free of **%.1f GB** total.\nPlease free up space.",
		usedPct, freeGB, totalGB,
	)

	for _, guild := range s.State.Guilds {
		channels, err := s.GuildChannels(guild.ID)
		if err != nil {
			log.Printf("Error fetching channels for guild %s: %v", guild.ID, err)
			continue
		}
		for _, ch := range channels {
			if strings.EqualFold(ch.Name, channelName) {
				if _, err := s.ChannelMessageSend(ch.ID, msg); err != nil {
					log.Printf("Error sending disk warning to %s/%s: %v", guild.ID, ch.ID, err)
				}
				break
			}
		}
	}
}
