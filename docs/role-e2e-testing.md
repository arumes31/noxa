# Role E2E checklist

The role scenarios in `cmd/e2e` run through the real Query TCP protocol, native
server role adapters, Authority and PostgreSQL in the integration test fixture.
The public CLI uses `roles-v1`; old authorization models are rejected.
This profile establishes the scenarios below,
not complete native, media or integration compatibility. The fixture starts real
health, metrics, UDP, Query, native control and file-transfer listeners with
account authentication, chat storage and persisted encrypted scope keys.

```text
go run ./cmd/e2e -authorization-model roles-v1 -alice-uid <alice-id> -alice-pass <password> -alice-nick <nickname> -bob-uid <bob-id> -bob-pass <password> -admin-uid <owner-id> -admin-pass <password>
```

Use the existing endpoint/TLS flags for the isolated server. The role profile
rejects `-chaos` and payload sizes outside 1 byte–16 MiB before dialing. It stops
at the first failed stage, rather than continuing with missing or uncertain state.

## Fixture and write boundaries

Preparation requires the Query actor to be the actual policy owner. Alice and
Bob must resolve by their exact, distinct unique IDs to manageable, unassigned,
non-owner accounts without role-management authority. Missing, incomplete,
ambiguous or stale roster replies fail before any mutation. The hierarchy check
also needs a Query connection authenticated as Alice, with integration admission
enabled separately from permissions.

The profile checks global ViewChannel, ReadHistory and SendMessages for both
accounts and guests. They must lack Administrator, ManageRoles, ManageChannels,
ManageMessages and BypassSlowmode. Native account/nickname/guest authentication
also finishes before policy mutations. An integration regression supplies a bad
Bob password and verifies that the policy, including revision, stays unchanged.
Enable guest login and configure the isolated server's chat rate limit to at
least 100 messages per window for these burst scenarios. That rate limit is not
exposed by the current metadata API, so the CLI cannot preflight it; ordinary
rate-limit errors remain failures. This runner does not alter server settings.

Use dedicated test accounts and an isolated server. The scenarios create roles
and a permanent test channel, temporarily assign test roles, and write audit
records, global/direct test chat, and files in the test channel. Channel cleanup
removes its uploaded files; global/direct test messages and audit records remain.
They do not reset the server or alter existing role grants/cosmetics. Role
insertion/deletion may renumber existing positions while preserving their order.
The integration fixture creates a uniquely named scratch database, initializes its fresh schema,
registers its own accounts, and drops only that database on cleanup.

Every successful write must acknowledge the exact next policy revision and a
valid resource ID. Created IDs must not have existed in the initial snapshot or
an earlier creation in the same run. Before cleanup is enabled, a read at the
committed revision must confirm the new resource's identity. Cleanup uses that
same revision chain, so it cannot silently overwrite a concurrent policy change.
Unexpected write responses and enforcement-pending acknowledgements stop further
writes; they are never retried automatically. A pending acknowledgement still
means the mutation was saved. Errors identify known test resource IDs that need
inspection when cleanup cannot safely proceed.

## Implemented scenarios

| Scenario | Evidence required |
| --- | --- |
| Role lifecycle | Create, reject stale update, update name/color, assign Bob, read assignment, remove assignment, delete and confirm absence |
| Delegated hierarchy | Alice gains ManageRoles, can edit a lower role, cannot edit her own role, grant Administrator, change the owner or change her own assignments |
| Live role revocation | The same authenticated Alice Query connection loses role-list access after its role is removed |
| Channel lifecycle | Acknowledged creation, exact saved settings, slow-mode edit and confirmed removal from committed policy/live state |
| Guest and member overrides | Explicit guest send grant, @everyone denial, then a registered-member Allow that leaves guest access denied, checked at each committed revision |
| Native authentication | Nickname login resolves to Alice's canonical ID; Bob authenticates by ID; a guest joins with the expected identity; a wrong account password is rejected |
| Native membership and chat | Exact client/channel snapshots precede traffic; channel, guest, global and direct messages correlate to their sender/reference and decrypt correctly |
| Message lifecycle | Encrypted history, own-message edit/delete and tombstone; a non-author's delete receives the concealed denial |
| Live send permissions | Existing Bob/guest sessions cannot send after denial; a Bob-specific Allow restores his send access while the guest remains denied |
| Rejection evidence | Exact error code/request type, unchanged stored history, and an observer message confirming no forbidden reference appeared earlier in its ordered queue |
| Slow mode | First send succeeds, the next is rejected without delivery/storage, and the same guest session recovers after slow mode is disabled |
| File traffic and revocation | Upload/download byte equality and listing; denied new uploads/downloads return the precise error without a token through a subsequent Pong fence; an unused upload token remains invalid after permission regrant; the same sessions can transfer again |
| Restoration and audit | CLI verifies semantically identical final policy, ignoring revision, set ordering and gaps in role positions; the database fixture also verifies no channel/files remain and all ten create/delete lifecycle operations have audit records |

The fixture grants its public baseline before constructing Authority and uses a
chat rate limit of 100 messages per window so burst scenarios test their intended
permissions and slow mode independently. Live checks modify only their own test
channel overrides. Alice remains an allowed sender during denial checks so her
subsequent confirmed message can bound the observer's queue.

The fixture's reconciliation callback updates the channel tree, invalidates file
tokens using the committed evaluator and removes deleted channels' file data.
These scenarios exercise request-time SendMessages changes and file-token
revocation, not ViewChannel or ReadHistory revocation, key rotation, media
revocation or the complete startup reconciliation factory. Broader Query
metadata/log/operator command coverage, event-stream parity, chaos drills and
those runtime checks remain open. Passing this profile is not a cutover gate by
itself; production startup is unchanged.

## Validation

Set `NOXA_TEST_DATABASE_URL` to the local disposable PostgreSQL test service, then
run:

```text
go test -tags integration ./cmd/e2e -run TestRoleE2EScenariosUseCommittedQueryAuthority -count=1
go test -race -tags integration ./cmd/e2e -count=1
```

Unit regressions also cover invalid fixture identities, reused create IDs,
mismatched saved resources, malformed acknowledgements, transport failure and
enforcement-pending writes. No production startup wiring is changed by these
tests.
