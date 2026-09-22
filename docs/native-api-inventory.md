# Native API cutover inventory

This is a development checkpoint, not a declaration of complete native parity.
The frontend call sites and corresponding native implementations were inspected
on 2026-09-21. Production still uses the legacy authorization model.

| Surface | Current contract | Remaining work |
| --- | --- | --- |
| Session snapshots, login/reconnect and cached metadata | Expected-tab snapshots include the negotiated authorization model; activation defers legacy queries while identity is pending. Tray reconnect freezes source and destination before its native status read; see `native-session-state.md` | End-to-end cutover rehearsal. |
| Rules acceptance/decline, subscriptions, avatars | Expected-tab reads/writes; decline closes the exact tab; rules/subscriptions use authoritative events | Complete. Native write success is submission, not acceptance. |
| Role/member/channel/access/voice editors and audit viewer | Expected-tab methods and guarded editor lifecycle | Final human UX and threat review with the complete role workflow. |
| Chat history, typing, receipts, sends and mutations | Captured connections; role-mode sends distinguish stored/relayed/queued outcomes; see `native-chat-reads.md` | End-to-end cutover rehearsal. |
| Local DM history and identities | Frozen storage contexts, activation/identity revisions and authenticated tab keys | End-to-end cutover rehearsal; these are local identity boundaries, not server permissions. |
| Member moderation, joins and pokes | Role-mode acknowledgements; disconnect carries the displayed source channel; see `native-role-moderation.md` | End-to-end cutover rehearsal, including unknown outcomes after lost replies. |
| Asset and file writes | Role-mode committed results on captured connections; see `native-asset-mutations.md` and `native-file-transfers.md` | Live authorization-revocation and transfer rehearsal. |
| Presence picker and auto-away | Expected-tab writes, role-mode exact status acknowledgements, recipient-specific invisible eligibility, guarded manual/idle intent and failed-restore suppression; see `native-presence.md` | End-to-end cutover rehearsal. |
| Main menu/tray disconnect | Uses `DisconnectTab` with captured source scope; native intentional-disconnect events remain responsible for notification and replacement replay | Complete; reconnect cancellation and failure feedback retain frontend source-generation guards. |
| Voice metadata and signaling | Expected-tab ICE/limits reads and offers/answers/candidates; guarded capture, queued negotiation and received-track ownership; see `native-voice-session.md` | Physical capture, revocation and ICE recovery rehearsal. Answers/candidates confirm submission only. |
| Priority, whisper, screen declarations and received-video quality | Expected-tab methods and role-mode confirmations; guarded hotkey/settings/capture/teardown/quality completion; see `native-media-controls.md` | Physical media and full cutover rehearsal. `SetPTT` and `SetMuted` emit local events only; delayed PTT release is guarded by session/channel scope and voice epoch. |
| Legacy groups, tier permissions and privilege tokens | Role-mode TCP dispatch rejects the retired message family; frontend entry points, dialog continuations, cache reads and grant events reject role/pending mode. Token controls are disabled; own-role inspection uses snapshots | Legacy-to-legacy `Group*`, `Perm*` and `GetPermissions` calls still use the active manager (except existing `GroupListForTab` token flow). Complete their scope inventory or retire them after cutover parity/reset; no legacy storage/API removal is claimed. |
| Local settings, window/tray controls, capture preferences and identity management | Deliberately local operations; many need no server tab | Distinguish local-only state from methods that also send protocol frames in the remaining inventory. |

The call-site search is a locator, not proof of completeness: aliases, dynamic
bridge access, native menu callbacks and server-originated events also need
inspection. Legacy `Group*`, `Perm*` and token methods must not be mechanically
rewritten into role APIs; their removal depends on parity and the approved reset
procedure. Audit and ban operations are separate retained features.

The broader release gaps remain in `tasks/todo.md`: enforcement inventory,
Query/event and chaos coverage, runtime media configuration, physical capture,
startup authority/reconciliation, default-member registration, copied-database
reset/recovery preservation, legacy retirement and explicit production activation.
