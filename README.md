# Discord Records Bot

A Discord bot that silently records all messages and can resurface them on demand — including edited and deleted messages. When invoked, it reposts the message using a webhook that mimics the original author's name and profile picture.

Built with Go and [discordgo](https://github.com/bwmarrin/discordgo). Stores data in PostgreSQL, designed to run on a Raspberry Pi with RAID storage via Docker Compose.

---

## Commands

All commands work by **mentioning the bot** (`@RecordsBot`) in a message.

### Message Reposting

| Command | Example | Description |
|---|---|---|
| **Repost latest** | `@bot @user` | Reposts the mentioned user's most recent message in this channel |
| **Repost deleted** | `@bot @user deleted [N]` | Reposts the user's last N deleted messages (default: 1) |
|  | `@bot @user 🗑️ [N]` | Same as above, using the trash emoji |
| **View edit history** | Reply + `@bot original` | Shows the full edit history of the replied-to message (all versions chronologically) |
|  | Reply + `@bot unedited` | Same as above |

### Summaries

| Command | Example | Description |
|---|---|---|
| **TLDR** | `@bot tldr` | AI summary of the last hour of conversation |
|  | `@bot tldr 6` | Summarize the last 6 hours (max: 24) |

Rate limits apply: 1 use per hour per channel, 5 uses per hour globally per user (configurable).

### Leaderboards

| Command | Example | Description |
|---|---|---|
| **Leaderboard** | `@bot leaderboard` | All-time server leaderboards |
|  | `@bot leaderboard all` | Same as above (explicit all-time) |
|  | `@bot leaderboard 3 hours` | Leaderboard for the past 3 hours |
|  | `@bot leaderboard 7 days` | Leaderboard for the past 7 days |
|  | `@bot leaderboard 2 months` | Leaderboard for the past 2 months |
|  | `@bot leaderboard 24 h` | Shorthand — `h`, `d`, `m` accepted |

Also triggered by `cowards` or `stats`. Displays top 5 for:
- **Most Active** — total messages sent
- **Most Regretful** — total deletes with average time-to-delete
- **Second Thoughts** — total edits with average time-to-edit

### Meta

| Command | Example | Description |
|---|---|---|
| **Help** | `@bot help` | Show the in-chat command reference |

Every repost appears as if the original user sent it — the bot creates a temporary webhook with their username, display name, and avatar, then cleans it up immediately.

---

## How It Works

- **Message recording** — Every non-bot message is stored in PostgreSQL as it arrives (text content + attachment metadata). Messages in a channel named `quotes` are excluded.
- **Edit tracking** — When a message is edited, the previous content is saved to a `message_edits` table before updating. The full chronological edit history is preserved and displayed when requested. The `original_content` column on `messages` holds the very first version as a quick-access snapshot.
- **Delete tracking** — When a message is deleted, it is marked as deleted with a timestamp. The content remains in the database for retrieval.
- **TLDR summaries** — When invoked with `tldr`, the bot queries recent messages from the database, builds a timestamped transcript, and sends it to Google Gemini for summarization. The transcript is capped at 16,000 characters to control API costs. Requires a Gemini API key; the feature is silently disabled without one.
- **Leaderboards** — Aggregated stats for the server, filterable by time window (hours, days, months, or all-time). Queries count messages, deletes, and edits per user, with average reaction times.
- **Disk monitoring** — A background goroutine checks RAID disk usage hourly and sends a warning to a `bot-alerts` channel (configurable) when usage exceeds a threshold.

---

## Setup

### Prerequisites

- Docker and Docker Compose
- A Discord bot token with the following **privileged gateway intents** enabled in the [Discord Developer Portal](https://discord.com/developers/applications):
  - **Message Content Intent**
- Bot permissions: `Manage Webhooks`, `Send Messages`, `Read Message History`, `View Channels`
- *(Optional)* A [Google Gemini API key](https://aistudio.google.com/apikey) for the TLDR feature (free tier: 15 RPM / 1M tokens per day)

### 1. Clone and configure

```bash
git clone https://github.com/your-user/discord-records-bot.git
cd discord-records-bot
```

Create secret files in the project root:

```bash
echo "your-bot-token-here" > discord_token.txt
echo "your-gemini-api-key" > gemini_api_key.txt   # optional, enables TLDR
```

### 2. Review storage paths

The `compose.yaml` is configured to store PostgreSQL data on a RAID mount:

```yaml
volumes:
  - /mnt/raid0/discord-records-bot/postgres-data:/var/lib/postgresql/data
```

Adjust this path if your RAID is mounted elsewhere. The directory will be created automatically by Docker on first run.

### 3. Deploy

```bash
docker compose up -d --build
```

The bot container waits for PostgreSQL to pass its healthcheck before starting. Database tables and indexes are created automatically on first launch.

### 4. Verify

```bash
docker compose logs -f bot
```

You should see:

```
Bot is ready as YourBotName (ID: 123456789)
```

---

## Configuration

All configuration is done through environment variables in `compose.yaml` (or a `.env` file for local development — see `.env.example`).

| Variable | Default | Description |
|---|---|---|
| `DISCORD_TOKEN` | — | Bot token (direct value) |
| `DISCORD_TOKEN_FILE` | — | Path to a file containing the bot token (Docker secrets) |
| `DATABASE_URL` | — | PostgreSQL connection string |
| `RAID_MOUNT_PATH` | — | Filesystem path to monitor for disk usage. Leave empty to disable monitoring |
| `DISK_WARN_THRESHOLD` | `90` | Disk usage percentage that triggers a warning |
| `DISK_WARN_CHANNEL` | `bot-alerts` | Channel name where disk space warnings are sent |
| `GEMINI_API_KEY` | — | Google Gemini API key (direct value) |
| `GEMINI_API_KEY_FILE` | — | Path to a file containing the Gemini API key (Docker secrets) |
| `GEMINI_MODEL` | `gemini-2.0-flash-lite` | Gemini model to use for TLDR summaries |
| `TLDR_CHANNEL_LIMIT` | `1` | Max TLDR uses per hour, per channel, per user |
| `TLDR_GLOBAL_LIMIT` | `5` | Max TLDR uses per hour globally, per user |

---

## Project Structure

```
├── main.go          # Entry point, session setup, signal handling
├── handlers.go      # Discord event handlers and repost logic
├── database.go      # PostgreSQL operations and schema migration
├── gemini.go        # Gemini API client for TLDR summaries
├── monitor.go       # RAID disk space monitoring
├── schema.sql       # Database schema (embedded into the binary)
├── reference.go     # Webhook reference implementation
├── Dockerfile       # Multi-stage Go build → Alpine runtime
├── compose.yaml     # Docker Compose for bot + PostgreSQL
└── .env.example     # Environment variable template
```

---

## Database Schema

**`messages`** — One row per Discord message.

| Column | Type | Notes |
|---|---|---|
| `id` | `TEXT` PK | Discord message snowflake ID |
| `guild_id` | `TEXT` | Server ID |
| `channel_id` | `TEXT` | Channel ID |
| `user_id` | `TEXT` | Author's user ID |
| `username` | `TEXT` | Author's username at time of message |
| `display_name` | `TEXT` | Author's display name at time of message |
| `avatar_url` | `TEXT` | Author's avatar URL at time of message |
| `content` | `TEXT` | Current message text (updated on edit) |
| `original_content` | `TEXT` | Pre-edit text (populated on first edit only) |
| `sent_at` | `TIMESTAMPTZ` | When the message was sent |
| `edited_at` | `TIMESTAMPTZ` | When the message was last edited |
| `is_deleted` | `BOOLEAN` | Whether the message has been deleted |
| `deleted_at` | `TIMESTAMPTZ` | When the message was deleted |

**`message_contents`** — Attachment metadata (one row per attachment).

| Column | Type | Notes |
|---|---|---|
| `id` | `SERIAL` PK | Auto-increment ID |
| `message_id` | `TEXT` FK | References `messages.id` |
| `content_type` | `TEXT` | e.g. `attachment` |
| `content` | `TEXT` | Reserved for future use |
| `filename` | `TEXT` | Original filename |
| `url` | `TEXT` | Discord CDN URL |

**`message_edits`** — Chronological edit history for each message.

| Column | Type | Notes |
|---|---|---|
| `id` | `SERIAL` PK | Auto-increment ID |
| `message_id` | `TEXT` FK | References `messages.id` (cascading delete) |
| `content` | `TEXT` | Message text at that point in time |
| `version_at` | `TIMESTAMPTZ` | When this version existed |

**`tldr_usages`** — Audit log for TLDR rate limiting.

| Column | Type | Notes |
|---|---|---|
| `id` | `SERIAL` PK | Auto-increment ID |
| `user_id` | `TEXT` | User who invoked TLDR |
| `username` | `TEXT` | Username at time of use |
| `channel_id` | `TEXT` | Channel where it was used |
| `guild_id` | `TEXT` | Server ID |
| `hours_requested` | `INT` | Number of hours requested |
| `message_count` | `INT` | Messages included in summary |
| `used_at` | `TIMESTAMPTZ` | When the command was used |

> **Note:** Discord CDN URLs for attachments expire and are invalidated when the original message is deleted. Attachment URLs for deleted messages may no longer resolve.

---

## Limitations

- The bot can only record messages sent **while it is running**. Messages sent before the bot was online cannot be retrieved.
- Discord attachment URLs are ephemeral — they may expire or become inaccessible after the original message is deleted.
- The bot requires the **Message Content** privileged intent to be enabled in the Discord Developer Portal.
