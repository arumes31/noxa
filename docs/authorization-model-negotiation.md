# Authorization model negotiation

The server requires the `roles-v1` authorization model at startup and on each
supported integration transport. Older clients and servers are unsupported.
Declaring compatibility grants no permissions. Authentication, account eligibility,
current policy and hierarchy checks still apply to every protected operation.

## Native TCP and desktop client

`Authenticate.authorization_models` lists supported model names, with a maximum
of eight entries. A role-enabled server requires `roles-v1`, before password
verification, challenge issuance, guest admission or session publication. Missing,
unknown-only and oversized lists receive an unsuccessful `AuthResponse` containing
`authorization_model: "roles-v1"` and an upgrade explanation. A new authentication
attempt invalidates any previously pending signed challenge, including when the
new attempt is incompatible. Signatures and final session admission also check
the negotiated model.

Successful responses identify the selected model. The desktop client advertises
`roles-v1` and rejects any other response model, including an omitted one,
before installing session metadata, encryption keys or publishing its key.

The desktop session snapshot includes `authorization_model` from the same
connection lock as client identity and status. Login, reconnect and tab activation
apply that field only while their tab and session generations still match.
During tab activation the UI holds a local `pending` state until the scoped
session snapshot resolves. `pending` is not a protocol model. The UI shows the
member's visible snapshot roles.

## Query and SSH

Role-enabled Query uses:

```text
login <unique_id_or_nickname> <password> authorization_model=roles-v1
```

The third argument must match exactly, with no extra arguments. Successful login
returns an `authorization_model=roles-v1` data line followed by the normal success
line. Incompatible or malformed retries discard the previous login before password
verification. Existing escaping rules for the first two positional arguments,
including base64 IDs ending in `=`, are unchanged. `help` advertises the model and
login syntax without authentication.

SSH authenticates account credentials in the transport handshake. Each new session
channel must then send the SSH `env` request `NOXA_AUTHORIZATION_MODEL=roles-v1`
before starting its shell or command. For an OpenSSH client, for example:

```text
ssh -o SetEnv=NOXA_AUTHORIZATION_MODEL=roles-v1 integration@server channellist
```

The environment value is local to the channel; another channel on the same SSH
connection must negotiate again. Unsupported replacements and malformed environment
requests clear acceptance. Query-style login in a shell cannot substitute for the
environment request. A negotiated shell may log out and log in again using the
Query syntax above. Unnegotiated protected commands receive an upgrade error;
they never reach the protected backend.

## gRPC

Every supported role-mode unary RPC, including `Authenticate`, requires exactly
one metadata entry:

```text
noxa-authorization-model: roles-v1
```

The server returns the same response header, including on incompatibility errors.
Missing, unsupported, repeated or comma-joined values return `FailedPrecondition`
with an upgrade explanation before credential verification or handler dispatch.
Successful authentication on one RPC grants no session or negotiation exemption
to later calls. Basic credentials remain required separately on protected RPCs.
Unsupported RPCs and raw event streams are unavailable.

Filtered gRPC `Events.Subscribe` requires the same per-RPC model metadata and
mandatory role snapshots. HTTP `/events` requires exactly one
`Noxa-Authorization-Model: roles-v1` upgrade header and confirms it in the
response. Its role frames use the same event schema through protobuf JSON.
Both transports revalidate admission and current projection at delivery and
during idle refresh. See [integration event contracts](integration-role-access.md)
for filters, snapshot replacement, lifetimes and reconnect behavior.

## Remaining tool work

`cmd/loadtest` negotiates roles-v1 by default and requires encrypted chat
confirmation and observed channel membership. See
[role load testing](role-load-testing.md) for capabilities, evidence and limits.
`cmd/e2e` runs its roles-v1 checklist by default and rejects legacy profiles.
It requires a matching authentication response and verifies role membership in
filtered snapshots. See [role E2E testing](role-e2e-testing.md) for the scenario
coverage and fixture requirements.
