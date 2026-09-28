# Inbox, history search, and saved messages

The chat header provides **Inbox**, **Search history**, and **Saved messages**.
These tools are scoped to the selected server connection. Switching servers
invalidates pending results and closes the server's dialogs.

## Inbox

Channel and direct-message unread counts and mentions come from messages received in the current
session. Private-group counts and followed-thread unread state are fetched from
the server. Opening the inbox does not mark a group or thread read. The mentions
filter shows channel sources containing an unread mention. Opening an entry
jumps to its message context or thread.

## Search

Search supports text, sender nickname or exact identity, channel, date range,
attachments, and a selected persistent thread. Bodies are decrypted and matched
in the native client. The server receives history-page requests, never the
search text or a plaintext index. The first search checks up to 2,000 messages;
**Continue searching** scans the next batch. **Stop** prevents another page from
starting after the current bounded request finishes.

Results report the number checked, undecryptable messages, remaining history,
and errors for inaccessible sources. An old channel result opens its surrounding
history in a separate context window, without inserting a disconnected page
into the live chat's pagination cache.

## Saved messages

Use **Save message** on channel, direct, group, and thread messages. In Saved messages,
assign collection names, filter by collection, open a message, or remove a
bookmark. This does not modify the original message.

Direct-message bookmarks use the stable client message ID, or a local sequence
for older records. They resolve through the encrypted local history under its
captured identity context. Switching identities invalidates pending actions;
clearing local history makes its old bookmarks unavailable.

The native client stores up to 1,000 references per identity and server in a
private, atomically replaced JSON file beside its settings. Server scope uses
the normalized address and pinned certificate fingerprint. Only source IDs,
message IDs, collection names, and save timestamps are stored. Message bodies,
attachment tokens, and encryption keys are excluded. Opening a bookmark checks
current server access; a deleted message or revoked membership makes the
reference unavailable. Saved messages are local to this device.
