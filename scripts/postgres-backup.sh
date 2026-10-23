#!/bin/sh
set -eu
umask 077

backup_dir="${BACKUP_DIR:-/backups/postgres}"
retention_days="${BACKUP_RETENTION_DAYS:-7}"
recipient_file="${BACKUP_AGE_RECIPIENTS_FILE:?set a public age recipient file}"
keys_dir="${BACKUP_KEYS_DIR:?mount the application's actual key directory read-only}"
staging_root="${BACKUP_STAGING_DIR:-/tmp}"
# Plaintext staging is permitted only in RAM. The published/remote files are
# always age ciphertext, and the decryption identity stays off this machine.
[ "$(stat -f -c %T "$staging_root")" = tmpfs ] || {
 echo 'postgres-backup: staging requires a tmpfs mount' >&2; exit 1;
}
[ -s "$recipient_file" ] || { echo 'postgres-backup: missing public recipients' >&2; exit 1; }
case "$retention_days" in ''|*[!0-9]*) echo 'postgres-backup: invalid retention' >&2; exit 1;; esac
for name in pii.key chat_master.key; do
 [ -f "$keys_dir/$name" ] && [ ! -L "$keys_dir/$name" ] && [ -s "$keys_dir/$name" ] || {
  echo 'postgres-backup: both real PII and complete chat key-ring files are required' >&2; exit 1;
 }
done
mkdir -p "$backup_dir"
work=$(mktemp -d "$staging_root/noxa-backup.XXXXXX")
stamp=$(date -u +%Y%m%dT%H%M%SZ)
database="${PGDATABASE:-noxa}"
database_id=$(printf '%s' "$database" | sha256sum | awk '{print substr($1, 1, 16)}')
archive_prefix="postgres-${database_id}"
target="$backup_dir/${archive_prefix}-${stamp}-${work##*.}.tar.gz.age"
cipher_tmp="$target.tmp"
child_pid=
cleanup() {
 rm -f "$cipher_tmp"
 # work is a private mktemp-created child of the verified tmpfs.
 rm -rf "$work"
}
stop() {
 if [ -n "$child_pid" ]; then
  kill -TERM "$child_pid" 2>/dev/null || true
  wait "$child_pid" 2>/dev/null || true
 fi
 cleanup
 trap - EXIT HUP INT TERM
 exit 0
}
trap cleanup EXIT
trap stop HUP INT TERM
run_child() {
 "$@" &
 child_pid=$!
 if ! wait "$child_pid"; then child_pid=; return 1; fi
 child_pid=
}
keys_digest() { (cd "$keys_dir" && sha256sum pii.key chat_master.key); }
keys_before=$(keys_digest)
mkdir "$work/keys"
cp "$keys_dir/pii.key" "$keys_dir/chat_master.key" "$work/keys/"
run_child pg_dump --format=custom --no-owner --no-privileges \
 --host="${PGHOST:?PGHOST is required}" --port="${PGPORT:?PGPORT is required}" \
 --username="${PGUSER:?PGUSER is required}" --dbname="$database" --file="$work/database.dump"
# A rotation during the dump could make a copied key ring incomplete. Never
# publish such a backup; retry at the next scheduled run with a stable ring.
[ "$keys_before" = "$(keys_digest)" ] && [ "$keys_before" = "$(cd "$work/keys" && sha256sum pii.key chat_master.key)" ] || {
 echo 'postgres-backup: encryption keys changed during backup; archive not published' >&2; exit 1;
}
printf 'format=noxa-database-and-keys-v1\ncreated_at=%s\n' "$stamp" > "$work/manifest.txt"
run_child tar -czf "$work/bundle.tar.gz" -C "$work" database.dump keys manifest.txt
run_child age -R "$recipient_file" -o "$cipher_tmp" "$work/bundle.tar.gz"
[ -s "$cipher_tmp" ] || { echo 'postgres-backup: encryption produced no output' >&2; exit 1; }
mv "$cipher_tmp" "$target"
cleanup
find "$backup_dir" -type f -name "${archive_prefix}-*.tar.gz.age" -mtime "+$retention_days" -delete
if [ -n "${BACKUP_REMOTE:-}" ]; then
 run_child rclone copyto "$target" "${BACKUP_REMOTE%/}/$(basename "$target")"
fi
trap - EXIT HUP INT TERM
