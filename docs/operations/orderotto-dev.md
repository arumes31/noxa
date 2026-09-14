# VoicX on orderotto-dev

Deployed 2026-09-14 from commit `589b9114bf96644bec376373a7ade3caf7543220`
plus the local shared-UDP implementation, guest channel-permission fix and
deployment overlay. The image is `voicx:589b9114-guest-talk`, ID
`sha256:0a5630e89971128d602caa00eef2b2bd0a9040004bbb973163d3e991b51eb58d`.
These deployment changes remain uncommitted in the local workspace.

The stack lives at `/opt/voicx/current` (symlink to `releases/589b9114-guest-talk`).
It uses `docker-compose.yml` plus `docker-compose.orderotto-dev.yml`, the
`backup` profile, and `/opt/voicx/shared/deployment.env`. Use the host wrapper:

```sh
/opt/voicx/compose ps
/opt/voicx/compose up -d --no-build --wait
/opt/voicx/compose logs --tail 50 voicx
```

## Networking

Every service uses the `voicx-net` Docker bridge; none uses host networking.

| Host endpoint | Purpose |
| --- | --- |
| Public TCP 12333 | TLS control; connect to `129.121.110.249:12333` |
| Public UDP 12334 | Existing UDP transport |
| Public TCP 12336 | TLS file transfer |
| Public UDP 12341 | All WebRTC peers share this ICE/media port |
| `100.103.150.8:12337` | Health, readiness and metrics |
| `100.103.150.8:12339` | ServerQuery over SSH |

PostgreSQL and Redis have no host port publications. Raw ServerQuery 12335
and gRPC 12338 listen only on loopback inside the application container.
No host firewall rules were changed. Docker manages the port forwarding.

`VOICX_WEBRTC_UDP_ADDR=:12341` enables Pion's shared IPv4 UDP multiplexer.
`VOICX_WEBRTC_EXTERNAL_IPS=129.121.110.249,100.103.150.8` advertises the public
and Tailscale addresses at that same port. Static host mappings replace server
STUN discovery; clients still receive their ICE server configuration. Empty
settings preserve dynamic ICE gathering for existing installations.

## Credentials and storage

The server join password is disabled (`VOICX_SERVER_PASSWORD=` in the root-only
`/opt/voicx/shared/deployment.env`), as requested. Clients can join without a
server password. Database and Redis passwords are individual
files under the root-only `/opt/voicx/shared/secrets` directory. Do not copy
these files into the repository.

The one-time admin privilege token is saved in the root-only
`/opt/voicx/shared/initial-startup.log`. Redeem it through the client's privilege
token flow after joining. Application keys and the stable TOFU certificate live
in the `voicx-data` volume. The TLS SHA-256 fingerprint is:

```text
4a:82:dd:0a:c4:34:6d:85:12:fb:e7:e2:1c:3b:57:c2:77:c4:89:ca:d4:a3:05:39:78:b7:cd:6e:8f:3d:40:5d
```

Production validation is enabled. PostgreSQL connections use TLS 1.3 with
`sslmode=require`; its self-signed certificate is in
`/opt/voicx/shared/postgres-tls`. Renew it before its September 2027 expiration.
Control and file transfer use the application's persistent TOFU TLS identity.

## Backups and rollback

The backup service creates compressed PostgreSQL dumps daily at 02:15 UTC,
retains seven days, and writes to `voicx-pgbackups`. The first dump passed gzip
integrity and `pg_restore --list` checks. Keys and TLS have a protected initial
snapshot at `/opt/voicx/shared/backups/initial-keys-tls.tar.gz`.
These backups are on this host; no remote backup destination was configured.

```sh
/opt/voicx/compose run --rm -T -e BACKUP_RUN_ONCE=1 postgres-backup </dev/null
```

The prior `releases/589b9114` and `voicx:589b9114-bridge` image are retained;
its environment is `/opt/voicx/shared/deployment.env.pre-guest-talk`. The guest
permission update has no schema migration. To roll it back, restore that
environment, point `current` at the prior release, and run the wrapper's
`up -d --no-build --wait`. The Public channel remains in the database.
For later updates, retain this release and image, take database and application-data
backups before migrations, and apply the backup/restore runbook when schemas
are incompatible. All services restart unless stopped, and logs rotate at
three 10 MB files per container.

## Verification

The permanent **Public** channel (ID 2) is available without a channel password,
join-power requirement, or talk restriction. Guests have default talk power 0,
which permits audio in unrestricted channels. Channel permissions now apply to
guests, including inherited restrictions: an explicit talk deny blocks audio,
and `i_client_needed_talk_power` must not exceed the speaker's
`i_client_talk_power`. No guest administrative privileges were added.

- All deployed server packages passed `go test ./cmd/... ./internal/... ./v1/...`
  and `go vet` for the same packages. `go test ./...` also finds unrelated,
  ignored scratch tests under `temp/live-github-updater-validation`; those fail
  to compile and are excluded from the deployed source archive.
- Shared UDP tests cover concurrent peer candidates, both advertised addresses,
  port collision failures, cleanup, environment decoding and invalid settings.
- Health/readiness returned success; schema is `024_chat_kek_id_range.sql`.
- Two remote clients using the pinned TLS certificate authenticated and exchanged
  Opus audio: 2,193 RTP packets sent, 4,191 received, both receivers active,
  no session or WebRTC failures. Logins were staggered to respect the per-IP
  authentication reservation while password hashing is in progress.
- Public control and file ports were reachable from the workstation. Public
  ports 12335, 12337, 12338 and 12339 were unreachable, while Tailscale health
  and SSH administration were reachable.
