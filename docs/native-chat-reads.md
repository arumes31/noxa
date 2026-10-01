# Chat operation server isolation

The frontend requests history and pins through `ChatHistoryForTab` and
`ChatPinsForTab`. Native code checks the expected active tab under the activation
lock before capturing its connection. A switch that precedes the frontend reset
therefore rejects the stale request. A request already underway retains its
original connection and key cache through response decoding and decryption.
Ciphertext and sealed keys are stripped before returning to the webview.

The frontend checks server generation, tab identity and cache identity before
applying a history response. Delayed initial/scrollback completions cannot render
a replacement server's view. Pin panels additionally check panel identity,
conversation scope and request order before applying either results or errors.
Newer panel snapshots supersede pending initial pin loads. Live pin/unpin deltas
received during initial loading are applied over that snapshot, preserving
unrelated pins. Reset and channel deletion invalidate pending cache work.

Compatibility wrappers keep the original native API available. Server-side
permissions, wire messages and chat encryption are unchanged by this binding
change. These client checks do not replace current server authorization.

Validation includes native exact-payload/stale-tab tests, a tab switch during
encrypted history/pin responses, and browser regressions for redirected reads,
late history rendering, cross-server pin content/errors and pin preload ordering.

Search and transcript scans use `ChatSearchForTab` and
`ChatExportHistoryForTab`. Each accepts the expected tab and a nonempty request ID
of at most 128 bytes. The initial lookup captures one connection for every page
and its decryption keys. Scoped progress events carry `{request_id, scanned}`;
legacy wrappers retain their numeric events. Request IDs are correlation values,
not authorization or remote protocol fields.

The frontend assigns a fresh UUID and captures the conversation plus a view
generation. Switching away and back does not revive an obsolete operation.
Search input changes, conversation changes and reset release UI busy state and
subscriptions without allowing an old completion to release a newer request.
Progress from another request, malformed progress, late errors and stale results
are ignored. Rendered search dialogs close when their conversation changes;
their message actions also check the captured scope. Local DM search results use
the same UI guards, while the local history remains tied to the identity.

Export keeps its original scope and attempt through the passphrase dialog,
transcript scan and save feedback. The existing explicit plain-export confirmation
and encrypted-export choice remain. Closing the progress dialog abandons the
result and prevents a later native save invocation. A newer export also suppresses
the previous attempt's late save errors and partial-export notices.

An already-started scan may finish against its captured connection; this is UI
invalidation, not native scan cancellation. Once the native save method has been
invoked, its OS dialog/write may finish using the captured confirmed transcript.
Changing views cannot replace those bytes with another conversation. File-save
cancellation after invocation is not added by these bindings.

Native tests verify stale-tab rejection, request-ID validation, paging after
activation, progress correlation, decryption and transcript ordering. Browser
tests cover native activation gaps, conversation round trips, overlapping search,
local DM scope changes, closed progress, plain/encrypted happy paths and a late
native save failure during a newer export.

Message edits, deletion, reactions and pin changes use `ChatEditMessageForTab`,
`ChatDeleteMessageForTab`, `ChatReactForTab` and `ChatPinMessageForTab`. Each
captures the expected native connection; edits seal their new text with that
connection's current channel key. Existing compatibility methods remain.

The frontend retains conversation/view scope through confirmation, editing and
write completion. Obsolete failures and optimistic reaction updates are ignored,
including navigation between async continuations. Pin-panel writes also retain
panel identity. Conversation changes dismiss old pin panels and reaction strips;
detached controls cannot operate on a replacement view. Edit completion disables
its blur handler before replacing the input to avoid a recursive replacement.

These methods still acknowledge transport writes, not committed server changes.
Authoritative broadcasts and current server permissions retain their existing
roles. Native tests verify encrypted edit metadata and stale-tab rejection; browser
tests cover activation gaps, view round trips, rejected bridge promises, pending
panel closure, detached controls and current action payloads.

Sending uses `SendChatForTab` and `SendChatReplyForTab`; encryption and the wire
write share the captured manager. `UploadChatAttachmentForTab` captures that
manager before base64 decoding and encryption, then retains it through transfer
initialization and data delivery. `DownloadChatAttachmentForTab` similarly keeps
the original source for bounded inline preview. `SaveChatAttachmentForTab`
rejects a stale tab before opening the native dialog and retains its manager
through the subsequent download and atomic file save.

