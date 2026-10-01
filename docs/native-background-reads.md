# Background read timeout recovery

The native client serializes requests by reply type because control frames do
not carry a request ID. Asset and connection-information reads can time out
without disconnecting voice: avatar, server icon/banner, channel icon, emoji,
client information, and server information requests retain their reply slot
for up to ten additional seconds.

The caller receives its original timeout. A new request for the same reply type
fails immediately while the old reply is outstanding; other request types can
continue. A late typed reply or error with the matching `OriginType` releases
the slot and is discarded. Requests are never automatically replayed.

If the reply still has not arrived when the grace period expires, the client
closes that exact connection. Expiration and response delivery claim ownership
under the same lock. Disconnect cancels all drain timers, and a stale timer
cannot close a replacement connection or a subsequent request.

This exception is an explicit allowlist, not a rule for everything named
"query". Operations that consume state or require reconciliation—including
publication control, negotiation, mutations, and pre-key retrieval—retain
their existing timeout behavior. Socket read/write deadlines also remain in
effect; this does not promise uninterrupted sessions during arbitrary outages.

`client/conn_drain_test.go` covers late success/error responses, same-type
isolation, unrelated reads, bounded expiration, replacement protection, and
mutation timeouts. Native verification additionally pauses the server beyond
the five-second client-information deadline and checks the original clients,
voice memberships, fresh reads, and media recovery after resumption.
