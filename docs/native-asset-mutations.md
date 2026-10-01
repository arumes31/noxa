# Native asset mutation confirmation

Role-mode avatar uploads, server icon/banner uploads and custom emoji
upload/delete/rename now wait for the server's storage result. Existing native
methods and their tab-bound counterparts share the same captured connection.
Their empty-string success return is produced only after a matching reply.

The six requests carry optional `ack_requested`. `AssetMutationSaved` (159)
identifies the original operation, authenticated connection and, for emoji,
the original and replacement names. The native client compares the entire
tuple. Its shared reply gate serializes these asset operations, and its
20-second timeout closes the captured transport so a late reply cannot satisfy
a later request. It never automatically retries an uncertain operation.

The reply is sent inside the authorized success branch after the existing
storage method completes. UploadAvatar, ManageServer and ManageEmoji checks,
guest restrictions, image validation and filesystem protections remain in
place. Requested acknowledgements require role authority before any mutation;
legacy unrequested calls retain their existing behavior. Replies use the shared
five-second writer-lock/transmission bound. Confirmation does not promise event
delivery or new crash-durability guarantees. A failure or lost response can
require refreshing the asset before deciding whether to retry.

Channel icons already have a dedicated confirmed role protocol. Both
`SetRoleChannelIconForTab` and role-mode calls to the older `ChannelIconSet`
methods now use the same captured-manager helper and validate the target reply.
Upload and copy requests reject invalid target/source combinations. Legacy
calls keep the original unacknowledged protocol. Legacy group-icon management
is separately retired/blocked in role mode and is not routed through this API.

Native regression tests reproduce premature success for all six asset mutations
and both channel-icon modes. They verify held denials, exact operation/client/
name matching, malformed and empty-error replies, original-tab completion and
legacy wire behavior. Real TCP server tests hold the storage namespace lock,
then check persisted bytes, deleted assets and renamed paths; they also cover
storage-root failures, invalid requests, permission denial and legacy rejection.
Browser workflows verify pending branding and emoji actions, rejection feedback,
success refreshes and stale-tab protection.

File delete and rename/move confirmations are documented in `native-file-transfers.md`.
