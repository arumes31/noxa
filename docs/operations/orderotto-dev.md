# noXa on orderotto-dev

Updated on 2026-09-15 to commit
`f06b0bb1e4f9df142e4b5b6f9bf7139a1dbeaa58` from PR #13. The server reports
`0.4.3-dev+gf06b0bb1e4f9` and displays **noXa wowcraft.pw**.
The image is pinned to the successful CI build:

```text
ghcr.io/arumes31/noxa@sha256:25c8e70ddc2ba4a3fb562f175606eda30f6bc57276e776b0373bbc997c56b888
```

## Operating the stack

The release lives at `/opt/noxa/releases/f06b0bb`; `/opt/noxa/current` points
there. The wrapper uses `docker-compose.yml`, the host-specific
`docker-compose.orderotto-dev.yml`, the `backup` profile, and the root-only
`/opt/noxa/deployment.env`:

```sh
/opt/noxa/compose ps
/opt/noxa/compose up -d --no-build --wait
/opt/noxa/compose logs --tail 50 noxa
```

The running containers are `noxa`, `noxa-postgres`, `noxa-redis`, and
`noxa-postgres-backup-1`, on the `noxa-net` bridge. PostgreSQL and Redis retain
their previous image versions, now pinned by digest. The renamed backup image
`noxa-backup:f06b0bb` was built from the deployed source. All services restart
unless stopped, with logs limited to three 10 MB files per container.

## Networking

| Host endpoint | Purpose |
| --- | --- |
| Public TCP 12333 | TLS control; connect to `129.121.110.249:12333` |
| Public UDP 12334 | Existing UDP transport |
| Public TCP 12336 | TLS file transfer |
| Public UDP 12341 | Shared WebRTC ICE/media port |
| `100.103.150.8:12337` | Health, readiness and metrics |
| `100.103.150.8:12339` | ServerQuery over SSH |

PostgreSQL and Redis have no host port publications. Raw ServerQuery 12335
and gRPC 12338 listen only on loopback inside the application container.
No host firewall rules were changed.

`NOXA_WEBRTC_UDP_ADDR=:12341` enables the shared IPv4 UDP multiplexer.
`NOXA_WEBRTC_EXTERNAL_IPS=129.121.110.249,100.103.150.8` advertises those
addresses on the same port. Clients still receive their ICE configuration.

## Credentials and storage

Deployment variables now use `NOXA_*`. The database name, user, passwords,
application keys, and storage were preserved. The server join password remains
disabled, as requested. Production validation is enabled; PostgreSQL uses TLS
with `sslmode=require`.

The stack explicitly reuses these existing external Docker volumes:

| Data | Volume |
| --- | --- |
| PostgreSQL | `voicx-pgdata` |
| Database backups | `voicx-pgbackups` |
| Redis | `voicx-redisdata` |
| Files, recordings, keys and TLS | `voicx-data` |

`/opt/noxa/shared` links to `/opt/voicx/shared`. Existing root-only secrets and
PostgreSQL TLS files remain there; do not delete the old directory. The
PostgreSQL certificate expires in September 2027. The initial admin privilege
token remains in the protected `shared/initial-startup.log`.

The application keys and persistent TLS certificate were verified byte-identical
before and after the update. The control/file TLS SHA-256 fingerprint remains:

```text
4a:82:dd:0a:c4:34:6d:85:12:fb:e7:e2:1c:3b:57:c2:77:c4:89:ca:d4:a3:05:39:78:b7:cd:6e:8f:3d:40:5d
```

## Backups and rollback

Before switching, the application was stopped and a fresh PostgreSQL dump and
application-data archive were saved under:

```text
/opt/noxa/shared/backups/pre-noxa-20260915T172925Z
```

The compressed database dump passed decompression and `pg_restore --list`.
The application archive passed a tar integrity check. This directory also
contains the previous environment/overlay and identity checksums. Backups are
protected on the host; no remote backup destination was configured.

The renamed backup service was tested after deployment. It continues daily
compressed PostgreSQL dumps at 02:15 UTC with seven-day retention:

```sh
/opt/noxa/compose run --rm -T -e BACKUP_RUN_ONCE=1 postgres-backup </dev/null
```

The prior `voicx` containers are stopped and retained, together with the old
release, environment, network and images. All 26 historical SQL migration files
are unchanged and this release adds no migrations. To restore that release:

```sh
/opt/noxa/rollback
```

The script stops the noxa stack before starting the prior stack and waiting for
readiness. Both use the same volumes; never run both stacks simultaneously.
Do not remove volumes during rollback. Reassess schema compatibility before
using this procedure after any future update.

## Deployment verification

- All CI checks passed for the deployed commit, including tests, security,
  protobuf compatibility, and both Docker architectures.
- Health/readiness passed; schema remains `024_chat_kek_id_range.sql`.
- All expected services are running; the server has zero restarts and no error,
  fatal or panic log entries during deployment verification.
- Two external clients authenticated as guests in the existing Public channel
  (ID 2), using the unchanged pinned TLS certificate.
- Both WebRTC peers connected and received voice: 1,820 RTP packets sent and
  1,651 received across the test, with no authentication or session failures.
- Existing channel permissions and the public/Tailscale port bindings were
  preserved. The Public channel remains available without a channel password
  or elevated talk-power requirement.
