# Native file transfer isolation

The file browser captures its server tab, view generation, channel and folder before asynchronous work. Listing, versions, link creation, checksum verification, rename/move and deletion use the corresponding `ForTab` native bindings. Upload/download initialization, retries and cancellation do too. The native activation lock rejects requests from an old tab even before the frontend receives its reset event; accepted operations keep their captured connection manager.

Native picker results require the original file view. Once an upload is queued, it retains its original channel/folder while the user navigates within that server. A server reset discards queued work and its polling timer. In-flight callbacks own a distinct task token, so an old completion cannot release a new queue. Already-started transfer workers remain on their originating connection and report through its tab relay.

Transfer records are keyed by tab and transfer ID. Each native tab caches one latest progress record per ID, retaining all active records and the 20 most recently updated finished records. This cache is independent of channel snapshots and disappears when its tab closes. Background updates change the cache without publishing their file metadata into another tab's view.

Tab activation captures an immutable `ft_snapshot` payload under the tab lock, then publishes it after `tab_reset` and before `tab_replay_done`, using the same publication sequencer as live progress. The frontend replaces that tab's records with the snapshot, including an empty snapshot, and prunes retry arguments for omitted records. Thus an evicted completion cannot leave an old active row visible. This is in-memory history; restarting the application does not restore its transfer list.

Explicit Resume verifies the original tab and server address, retains the channel/folder/name/destination/size, and captures the current view generation for the new attempt. Native validation still runs before initialization. Server authorization and the data-port protocol are unchanged: the server checks current file access, and resumed downloads verify the full content digest before replacing the destination.

Validation covers exact native initialization payloads, stale-tab rejection and cancellation isolation; coalesced/empty/bounded replay, closed tabs and concurrent activation; browser picker/drop preparation, queue ownership, background failure recovery, retry targets and cross-tab row isolation.

## File mutation confirmation

Role-mode delete, rename and cross-channel move now wait for `FileMutationSaved`
(160) on the captured connection. The reply echoes the operation, authenticated
connection, source channel/folder/name and submitted destination fields. A zero
destination retains its existing meaning of keeping the source channel. Every
field must be explicit and non-null, including valid zero/empty values, and
the entire tuple must match before the native method returns success.

The server replies only after the existing file backend returns success. Source
visibility, uploader/ManageFiles checks, destination UploadFiles and backend
ownership/quota checks remain in place. Requested acknowledgements require
role authority before effects; legacy unrequested wire behavior is unchanged.
The shared native reply gate serializes these mutations, its 20-second timeout
closes the captured transport, and committed replies have a five-second
writer-lock/transmission bound. Lost replies are uncertain outcomes; no automatic
retry is introduced.

Deletion confirms removal of the logical file record. Physical blob cleanup
remains best effort and can leave residual bytes if filesystem cleanup fails.
Rename/move preserves the backend's existing metadata/blob relocation and
rollback behavior, including acceptance of an already-missing source blob.
An error can require inspecting current state before retrying; neither operation
adds secure-erasure or crash-atomicity guarantees. The browser waits before
showing move success or scheduling its current-view refresh, and obsolete
view results cannot change another server's UI.

Controlled native/TCP tests reproduce premature success, hold effects, reject
malformed/mismatched/partial replies and preserve original-tab completion.
PostgreSQL integration tests block actual DELETE/UPDATE statements, then verify
rows and physical files after success, database rejection, ownership/destination
denial, metadata rollback following blob failure and best-effort deletion
cleanup. Browser tests cover pending results, rejection feedback, success
refreshes and replacement-tab isolation for all three operations.
