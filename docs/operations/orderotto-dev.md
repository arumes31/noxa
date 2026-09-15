# noXa on orderotto-dev

Updated on 2026-09-15 to commit
`f57945e02114ddb07f9cbe5759d41ff3860b7336` from PR #14. The server reports
`0.4.3-dev+gf57945e02114` and displays **noXa wowcraft.pw**.
The image was built on the host from an archive of that exact commit:

```text
noxa:f57945e
sha256:99db4c17778311756c8589111bc88ca796e106b0c91ef40a8a63831d7878e47c
```

## Operating the stack

The release lives at `/opt/noxa/releases/f57945e`; `/opt/noxa/current` points
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

The voice/member updates took fresh one-shot PostgreSQL backups and saved
the previous release link and protected deployment environment under:

```text
/opt/noxa/shared/backups/pre-fixes-3a8466e
/opt/noxa/shared/backups/pre-fixes-f57945e
```

Only the application container was replaced. To undo the latest forwarding
change while retaining the guest-membership fix:

```sh
cp /opt/noxa/shared/backups/pre-fixes-f57945e/deployment.env /opt/noxa/deployment.env
ln -sfn releases/3a8466e /opt/noxa/current
/opt/noxa/compose up -d --no-build --no-deps --pull never --wait noxa
```

Before the original rename, the application was stopped and a PostgreSQL dump and
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

- Root/client Go tests and lint, guest assignment race tests and isolated
  PostgreSQL transaction tests passed. Browser tests cover playback, live
  device settings and inactive video tracks.
- Health/readiness passed; schema remains `024_chat_kek_id_range.sql`.
- All expected services are running; the server has zero restarts and no error,
  fatal or panic log entries during deployment verification.
- Two external clients authenticated as guests in the existing Public channel
  (ID 2), using the unchanged pinned TLS certificate.
- Chromium negotiates distinct microphone/camera identities without duplicate
  MSID errors. Subscriber RTP headers omit publisher-specific extensions;
  egress interceptors supply extensions negotiated for each subscriber.
- The [remote-audio regression](../../client/frontend/tests/remote-audio.spec.js)
  connects two RTCPeerConnection instances directly within one browser page.
  It verifies that the client audio-source helper produces a nonzero decoded
  waveform using a silent output sink. It does not exercise deployed-server
  routing or either user's physical speakers.
- Existing channel permissions and the public/Tailscale port bindings were
  preserved. The Public channel remains available without a channel password
  or elevated talk-power requirement.

## Desktop playback and settings

Install the updated Windows client on each participant's machine. A server
update cannot replace the WebAudio code embedded in an existing executable.
Chromium requires a muted playing media element to start pulling a remote
WebRTC audio track into WebAudio; the client retains and tears down that
element alongside each voice/shared-audio source. The WebAudio graph remains
responsible for volume, deafen and the chosen output device.

Audio device settings are local preferences. Saving them no longer sends an
unchanged whisper configuration, which previously caused permission failures
for Public users. Active microphone constraints and the output device now
update without leaving the channel. Inactive reserved camera tracks stay
hidden, including when a participant has never enabled their camera.

Group assignment can now persist an online guest and membership atomically.
The updated client waits for server acknowledgement when the authentication
response advertises `group_assign_ack`. With older servers it sends the
assignment without waiting; failures arrive through the existing asynchronous
error event, so a successful legacy assignment does not close the connection.
