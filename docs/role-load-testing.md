# Load testing with roles-v1

`cmd/loadtest` accepts `-authorization-model legacy` (default) or
`-authorization-model roles-v1`. The selected model must match the server's
authentication response. A role-mode run refuses an omitted model; a legacy run
refuses a role model. Unknown selections fail before dialing. This prevents
accidentally reporting a legacy run as role enforcement evidence.

Run against a dedicated test server with the role Authority configured. Production
startup has not switched to roles. For example, after provisioning a test account
and granting the required capabilities:

```text
go run ./cmd/loadtest -authorization-model roles-v1 -addr 127.0.0.1:12333 -tls-insecure -unique-id <test-account-id> -password <test-password> -channel 1 -clients 2 -duration 10s
```

Use certificate verification or an exact fingerprint outside the explicit
loopback-only insecure test mode. Guest scenarios use `-anonymous` instead of
account credentials. The runner grants no permissions and makes no role changes.
The test identity needs global View Channel and Send Messages, plus View Channel
and Connect on the requested channel. `-webrtc` additionally needs Speak and
requires actual RTP reception on each client when testing multiple clients in a
channel. Server passwords, rules admission, capacity, rate limits and moderation
remain independent gates. A shared account also shares per-account rate limits;
choose the client count and test-server limits accordingly. Do not raise production
limits merely to make a test pass.

Role-mode clients generate independent X25519 keys and decrypt the global scope
key delivered at authentication. They send encrypted global chat with distinct
message IDs and payloads; plaintext fallback is unavailable in this mode. Each
client must receive and decrypt its own first message with the matching reference
before the run can succeed. Other events, messages from another client and
unmatched references cannot establish that confirmation. Reports count actual
load-message events separately from sent frames.

For a nonzero target channel, the runner waits until the filtered server snapshot
places its exact client ID there before media setup. A sent join request alone
is insufficient. Global key rotations are retained during snapshot/join/media
setup as well as normal traffic. The client keeps only current and previous
global generations. Corrupt or unavailable keys, invalid ciphertext, server error
frames and premature session termination fail the run. A deadline that arrives
before every client confirms chat also fails. Error diagnostics use fixed
categories and numeric codes, never peer-controlled reasons or credentials.

Legacy mode retains its existing plaintext test traffic and therefore requires a
test server that explicitly permits plaintext. It now also rejects server error
frames and counts chat events rather than unrelated events. This compatibility
mode is not evidence of roles-v1 behavior.

The automated tests exercise real in-process role authorization for account and
guest login, channel joins, encrypted chat and Send Messages denial. They also
cover mismatched server models, scope-key rotation during setup, ciphertext and
echo validation, bounded key retention and teardown races. They do not constitute
a production cutover, a 100-client media rehearsal, or the complete E2E permission
checklist. `cmd/e2e` still requires its legacy group/permission scenarios to be
replaced before it can advertise role compatibility.
