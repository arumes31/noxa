CREATE TABLE chat_polls (
    message_id BIGINT PRIMARY KEY REFERENCES chat_messages(id) ON DELETE CASCADE,
    option_count SMALLINT NOT NULL CHECK(option_count BETWEEN 2 AND 10),
    multiple BOOLEAN NOT NULL,
    closes_at TIMESTAMPTZ NOT NULL,
    closed BOOLEAN NOT NULL DEFAULT FALSE,
    version BIGINT NOT NULL DEFAULT 1 CHECK(version > 0)
);
CREATE TABLE chat_poll_votes (
    message_id BIGINT NOT NULL REFERENCES chat_polls(message_id) ON DELETE CASCADE,
    unique_id TEXT NOT NULL,
    choices INTEGER[] NOT NULL CHECK(cardinality(choices) BETWEEN 1 AND 10),
    PRIMARY KEY(message_id, unique_id)
);
