#!/bin/sh
set -eu
umask 077
exec 9>/container/noxa/backups/full-backup.lock
flock -n 9 || exit 0
cd /container/noxa
mkdir -p /container/noxa/backups/full
stamp=$(date -u +%Y%m%dT%H%M%SZ)
dump=/container/noxa/backups/full/database-$stamp.dump
archive=/container/noxa/backups/full/noxa-$stamp.tar.gz
restart_server() {
    docker compose -f /container/noxa/compose.yaml start server
}
# Quiesce application writes so blobs, encryption keys and the database agree.
trap restart_server EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
docker compose -f /container/noxa/compose.yaml stop server
docker compose -f /container/noxa/compose.yaml exec -T postgres pg_dump -U noxa -d noxa --format=custom --no-owner --no-privileges > "$dump"
tar -czf "$archive.tmp" -C /container/noxa compose.yaml config secrets data "backups/full/database-$stamp.dump"
mv "$archive.tmp" "$archive"
rm -f "$dump"
restart_server
trap - EXIT HUP INT TERM
find /container/noxa/backups/full -maxdepth 1 -type f -name 'noxa-*.tar.gz' -mtime +7 -delete
printf 'Created full noXa backup: %s\n' "$archive"
