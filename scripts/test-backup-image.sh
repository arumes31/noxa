#!/bin/sh
# Exercises the pinned backup image's root entrypoint and real gosu drop.
set -eu

image=${BACKUP_IMAGE:-noxa-backup:dev}

stop_signal=$(docker image inspect --format '{{.Config.StopSignal}}' "$image")
[ "$stop_signal" = SIGTERM ] || {
	echo "test-backup-image: expected SIGTERM stop signal, got $stop_signal" >&2
	exit 1
}

output=$(docker run --rm \
	-e POSTGRES_PASSWORD=container-test-password \
	-e BACKUP_RUN_ONCE=1 \
	-e BACKUP_COMMAND=id \
	"$image")
case "$output" in
	*'uid=10001(noxa-backup)'*) ;;
	*)
		echo 'test-backup-image: backup command did not run as the application-data UID' >&2
		exit 1
		;;
esac

if symlink_output=$(docker run --rm --entrypoint sh \
	-e POSTGRES_PASSWORD=container-test-password \
	"$image" -c 'mkdir -p /backups; ln -s /etc /backups/postgres; exec postgres-backup-entrypoint.sh true' 2>&1); then
	echo 'test-backup-image: accepted a symlinked backup directory' >&2
	exit 1
fi
case "$symlink_output" in
	*'postgres-backup: refusing symlinked privileged path'*) ;;
	*)
		echo 'test-backup-image: symlink check failed for an unexpected reason' >&2
		echo "$symlink_output" >&2
		exit 1
		;;
esac

echo 'backup image harness passed'


# Exercise real pg_dump -> encrypted bundle -> age decrypt -> pg_restore,
# including the key files needed to decrypt the application's stored data.
name="noxa-backup-proof-$$"
network="$name-net"
pg="$name-pg"
data="$name-data"
identities="$name-identities"
archives="$name-archives"
cleanup() {
 docker rm -f "$pg" >/dev/null 2>&1 || true
 docker volume rm "$data" "$identities" "$archives" >/dev/null 2>&1 || true
 docker network rm "$network" >/dev/null 2>&1 || true
}
trap cleanup EXIT HUP INT TERM
docker network create "$network" >/dev/null
for volume in "$data" "$identities" "$archives"; do docker volume create "$volume" >/dev/null; done
docker run --rm --entrypoint sh -v "$data:/data" -v "$identities:/identities" "$image" -c '
 mkdir -p /data/keys
 age-keygen -o /identities/test-key 2>/dev/null
 age-keygen -y /identities/test-key > /data/recipients
 printf "fixture pii key" > /data/keys/pii.key
 printf "fixture full key ring" > /data/keys/chat_master.key
 chown -R 10001:10001 /data/keys
 chmod 700 /data/keys; chmod 600 /data/keys/*
 chmod 644 /data/recipients
'
docker run -d --name "$pg" --network "$network" --tmpfs /var/lib/postgresql/data \
 -e POSTGRES_PASSWORD=owner-fixture --entrypoint docker-entrypoint.sh "$image" postgres >/dev/null
attempt=0
until docker exec "$pg" pg_isready -U postgres >/dev/null 2>&1; do
 attempt=$((attempt + 1)); [ "$attempt" -lt 60 ] || exit 1; sleep 1
done
docker exec -i "$pg" psql -X -v ON_ERROR_STOP=1 -U postgres -d postgres <<'SQL'
CREATE TABLE backup_fixture (value text NOT NULL);
INSERT INTO backup_fixture VALUES ('restored relational data');
CREATE ROLE backup_reader LOGIN PASSWORD 'backup-fixture';
GRANT CONNECT ON DATABASE postgres TO backup_reader;
GRANT USAGE ON SCHEMA public TO backup_reader;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO backup_reader;
SQL
docker run --rm --network "$network" --tmpfs /tmp \
 -v "$data:/data:ro" -v "$archives:/backups" \
 -e PGHOST="$pg" -e PGPORT=5432 -e PGUSER=backup_reader -e PGDATABASE=postgres \
 -e POSTGRES_PASSWORD=backup-fixture -e BACKUP_RUN_ONCE=1 \
 -e BACKUP_KEYS_DIR=/data/keys -e BACKUP_AGE_RECIPIENTS_FILE=/data/recipients "$image"
docker run --rm --network "$network" --tmpfs /tmp --entrypoint sh \
 -v "$data:/data:ro" -v "$identities:/identities:ro" -v "$archives:/backups:ro" \
 -e PGHOST="$pg" -e PGUSER=postgres -e PGPASSWORD=owner-fixture "$image" -c '
 set -eu
 archive=$(find /backups/postgres -type f -name "*.tar.gz.age")
 test -n "$archive"
 test "$(find /backups/postgres -type f | wc -l)" -eq 1
 age -d -i /identities/test-key -o /tmp/bundle.tar.gz "$archive"
 mkdir /tmp/restored; tar -xzf /tmp/bundle.tar.gz -C /tmp/restored
 cmp /data/keys/pii.key /tmp/restored/keys/pii.key
 cmp /data/keys/chat_master.key /tmp/restored/keys/chat_master.key
 createdb restored
 pg_restore --exit-on-error --no-owner --dbname=restored /tmp/restored/database.dump
 test "$(psql -X -At -d restored -c "SELECT value FROM backup_fixture")" = "restored relational data"
'
echo 'encrypted database and key recovery passed'
