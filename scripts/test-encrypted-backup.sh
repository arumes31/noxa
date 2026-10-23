#!/bin/sh
set -eu
repo=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
work=$(mktemp -d /dev/shm/noxa-backup-test.XXXXXX)
trap 'rm -rf "$work"' EXIT HUP INT TERM
mkdir "$work/keys" "$work/out" "$work/bin" "$work/restored"
age-keygen -o "$work/identity" 2>/dev/null
age-keygen -y "$work/identity" > "$work/recipients"
printf 'test pii key' > "$work/keys/pii.key"
printf 'test complete key ring' > "$work/keys/chat_master.key"
cat > "$work/bin/pg_dump" <<'DUMP'
#!/bin/sh
[ "${FAIL_DUMP:-0}" = 0 ] || exit 1
for arg in "$@"; do case "$arg" in --file=*) printf 'test database payload' > "${arg#--file=}";; esac; done
if [ "${ROTATE_KEYS:-0}" = 1 ]; then printf 'changed ring' > "$BACKUP_KEYS_DIR/chat_master.key"; fi
DUMP
chmod +x "$work/bin/pg_dump"
export PATH="$work/bin:$PATH" BACKUP_DIR="$work/out" BACKUP_KEYS_DIR="$work/keys"
export BACKUP_STAGING_DIR=/dev/shm BACKUP_AGE_RECIPIENTS_FILE="$work/recipients"
export PGHOST=localhost PGPORT=5432 PGUSER=test PGDATABASE=test
"$repo/scripts/postgres-backup.sh"
archive=$(find "$work/out" -type f -name '*.tar.gz.age')
[ -n "$archive" ]
age -d -i "$work/identity" -o "$work/restored.tar.gz" "$archive"
tar -xzf "$work/restored.tar.gz" -C "$work/restored"
cmp "$work/keys/pii.key" "$work/restored/keys/pii.key"
cmp "$work/keys/chat_master.key" "$work/restored/keys/chat_master.key"
[ "$(cat "$work/restored/database.dump")" = 'test database payload' ]
[ "$(find "$work/out" -type f | wc -l)" -eq 1 ]
# Failures must not publish another backup or retain plaintext in the target.
if FAIL_DUMP=1 "$repo/scripts/postgres-backup.sh"; then exit 1; fi
if ROTATE_KEYS=1 "$repo/scripts/postgres-backup.sh"; then exit 1; fi
if BACKUP_AGE_RECIPIENTS_FILE="$work/missing" "$repo/scripts/postgres-backup.sh"; then exit 1; fi
[ "$(find "$work/out" -type f | wc -l)" -eq 1 ]
[ -z "$(find /dev/shm -maxdepth 1 -type d -name 'noxa-backup.??????')" ]
printf 'encrypted backup round-trip and failure boundaries passed\n'