Each frontend send captures the selector, target, conversation/view generation,
reply parent and draft revision before uploading anything. One send runs per
current view. Navigation invalidates its ownership, releases the send control,
clears staged files/reply controls and prevents pending upload completions from
starting token delivery, later uploads or text delivery. Old errors/retries cannot
reach the new view, and an old completion cannot unlock a newer send. A newer
draft edit or reply selection survives the previous send's success, even when
the resulting text/selection is identical. Same-view upload/token failures retain
their existing retry behavior.

FileReader callbacks retain their original destination and are discarded after
navigation. Inline previews and download controls retain the source tab and view
at creation; detached save/zoom controls cannot act on another view. Already
started transfers may finish on their captured server. A completed upload whose
send was abandoned may remain stored without a chat message; no automatic file
deletion is introduced. A native save already invoked may finish writing its
captured attachment. Navigation suppresses its late UI feedback, not the native
file operation.

Tests cover encrypted send payloads, early stale-tab rejection, save-dialog and
upload-init activation changes, draft/reply preservation, duplicate sends,
attachment retry boundaries, delayed file reads and current preview/save behavior.

Typing, delivery and read receipts use `SendTypingForTab`,
`SendChatDeliveredForTab` and `SendChatReadForTab`. Native calls capture the
expected connection before writing. Typing timers capture the original view and
destination when scheduled; navigation cancels them and late callbacks cannot
announce activity in a replacement conversation. Typing/delivery failures are
best effort and never become unhandled bridge rejections.

Read receipts wait for the active DM to be visible and focused. Files-to-Chat,
document visibility and window focus changes trigger queued reads. Every message
arriving while visible retains its exact receipt ID in a per-peer queue, with one
write in flight. The existing latest-message pending behavior remains while
hidden. Failed writes retain their queue entry for a later trigger without a
retry loop. A successful write removes only its own entry; captured server scope
and peer-object ownership prevent old completions from clearing replacement work.
These responses confirm a native transport write, not receipt delivery to the
peer.

Signal tests cover exact native payloads/stale-tab rejection, native activation
gaps, canceled typing, visible bursts, focus retries, bridge failures, chat reveal
and old receipts completing after reset.

Local DM history now captures its directory, public key and derived encryption
key once per native operation. Loads, peer enumeration, multi-peer searches and
exports share that snapshot; append and clear capture before waiting for the
writer lock. A later active-identity change cannot redirect a captured disk
operation. Filename derivation, encrypted file format, retention, deduplication
and atomic replacement are unchanged, and readers remain independent of the
writer lock. Regressions cover activation during load/search/export and captured
write/peer-scan isolation alongside the existing storage tests.

Frontend DM loads, peer restoration, persistence feedback and clear operations
now retain their reset generation and original cache/peer ownership. Closing and
reopening the same peer cannot revive an old result, error or confirmation.
Detached peer controls cannot affect a replacement tab, and a pending peer scan
does not reopen conversations explicitly closed during that scan. Each reset
starts a fresh persistence-warning budget.

A successful clear removes the in-memory rows present when deletion was
requested while retaining later live arrivals. It invalidates pending loads so
they cannot restore deleted content. A load completing during a clear waits for
its outcome: successful deletion discards the load, while failure leaves the
history available. Bridge errors remain contained and are reported only to the
current owner. Native deletion still uses the existing exact-peer operation;
this does not change disk mutation ordering or introduce wider deletion.

The native bridge now exposes `DMHistoryContextForTab` and context-bound variants
of load, append, clear, peers, search and export. A context contains only the tab
ID, identity UID and string-valued activation/identity revisions. It is derived
from that tab's actual storage identity, not the general `IdentityUID()` display
API. Empty tab IDs are accepted only in real offline state, which resolves the
App-selected identity. Every context-bound operation validates all fields before
accessing DM files, then keeps its captured store through completion. Identity
changes and tab activation round trips invalidate prior contexts; accepted
operations still finish on their captured owner.

Capture serializes with identity mutation and revalidates activation after
manager identity resolution. It never holds the tab lock while taking a manager
lock, and releases lifecycle locks before waiting for the history writer lock.
Tests cover stale reads/writes/clear, offline identity selection, captured
load/search/export and append/clear paused behind another writer during an
identity and tab change.

