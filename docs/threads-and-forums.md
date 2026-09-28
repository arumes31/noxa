# Channel threads and forums

Open **Threads and forum** in a channel, or choose **Start a thread** on a
channel message. A thread has its own title, messages, membership, followed state,
unread marker, and archive state. The existing reply-chain viewer remains available.

Joining a thread follows its updates by default. Unfollow stops notifications
without leaving; leaving removes membership and the follow state. Reading a thread
advances its read cursor. Following works independently of the voice channel and
the channel-tab subscription. Offline unread state persists on the server.

Threads are public to everyone entitled to read the parent channel. Joining is a
participation preference, not a private access grant. The server checks current
`ReadHistory` for every operation and also requires `SendMessages` for creating
posts and replies. Guests can read but cannot join or mutate. Authors with send
permission and moderators with `ManageMessages` can archive or reopen; archived
threads reject replies. Membership changes, replies, and archiving serialize on the
thread row so an in-flight reply cannot bypass an archive or leave operation.

Administrators with `ManageChannels` can enable **Use this channel as a forum**
and configure up to twelve tags. Configured forum channels open on a post board;
**Channel chat** retains access to prior channel messages and the normal composer.
The directory filters active/archived posts, tags, and followed threads. Its pages
and thread-message pages are bounded at fifty records. Voice recordings can be
used as posts or replies and play inline through the encrypted attachment pipeline.

## Storage and protocol

Migration `034_discussions.sql` adds `discussion_channels`, `discussion_threads`,
`discussion_members`, and `discussion_messages`. Normal startup migration applies
it; no conversion of older reply chains or chat history is needed. Back up the
database and existing external chat-key files together, as for channel chat.
Deleting a channel cascades through its thread metadata, memberships and messages.
Archiving retains history; it is not a deletion or an expiration policy.

Message types 180/181 carry `DiscussionRequest`/`DiscussionResult`.
`DiscussionForTab` captures the original connection and seals message bodies with
the current parent-channel key. Stored bodies remain ciphertext. Historical pages
bundle archival key generations using the existing channel-key authorization,
refusal, and key-budget rules. Retired channel keys remain available under the
existing key retention policy. Native code decrypts and strips sealed key material
and ciphertext before returning messages to the webview.

Thread titles and tags are channel metadata, stored in plaintext like channel
names, and are only returned after channel-history authorization. The UI states
this when creating a post. This is channel encryption, with server moderation;
it does not introduce private-call end-to-end encryption semantics into forums.
Existing word/link filtering, byte limits, slow mode, rate limits and spam checks
apply to post/reply creation. Post and reply request IDs provide persistent retry
deduplication.

The `state` action returns authorized metadata without message bodies or advancing
read state. Notification handlers use it to recheck following/access before showing
a title. Invalidation events include IDs, not message contents. Replies are sent
to current channel recipients and separately to online followers elsewhere on the
server, with current role checks. The browser serializes discussion bridge calls
and discards results after a server/tab generation change.

## Validation

`internal/store/discussion_test.go` exercises real PostgreSQL persistence across
connections, deduplication, membership/follow changes, archive/reopen, tag filters,
pagination, channel isolation and cascade cleanup. `internal/server/discussion_test.go`
covers role denial before storage and author/moderator lifecycle permissions.
The integration-tagged `TestIntegrationDiscussionEncryptedTCPRoundTrip` exercises
real TLS/TCP requests and PostgreSQL together, including a recipient's published
X25519 key, decryption of the bundled history key, ciphertext-at-rest verification,
and plaintext/unauthorized-scope rejection. Native client bridge tests also verify
originating-tab key isolation and removal of wire encryption material.
`client/frontend/tests/discussions.spec.js` covers the UI lifecycle, error/draft
preservation, stale responses, notifications, narrow windows and the forum-board
fallback to existing channel chat.
# Additional discussion controls

Authors and channel message moderators can edit a post's title/tags and mark a question resolved or unresolved. Moderators can pin important posts; pinned posts sort first across paginated results. Titles and tags remain channel-visible metadata, while bodies remain encrypted.

Forum settings support archiving after 24, 72 or 168 hours of inactivity, or never (default). The durable last-message timestamp is checked whenever the channel's discussion state is read or changed, including after server restart. Editing metadata does not extend activity. Reopening starts a new inactivity period. Archived posts remain readable. History search uses a read-only message action that does not clear unread state.
