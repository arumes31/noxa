# Role setup and activation

`role-setup` is a non-serving operator command. Its default mode is read-only
inspection. Its explicit activation mode prepares a clean closed roles-v1
policy when needed, rotates current chat scope keys and activates that policy.
It does not run migrations or start a server.

Build from the repository root:

```sh
go build -o role-setup ./cmd/role-setup
```

Provide the fresh-version database through `NOXA_DATABASE_URL` using the
existing secret configuration process. Select the owner by the exact unique ID
reported by `adduser`:

```sh
role-setup -owner-uid '<exact-unique-id>' -query-timeout 10s
```

The initial connection check has a five-second limit. The query deadline starts
after connecting. Inspection runs in one repeatable-read, read-only transaction;
a database connection configured with `default_transaction_read_only=on` works.
The command requires a roles-v1 fresh-install marker and initialized schema.
Missing tables or columns fail without changing the database.

## Reading the JSON report

- `selected_owner` contains only account ID, exact unique ID, nickname and
  credential-presence booleans. There is no nickname fallback or automatic choice
  from another account. Presence does not validate credential format,
  prove possession, verify login, bypass bans or establish recovery access.
- `configured`, `model`, `active`, `revision` and `configured_owner_id` describe
  stored configuration. The selected owner does not replace the configured owner.
  The stored active flag does not establish what model a running binary serves.
- `state` is `unprepared`, `prepared_inactive`, `active` or `inconsistent`.
  `issues` identifies orphan staging, channels without policies or an invalid
  stored role policy. Database/query errors fail the command instead of producing
  a partial report.
- `counts` inventories accounts, channels and staged role policy. These are inspection counts,
  not a content-preservation manifest or proof of complete enforcement coverage.
- `uncovered_channels` counts current channels without a staged access row.
  Channels created after staging can make coverage
  incomplete; a successfully prepared policy does not imply cutover readiness.

The report omits hashes, public/private key material, privilege-key values,
content, configuration secrets and DSNs. Error output uses fixed diagnostics
instead of raw driver messages. Identity fields remain operator-visible data;
handle the report accordingly.

Exit status 0 means a report was written, including when its state is
`inconsistent`. It is not an activation approval or a successful recovery check.
Invalid arguments, unknown owner, missing schema, database errors, cancellation
and output failure return status 1.

## Activating roles-v1

Stop the server and take the matching database/key/assets backup first. The
activation command holds an exclusive PostgreSQL process lease and refuses to
run alongside a new server or an offline operator command. Old binaries do not
honor this lease, so the operator must still verify that every old process is
stopped.
Then run:

```sh
role-setup -owner-uid '<exact-unique-id>' \
  -activate -confirm ACTIVATE-ROLES-V1 \
  -chat-master-key-file './data/keys/chat_master.key' \
  -query-timeout 30s
```

`NOXA_CHAT_MASTER_KEY` overrides the key file, matching server startup. The
command refuses nickname fallback and repeated activation. If the database is
unprepared, it first stages the owner, empty `@everyone`, unassigned Member,
Moderator and Administrator roles, and a View-channel deny for `@everyone` on
every existing channel. Orphan staging is refused. An interruption between
preparation and activation leaves the policy inactive and the server refuses to
serve it; rerunning the exact activation resumes from that closed state.

Activation requires the configured owner to match, no default member role, no
role assignments, complete channel coverage and a View-channel deny for
`@everyone` on every channel. It rotates the global scope plus every channel
scope and sets `active=true` in the same transaction. Previous key generations
remain readable for authorized history; only the current generation changes.
Failure to generate/wrap a key, write the audit or commit rolls back all key
rotations and leaves the policy inactive.

This version requires a new empty database. It does not migrate old accounts,
permissions or content, and the active server has no legacy fallback.

Activation still does not verify owner password/key possession, bans, backup
integrity or asset recovery. Rehearse on an isolated fresh database, verify
owner login and recovery, and test backup restoration before launch. See
[the approved plan](../tasks/plan.md) and the
[backup/restore procedure](operations/backup-restore.md).

Validation uses disposable PostgreSQL databases: exact-ID selection, read-only
connections, unchanged table digests, secret canaries, absent schema, stored
active/inactive states, orphan staging, incomplete/invalid policy, concurrent
preparation, activation readiness, all-scope key rotation, repeated activation
refusal and transaction rollback. This is not a production reset rehearsal and
no production database was changed during development validation.

The new server holds the same lease from before schema initialization through shutdown
and checks it once per second while serving. `adduser`, `migrate`, chat-key
rewrapping and activation acquire it exclusively. Run `adduser` offline after
activation; its default-role assignment is then visible when the server starts
and loads a fresh Authority snapshot. A second server using the same database
is refused rather than serving an out-of-date policy cache. Read-only
`role-setup` inspection remains available while serving. The lease is a guard
against accidental overlap, not proof that an old process is stopped: a lost
database session releases its lock before the server's next check. The
operator must stop and verify the server process before every offline write.
Registered native and integration logins also refresh Authority from the
stored policy before admission when an offline registration changed member
assignments without advancing the policy revision. An unchanged policy skips
live reconciliation. A single default-role addition may also skip it when the
admitted member has no live native session and gains no revocation; all other
changes reconcile normally.