The frontend now uses these context-bound methods for every local DM operation.
It captures its owner before awaiting, including before clear/export dialogs.
Acquisition pins the exact known identity revision. If a newer revision arrives
through the getter before its notification, the old owner and its waiting actions
are invalidated; a fresh owner restores the new view. No obsolete action is
retried with refreshed credentials. A new explicit open can recover a failed
acquisition. Offline restoration uses the same ownership lifecycle.

Native identity changes publish a lossless revision notification, including
offline switches and regeneration. Newer notifications reset DM caches, tabs,
typing/receipt state and offline-summary timers, close old quick-switcher
controls, and refresh restoration. Older, duplicate or malformed revisions are
ignored. Channel history stays intact. Late errors stay scoped, and current
persistence failures are reported once per owner.

An established server tab now retains the key it authenticated with, including
after identity selection or regeneration. This keeps its decrypted journal and
DM storage under the same identity. A new connection captures the App-selected
identity before publishing its tab. Activation emits that tab's identity before
journal replay, so own echoes are routed to their actual peer even while context
acquisition is pending. `myUniqueID` comes from the connection/history context;
the general `IdentityUID()` API remains suitable for the selected login identity.

Tests cover native activation and identity-notification gaps, delayed/newer
context responses, explicit recovery, offline restoration, all six scoped DM
operations, obsolete controls/timers, authenticated replay ordering and preserved
connection keys. This completes the local DM binding slice; the broader
permission cutover remains separate work.

## Committed chat mutations

Role-mode edit, delete, pin/unpin and reaction calls now request and wait for
`ChatMutationSaved`. The server emits it only after the existing storage
operation succeeds, within the existing authorization path. It contains the
submitted operation and message ID; the native client rejects malformed or
mismatched acknowledgements. Permission denial and storage errors remain
correlated with the initiating request and cannot produce a success reply.

All four operations share the native response gate, so only one mutation waits
for that reply type at a time. A wait remains attached to its captured server
after tab activation. Timeout closes that captured transport to prevent a late
reply satisfying a subsequent request. A lost reply leaves the committed outcome
unknown; no automatic mutation retry is introduced. Acknowledgement confirms
storage, not delivery to every viewer. Legacy connections retain unrequested
write behavior; the negotiated authorization model is installed and cleared with
the connection.

Reaction counts and own highlighting in role mode use the authoritative event,
which now includes the affected emoji alongside actor and added/removed state.
This works whether the event precedes or follows the acknowledgement. Legacy
local previews separately track newer counts and per-emoji ownership so older
events without an emoji field still work without double-counting.

TCP tests with a controlled store cover blocked writes, success, denial,
storage failure and unrequested behavior, including pin and unpin. Native tests
cover held replies, invalid replies, server switching and model lifecycle;
browser tests cover event ordering and both legacy event formats.

## Message acceptance

Role-mode sends request `ChatAccepted` and wait on the captured connection. The
reply must match the client message reference and exact submitted destination.
Channel/global sends require a successful history write and a positive stored
message ID. Live DMs report `relayed` after acceptance by the recipient's outgoing
queue; offline DMs report `queued` after the encrypted spool write succeeds.
Neither DM outcome claims receipt or reading by the recipient.

Acknowledged DMs write their own-message echo directly through the serialized
socket writer before acceptance. A full broadcast queue therefore cannot report
success while dropping the sender's history event. Offline plaintext remains
rejected. Storage failure, invalid destinations and permission denial produce
correlated errors, with no success reply. Legacy unrequested sends retain their
existing behavior, including no own echo for offline spooling.

The frontend retains its existing draft, reply and attachment ownership guards;
success now follows server acceptance instead of the native socket write. Tab
switching cannot retarget the wait. A lost or malformed reply leaves the outcome
unknown: check history before manually sending again. There is no automatic
retry. Existing rate, moderation, key and reply checks still apply to duplicate
submissions. A duplicate that reaches history deduplication returns the stored
ID without storing or relaying another message. Offline spool delivery retains
its existing semantics; this change does not introduce DM deduplication or
exactly-once delivery, and a lost connection can still interrupt the own echo.

Tests cover blocked channel/global storage and offline spooling, rejection and
failure, duplicate storage, live destinations, malformed acceptance, tab changes,
legacy sends and a full sender queue. Other native write acknowledgements and
the complete authorization cutover remain unfinished.
