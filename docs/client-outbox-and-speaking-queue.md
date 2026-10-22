# Linked channel chats, outbox and speaking queue

The sidebar places Direct Messages before Voice. Every visible voice channel also
appears under Text as a link to its existing chat, using the same channel ID,
history, subscriptions and unread state. Opening the link does not join voice.
The user's current voice room receives a subtle secondary highlight even while
another text conversation is selected. The ordinary current-chat highlight
remains distinct.

## Delivery choices (ideas 14 and 16)

Standalone text messages in channel, global and direct chat enter a native outbox.
The default undo window is five seconds; Settings > Chat offers Off, 5, 10 or
30 seconds. Before delivery the composer strip offers Undo and Send now.
Undo restores the text without overwriting newer composition; replies retain
their parent ID. The outbox icon opens the full queue for the active server.

While offline, pressing Send retains the draft and offers Queue when online.
Only that explicit action commits an offline message. The original destination
is retained when a voice connection is lost. Attachments, recordings, forum
replies and private-group messages keep their existing send path and are
identified as such in Settings and the outbox. Offline attachment attempts
preserve the staged files and explain that only text can be queued.

Payloads and recipient IDs are sealed on disk using a domain-separated key
derived from the active identity's private key and the authenticated server
namespace. Filenames are hashes. The namespace includes the canonical server
address and pinned certificate and survives teardown of the live TLS state.
Queues are capped at 100 messages and one MiB of sealed data per namespace.

The client pumps connected tabs, including background tabs, while running.
Encryption keys and server authorization are resolved at delivery time.
One persisted sending state and a per-namespace single-flight guard prevent
concurrent duplicate dispatch. An interrupted or failed attempt becomes
unconfirmed and requires explicit retry; the UI advises checking history
because transport failures can make acceptance ambiguous. No exactly-once
delivery guarantee is claimed. Failed final disk writes leave the persisted
sending state recoverable as unconfirmed.

A damaged queue has an explicit Back up and reset action. It preserves the
original sealed bytes under a unique backup filename; reset is rejected while
a send is in flight.

## Speaking queue (idea 74)

Supporting servers advertise the feature in their tree snapshots. The hand
button and room-details queue are absent on older servers. Any current voice
member with Connect access can raise or lower their own hand. Repeated raises
retain the original FIFO position. Server sequence numbers determine order;
snapshots expose only ranks among the recipient's visible room members.

Calling the next speaker, clearing the queue, or lowering another member's hand
requires Mute members in the current channel. Requests include the channel ID,
so delayed actions cannot operate after a move. Current policy and membership
are checked under the existing metadata/policy lease. Queue updates use
recipient-specific snapshot projection, including channel and invisible-user
filtering.

Calling a person marks their turn and notifies that client; it never grants Speak,
changes priority, or changes self/moderator mute or deafen. Moving, leaving,
channel removal and disconnect clear session queue state. Rejoining starts
without a position. A microphone or moderator permission is still required to
actually transmit voice.

## Verification

Native tests cover encrypted persistence, certificate separation, real transport
teardown, explicit offline opt-in, delayed and single-flight delivery, ambiguous
attempts and corrupt-file recovery. Server and state tests cover queue order,
duplicate raises, membership cleanup, permission checks and unchanged mute
controls. Browser workflows cover sidebar ordering, linked navigation, undo,
explicit queueing, scoped UI state and moderator controls.
