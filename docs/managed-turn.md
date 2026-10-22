# Managed Pion TURN

`turn.managed: true` runs Pion TURN v5.1.2 inside the Noxa control-server process.
The external coturn REST profile remains an explicit alternative and cannot be
combined with managed credentials. Managed mode requires TLS control, an IPv4
advertised address, a realm, TURN URIs and exactly `credentials_ttl: 1h`.

## Configuration

```yaml
turn:
  managed: true
  realm: noxa
  credentials_ttl: 1h
  listen_addr: 0.0.0.0:12340
  public_ip: 203.0.113.10 # replace with the actual reachable server address
  uris:
    - turn:voice.example.org:12340?transport=udp
    - turn:voice.example.org:12340?transport=tcp
  relay_min_port: 12342
  relay_max_port: 12597
  max_allocations: 256
  bytes_per_second: 8388608
  allowed_peers: []
  allow_public_peers: true # required for the existing private/group-call mesh
```

`webrtc.udp_addr` must bind a fixed port. The configured SFU UDP port is automatically allowed at its exact local and
advertised IPv4 addresses. Optional `allowed_peers` entries are exact IPv4:port
exceptions, never subnets. Other private, loopback, CGNAT, link-local, multicast,
reserved and documentation destinations are rejected. The relay's own public
address is denied except for explicit SFU endpoints and active registered relay
allocations (needed for two relayed private-call participants). Same-process
relay traffic is routed to the owned local socket, restoring its advertised
source address for TURN permission checks, without relying on NAT hairpinning. Permission checks
also run on the actual socket destination, not just TURN's IP-only permission.
`allow_public_peers: false` limits traffic to those explicit endpoints and local
active allocations. Public-peer mode permits public UDP destinations; credentials
are bearer credentials and this is not a service-specific public allowlist.

Publish the TURN UDP/TCP listener and the UDP relay range without changing port
numbers. With the repository Compose file, add `docker-compose.turn-managed.yml`
and set the matching `NOXA_TURN_*` variables. Do not start the coturn profile or
set `TURN_SECRET`/`TURN_SECRET_FILE`. `turns:` TLS and RFC6062 TCP relay allocations
are currently rejected; TURN over TCP uses UDP relays and is supported.

## Lifecycle and identity

Only registered, non-bot, authenticated sessions with current Connect permission
and accepted rules obtain leases. Guests and bots retain existing direct/STUN
behavior but receive no managed relay grants. A device is identified by its live
server-generated control session; a client-provided device label is never trusted.
This does not add persistent hardware identity or a ban on future logins.

Each session owns opaque random 256-bit usernames/passwords. The relay holds the
canonical account/session mapping, generation and allocation handles in memory.
Credentials are delivered through TLS control and kept only in the native cache.
Authentication and credential frames are redacted from the client's debug console.
No credential, allocation or packet triggers a database write.

A generation is usable immediately at issue time and expires exactly 60 minutes
later. Refresh starts at minute 50 through 54:15, with random jitter. Repeated
requests return the same generation; at most two unexpired generations coexist.
For example B issued at minute 53 expires at minute 113, not minute 120. Old
allocations retain A's username and stop at A's own deadline. A new credential
cannot refresh an allocation authenticated as another generation.

The native client renews independently of the displayed tab. Existing channel
and private-call peer connections update ICE configuration and restart ICE using
their negotiation queues, keeping tracks and data channels. The browser checks
for a changed selected relay endpoint and performs bounded recovery attempts.
Sleep/wake, stale generations and retired peer ownership are handled explicitly.
Receiving credentials or calling setConfiguration alone is not migration proof.

## Revocation and bounds

`RelayRequest` / `RelayResponse` use message types 193/194 and capability
`managed-relay-v1`. Refresh accepts no identity argument. Revoke selects a live
session; `all_sessions` resolves its authenticated account on the server. Self
revocation is allowed. Other accounts require KickMembers, ViewConnectionInfo
and strict member hierarchy. Revocation requests are limited to one per second per control session. Only a newly blocked session produces an audit; database writes and replies run after releasing the policy lease. The participant context menu exposes the matching
session/all-session actions when available. Revocation lasts for these sessions;
a new login needs a fresh, independently authorized grant.

