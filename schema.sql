CREATE TABLE IF NOT EXISTS messages (
    id TEXT PRIMARY KEY,
    guild_id TEXT NOT NULL,
    channel_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    username TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    avatar_url TEXT NOT NULL DEFAULT '',
    content TEXT NOT NULL DEFAULT '',
    sent_at TIMESTAMPTZ NOT NULL,
    edited_at TIMESTAMPTZ,
    is_deleted BOOLEAN NOT NULL DEFAULT FALSE,
    deleted_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS message_contents (
    id SERIAL PRIMARY KEY,
    message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    content_type TEXT NOT NULL,
    content TEXT NOT NULL DEFAULT '',
    filename TEXT,
    url TEXT
);

CREATE INDEX IF NOT EXISTS idx_messages_channel_user_sent
    ON messages(channel_id, user_id, sent_at DESC);

CREATE INDEX IF NOT EXISTS idx_messages_deleted
    ON messages(channel_id, user_id, is_deleted, deleted_at DESC);

CREATE INDEX IF NOT EXISTS idx_message_contents_msg
    ON message_contents(message_id);
