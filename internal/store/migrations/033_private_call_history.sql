CREATE TABLE private_call_history (
    call_id UUID PRIMARY KEY,
    revision BIGINT NOT NULL,
    created_at BIGINT NOT NULL,
    ended_at BIGINT NOT NULL,
    participant_uids TEXT[] NOT NULL,
    state JSONB NOT NULL
);
CREATE INDEX private_call_history_members ON private_call_history USING GIN(participant_uids);
CREATE INDEX private_call_history_time ON private_call_history(created_at DESC);
