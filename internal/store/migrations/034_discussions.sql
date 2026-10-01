CREATE TABLE discussion_channels (
    channel_id BIGINT PRIMARY KEY REFERENCES channels(id) ON DELETE CASCADE,
    forum BOOLEAN NOT NULL DEFAULT FALSE,
    tags TEXT[] NOT NULL DEFAULT '{}'
);
CREATE TABLE discussion_threads (
    id BIGSERIAL PRIMARY KEY,
    channel_id BIGINT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    root_message_id BIGINT REFERENCES chat_messages(id) ON DELETE SET NULL,
    title TEXT NOT NULL,
    tags TEXT[] NOT NULL DEFAULT '{}',
    author TEXT NOT NULL,
    archived BOOLEAN NOT NULL DEFAULT FALSE,
    request_id TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(channel_id, author, request_id)
);
CREATE INDEX discussion_threads_channel ON discussion_threads(channel_id, id DESC);
CREATE TABLE discussion_members (
    thread_id BIGINT NOT NULL REFERENCES discussion_threads(id) ON DELETE CASCADE,
    unique_id TEXT NOT NULL,
    subscribed BOOLEAN NOT NULL DEFAULT TRUE,
    last_read_id BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY(thread_id, unique_id)
);
CREATE TABLE discussion_messages (
    id BIGSERIAL PRIMARY KEY,
    thread_id BIGINT NOT NULL REFERENCES discussion_threads(id) ON DELETE CASCADE,
    from_unique_id TEXT NOT NULL,
    from_nickname TEXT NOT NULL,
    body_enc TEXT NOT NULL,
    key_id BIGINT NOT NULL CHECK(key_id > 0),
    request_id TEXT NOT NULL,
    sent_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(thread_id, from_unique_id, request_id)
);
CREATE INDEX discussion_messages_thread ON discussion_messages(thread_id, id DESC);
