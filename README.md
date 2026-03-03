# Discord Records Bot

A Discord bot that silently records all messages and can resurface them on demand — including edited and deleted messages. When invoked, it reposts the message using a webhook that mimics the original author's name and profile picture.

Built with Go and [discordgo](https://github.com/bwmarrin/discordgo). Stores data in PostgreSQL, designed to run on a Raspberry Pi with RAID storage via Docker Compose.

---

## Commands

All commands work by **mentioning the bot** (`@RecordsBot`) in a message.

| Action | How to invoke | What it does |
|---|---|---|
| **Repost latest message** | `@RecordsBot @user` | Reposts the mentioned user's most recent message in the current channel |
| **Repost deleted message** | `@RecordsBot @user 🗑️` | Reposts the mentioned user's most recently deleted message in the current channel |
| **Retrieve original (pre-edit)** | Reply to any message and tag `@RecordsBot` | Reposts the saved original version of the replied-to message, before any edits |

Every repost appears as if the original user sent it — the bot creates a temporary webhook with their username, display name, and avatar, then cleans it up immediately.

---

## How It Works

- **Message recording** — Every non-bot message is stored in PostgreSQL as it arrives (text content + attachment metadata). Messages in a channel named `quotes` are excluded.
- **Edit tracking** — When a message is edited for the first time, the original content is snapshotted into a separate column. The current content is updated in place. Subsequent edits do not overwrite the snapshot, so the pre-edit original is always preserved.
- **Delete tracking** — When a message is deleted, it is marked as deleted with a timestamp. The content remains in the database for retrieval.
- **Disk monitoring** — A background goroutine checks RAID disk usage hourly and sends a warning to a `bot-alerts` channel (configurable) when usage exceeds a threshold.

---

## Setup

### Prerequisites

- Docker and Docker Compose
- A Discord bot token with the following **privileged gateway intents** enabled in the [Discord Developer Portal](https://discord.com/developers/applications):
  - **Message Content Intent**
- Bot permissions: `Manage Webhooks`, `Send Messages`, `Read Message History`, `View Channels`

### 1. Clone and configure

```bash
git clone https://github.com/your-user/discord-records-bot.git
cd discord-records-bot
```

Create a `discord_token.txt` file in the project root containing your bot token:

```bash
echo "your-bot-token-here" > discord_token.txt
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

---

## Project Structure

```
├── main.go          # Entry point, session setup, signal handling
├── handlers.go      # Discord event handlers and repost logic
├── database.go      # PostgreSQL operations and schema migration
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

> **Note:** Discord CDN URLs for attachments expire and are invalidated when the original message is deleted. Attachment URLs for deleted messages may no longer resolve.

---

## Limitations

- The bot can only record messages sent **while it is running**. Messages sent before the bot was online cannot be retrieved.
- Discord attachment URLs are ephemeral — they may expire or become inaccessible after the original message is deleted.
- The bot requires the **Message Content** privileged intent to be enabled in the Discord Developer Portal.
