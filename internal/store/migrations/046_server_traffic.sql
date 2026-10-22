-- No client IDs, addresses or message data: only aggregate byte counts.
CREATE TABLE IF NOT EXISTS server_traffic_totals (
 instance_id TEXT PRIMARY KEY,
 started_at BIGINT NOT NULL,
 in_bytes BIGINT NOT NULL DEFAULT 0 CHECK(in_bytes >= 0),
 out_bytes BIGINT NOT NULL DEFAULT 0 CHECK(out_bytes >= 0)
);
CREATE TABLE IF NOT EXISTS server_traffic_buckets (
 instance_id TEXT NOT NULL,
 bucket_start BIGINT NOT NULL,
 in_bytes BIGINT NOT NULL CHECK(in_bytes >= 0),
 out_bytes BIGINT NOT NULL CHECK(out_bytes >= 0),
 PRIMARY KEY(instance_id,bucket_start)
);
CREATE INDEX IF NOT EXISTS idx_server_traffic_bucket_start ON server_traffic_buckets(bucket_start);