Issuance shares the existing authoritative policy gate. Explicit revocation is
serialized with issuance by the registry lock under a shared policy lease, marks the session blocked, invalidates every generation
including replacements, and closes its UDP relay sockets. Kick, ban and control
disconnect also remove relay access. Permission reconciliation revokes relay
access when no permitted Connect scope remains. Slot-specific voice/camera/screen
restrictions continue to be enforced at the SFU without dropping permitted media.
Pending rules revoke relay access immediately.

Socket wrappers recheck grant expiry, authorization lease, destination and byte
limits on actual traffic. The byte budget is shared per session across every
allocation and generation; allocation caps are 16 per session, 64 per account
and the configured server total. Accepted TCP connections are capped at 512, with at most 64 pending authentication globally and eight per source IP. Pending streams close after a fixed 30 seconds even if bytes trickle in. Only a successfully created allocation cancels that deadline; the earlier credential-key lookup cannot authenticate a stream. Allocation deletion closes its TCP transport. Admitted streams also expire after 60 seconds without inbound transport traffic. New allocations and authenticated TURN refresh requests
check the live registry. Registry teardown serializes against allocation creation
and bounded UDP writes; no client disconnect acknowledgment is trusted. In-flight
network packets can still arrive after the revocation call. A 5-second normal
revocation target is tested locally, not a hard real-time network guarantee.

The authority renews a 15-second in-memory authorization lease every 5 seconds.
If it becomes unavailable, expiry prevents forwarding even without a notification;
the one-second sweeper also closes idle sockets. Half-open control connections
lose eligibility after 45 seconds without any received control frame, with up to
5 seconds until reconciliation (the normal server timeout can close them sooner).
Explicit disconnect/revocation does not wait for that timeout. Restart discards
all credentials and requires clients to reconnect; no shared signing secret rotates.
The registry is single-process. Multi-replica relay ownership is not implemented.

Direct private/group calls remain P2P. Modified peers can keep a direct connection
after server revocation. Relay revocation only cuts traffic traversing this relay;
absolute server-enforced cutoff for direct calls requires a separate topology
change. Existing encrypted private signaling, DTLS/SRTP and chat keys are preserved.

## Verification and rollout

`go test ./internal/turn ./internal/server` covers expiry, overlap, idempotency,
revocation isolation, forged session selectors, hierarchy, rules/guest/bot denial,
reconnect teardown and actual bidirectional UDP/TCP relay traffic. CI runs the Go
race detector. `NOXA_TURN_BROWSER=1 go test -v -run TestBrowserRelayContinuity
./internal/turn` uses installed Playwright Chromium, synthetic microphone/camera
and screen video plus a reliable data channel. It forces TURN for both peers,
shortens fixture expiry/refresh and polling intervals, and requires >=3 generations
and changed selected allocations, no data loss, audio progress gaps <=200ms,
video-frame gaps <=500ms and data gaps <=500ms. `NOXA_TURN_TEST_IP` optionally picks
a local adapter; Chromium on Windows cannot gather UDP on the loopback adapter.

Initial Windows LAN run: four generations, 750/750 data messages per transport;
maximum sampled audio-progress gaps 76.8ms UDP / 76.1ms TCP, video-frame gaps
166.7ms / 133.5ms, data gaps 22.5ms / 22.0ms. This is accelerated local evidence,
not a multi-hour WAN measurement, audible-quality assessment or production SLA.
The test exercises the shared renewal controller with real peer connections;
channel and private-call application tests additionally check their integration.

Roll out server and supporting clients together. Older clients do not understand
managed relay renewal and retain only their configured STUN/direct paths. Validate
public listener/range reachability, endpoint restrictions and private calls before
expanding use. Preserve the previous immutable image and config for rollback.
Rolling back to legacy coturn loses individual active-allocation revocation; do
not describe it as retaining managed mode's security guarantees.
