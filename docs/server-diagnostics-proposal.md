# Proposed role-mode diagnostic log access

Status: deferred outside the roles-v1 cutover. Remote `logview` is retired;
no role-mode log endpoint is implemented or enabled. This proposal does not
authorize a deployment or a log retrieval.

The existing process log ring has no trusted channel scopes. Its entries can
contain addresses, identifiers, diagnostic context and other sensitive material.
It must not be exposed through ViewAuditLog, ManageServer, Administrator or old
account is_admin flags.

The proposed replacement is Query/SSH `logquery data=<escaped JSON>` and typed
gRPC `GetServerLogs`, available only to the current protected server owner with
explicit integration eligibility and roles-v1 negotiation. Fresh canonical
admission and owner checks would be held through bounded socket delivery, using
the existing protected read helpers. Ownership changes, bans and disabled
integration access would apply on the next read. No new server permission would
delegate raw logs.

Request: optional `lines` (zero/default 50, maximum 500) and `filter` (at most
128 UTF-8 bytes, no control characters, case-insensitive substring). Response:
chronological matching lines from the current 500-entry in-memory ring, not a
durable audit history. Each examined line is limited to 16 KiB and selected raw
line bytes to 256 KiB. Invalid UTF-8 or exceeded bounds fail the entire read;
there is no successful partial response. Wire encoding adds overhead. The
provider result must be validated again before delivery, and Query must reject
malformed UTF-8 before JSON decoding can normalize it.

Raw diagnostic content would be returned without per-channel filtering or
secret redaction. This is the scope needing explicit approval. The existing
filtered audit log remains the management alternative.

Live following is not included in this first bounded-read proposal. It needs
its own revocation, backpressure and dropped-line contract. Remote stop/restart
is also separate and remains closed in role mode.

Validation before delivery would cover current/former owner, Administrator and
legacy-admin denial, disabled/banned accounts, malformed input, provider bounds,
Query/SSH/gRPC escaping and negotiation, cancellation and socket-delivery leases.
Implementation tests would use fixtures; no production diagnostics would be read.

Automatic approval review rejected endpoint implementation because the general
permission-rework request did not specifically authorize exporting potentially
sensitive raw diagnostics over Query/gRPC. Preparatory endpoint changes were
removed, including the unwired bounded-reader prototype; no runtime change remains.
