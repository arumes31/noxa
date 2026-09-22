# noXa on orderotto-dev

This is a fresh roles-v1 installation. Old databases and old clients are not
supported. The server and matching Windows client are version 0.5.0.

Connect to `129.121.110.249:12333` (or `100.103.150.8:12333` over Tailscale).
Control/file TLS fingerprint:
`59:6e:51:8f:4b:5d:9e:4a:aa:44:d7:fd:9c:eb:61:40:6e:72:f1:86:dc:22:c2:ab:1d:77:11:a0:c5:fa:32:b6`.
The owner account is `owner`; its generated password stays on the host in
`/container/noxa/secrets/owner-password`. Read it through your authorized SSH
session. Do not copy secrets into source control or paste them into logs.

## Storage and networking

All application persistence is bound under `/container/noxa`:

| Host path | Content |
| --- | --- |
| `postgres` | PostgreSQL 16 data |
| `data/files` | Uploaded files |
| `data/recordings` | Server recordings |
| `data/keys` | Chat and personal-data encryption keys |
| `data/tls` | Stable control and file TLS certificates |
| `secrets` | Database/owner credentials and PostgreSQL TLS |
| `config/config.yaml` | Server configuration |
| `backups` | Database dumps and consistent full backups |

Public ports: TCP 12333 (control), TCP 12336 (files), UDP 12334 and 12341
(media). Query TCP 12335 and health TCP 12337 bind to host loopback only.
PostgreSQL and gRPC are not published. PostgreSQL uses verified TLS.

## Operations

Run on the host:

```sh
docker compose -f /container/noxa/compose.yaml ps
docker compose -f /container/noxa/compose.yaml logs --tail=100 server
curl --fail http://127.0.0.1:12337/readyz
systemctl status noxa-backup.timer
```

The full backup runs daily at 02:45 UTC, with up to two minutes of jitter.
It briefly stops noXa to keep the database, encrypted chat keys and file blobs
consistent, then starts it again. Archives are root-readable only and retained
for seven days. The separate database-only backup runs at 02:15 UTC.
For a manual full backup, run `systemctl start noxa-backup.service`.
Check `journalctl -u noxa-backup.service` after a failed run.

Backups currently stay on this host. An off-host copy is still required to
survive loss of the host or its disk; no off-host destination was supplied.

## Restore procedure

Restore into an isolated directory and PostgreSQL database first. Extract the
full archive, restore its custom-format database dump with `pg_restore`, and
use the matching `data/keys`, files and TLS material. Never initialize or
activate roles again on a restored database. Keep a restored server isolated
from production clients while verifying role state, encrypted history and file
hashes. The process lease prevents two noXa servers from opening one database.

For an actual production restore, stop the server and database, preserve the
current directories, restore the matching archive to `/container/noxa`, restore
the database dump, and start the stack. Do not combine an old database dump
with newer chat keys or uploaded-file directories.

## Media behavior

Camera and screen renegotiation preserve the encrypted transport. A fresh
desktop voice session uses a fresh peer. Tightened dimensions negotiate VP8
and remain enforced by packet inspection. A peer created with dimensions
already limited retains VP8 when those limits are lifted; starting a fresh
voice session restores the broader configured codec set.

Automated desktop media verification uses synthetic camera/audio and a
generated screen pattern through real native clients and the deployed server.
It does not certify physical microphone/camera drivers or every external NAT.
