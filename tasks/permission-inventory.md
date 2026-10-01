# Permission replacement inventory

This is a development coverage checklist, not a translation of existing grants.
The new configuration starts empty/closed. Runtime integration is still pending.

## Legacy key dispositions

| Existing keys | Replacement |
| --- | --- |
| `i_channel_join_power`, `i_channel_needed_join_power` | Connect in the explicit destination channel; remove numeric threshold. |
| `b_channel_join_ignore_password` | BypassChannelPassword; admission and operational checks still apply. |
| `i_channel_subscribe_power`, `i_channel_needed_subscribe_power` | ViewChannel in the subscription/key scope. |
| `b_channel_create_child`, `b_channel_create_permanent`, `b_channel_create_semi_permanent` | ManageChannels in the parent scope; lifecycle type remains a channel setting. |
| `b_channel_create_temporary` | CreateTemporaryChannels in the parent scope. |
| `b_channel_delete`, `b_channel_modify`, `i_channel_modify_power`, `i_channel_needed_modify_power` | ManageChannels for the target; access changes separately require ManageChannelAccess. |
| `i_client_kick_from_channel_power`, `i_client_needed_kick_from_channel_power` | DisconnectMembers in the target's channel plus member hierarchy. |
| `i_client_kick_from_server_power`, `i_client_needed_kick_from_server_power` | KickMembers at server scope plus member hierarchy. |
| `b_client_ban`, `i_client_ban_power`, `i_client_needed_ban_power` | BanMembers plus member hierarchy; bans remain admission restrictions. |
| `i_client_move_power`, `i_client_needed_move_power` | MoveMembers in source/destination, member hierarchy and destination access. |
| `b_client_use_channel_command` | Retired catalog-only legacy switch: no implemented command handler or client operation. Omitted from the new editor rather than promising an unavailable action. |
| `i_client_talk_power`, `i_client_needed_talk_power` | Speak; moderation mute/deafen stays session state. |
| `i_client_whisper_power`, `i_client_needed_whisper_power` | Whisper with explicit recipient/channel scopes. |
| `b_client_video_publish` | Separate ShareCamera and ShareScreen checks by source kind. |
| `b_client_issue_screenshare_1080p` | Legacy declared-height check only; it never verified encoded dimensions. Independent startup `video_max_bitrate` caps relayed RTP across each publisher's slots/layers; `video_max_width`/`video_max_height` enable VP8-only negotiation and encoded dimension inspection. Native capture constraints/feedback and runtime editor integration remain. See docs/media-resource-limits.md. |
| `b_client_priority_speaker` | PrioritySpeaker. |
| `b_client_ignore_antiflood` | Retire unrestricted bypass; infrastructure rate limits apply to all. |
| `b_client_request_talker` | Retired catalog/conflict-warning legacy switch: no request/approval handler or client operation exists. Omitted from the new editor; any future workflow needs its own implementation and contract. |
| `i_ft_file_upload_power`, `i_ft_needed_file_upload_power` | UploadFiles at target channel; authenticated uploader identity remains required. |
| `i_ft_file_download_power`, `i_ft_needed_file_download_power` | DownloadFiles at target channel; recheck outstanding transfer authorization. |
| `i_ft_quota_mb_upload_per_client` | `file_user_quota_mb` is a backend resource ceiling for uploads and cross-channel moves, including owner/Administrator. Zero defaults to unlimited; callers cannot override an explicit ceiling. Rechecked at commit under the file mutation lock with content dedup accounting and source-reference release for moves. |
| `b_ft_delete` | ManageFiles for others' files; own-file operations retain channel access requirements. |
| `b_client_avatar_upload` | UploadAvatar, authenticated identity and image limits. |
| `i_client_poke_power`, `i_client_needed_poke_power`, `b_client_poke` | PokeMembers; recipient blocking/DND and rate limits remain independent. |
| `b_client_serverquery_view`, `b_virtualserver_connectioninfo_view` | ViewConnectionInfo; transport access does not itself confer authority. |
| `b_client_remoteaddress_view` | ViewRemoteAddresses; never leak through roster/preview. |
| `b_permission_modify_power`, `i_permission_modify_power`, `b_permission_modify_power_ignore` | Retire grant ceilings/bypass; pre-change capabilities and strict role hierarchy. |
| `b_virtualserver_select`, `b_virtualserver_info_view` | Admitted-session basic metadata; protected metadata explicitly filtered. |
| `b_virtualserver_recording` | RecordChannel, channel scope and independent recording policy. |
| `b_virtualserver_token_list`, `b_virtualserver_token_add`, `b_virtualserver_token_delete` | Retired in role mode. No role-native invitation management exists; unimplemented ManageInvitations is omitted from the capability catalog. Future role-grant invitations require their own authorized implementation. |
| `b_virtualserver_token_use` | Retired in role mode. No old privilege-key redemption or advertised UseInvitations grant; role-grant automation is deferred. |
| `b_chat_delete_any` | ManageMessages at the message's actual scope. |
| `b_chat_mention_all` | MentionEveryone. |
| `b_chat_slowmode_bypass` | BypassSlowmode, distinct from infrastructure rate limits. |
| `b_emoji_manage` | ManageEmoji. |
| `b_chat_filter_manage` | ManageChatFilters for reading and writing the moderation configuration. |
| `b_client_is_bot` | Explicit default-false `users.is_bot` identity attribute (migration 026, operator `adduser -bot`); every account login path preserves it. Role-mode badges ignore old permission grants; no privilege or rate-limit exemption. |
| `b_server_group_manage`, `b_channel_group_manage`, `b_permission_manage` | ManageRoles / ManageChannelAccess by action; remove raw legacy mutation APIs. |
| `b_audit_view` | ViewAuditLog with protected-target filtering. |

New explicit gates also cover SendMessages, ReadHistory, ViewChannel, MuteMembers,
DeafenMembers and ManageServer where the old implementation relied on admission,
subscription state or the independent admin flag.

The current named-capability paths and test limits are recorded in
`docs/capability-enforcement.md`. ViewConnectionInfo includes invisible-member
visibility as well as connection statistics; its English/German labels disclose
both effects. This audit does not complete the exhaustive legacy bypass inventory.

## Enforcement seams and remaining cutover work

| Area | Current source seams | Required replacement checks / work |
| --- | --- | --- |
| Admission/identity | server/handlers.go, auth/service.go, cmd/server/main.go, cmd/adduser | Negotiate roles-v1, use protected owner/Administrator, remove is_admin authority and automatic group assignment. Keep bans/passwords/rules. |
| Channel tree | server/handlers.go, server/role_channel_api.go, store/role_channels.go, broadcast/snapshot.go, channels/manager.go | Filter snapshots/events; explicit destination checks; create policy with channel lifecycle; atomic reparent keep/sync and all descendant evaluation. Role moves optionally commit signed order with parent/policy/revision/audit, preserving omission versus explicit zero across native/Query/SSH/gRPC. |
| Subscriptions/keys | server/subscriptions.go, server/e2e.go | ViewChannel before current/archival key delivery. Revoke subscriptions and rotate affected keys before later ciphertext. Remove group auto-assignment. |
| Chat | server/chatx.go, server/handlers.go | SendMessages/ReadHistory/ManageMessages against message scope; explicit global/DM policy; no hidden lookup leaks. |
| Voice/video | server/voice.go, internal/webrtc | Connect/Speak/source-specific publish; hierarchy for moderation; stop current unauthorized forwarding and publication. |
| Files | server/files.go, internal/filetransfer | Target-channel checks at issue and transfer/use time; quotas independent; revoke standing tokens. |
| Moderation/presence | server/admin.go, presence.go, extras.go | Capabilities plus hierarchy; owner protected; server config uses ManageServer. |
| Role administration | internal/authorization, store/roles.go, server/roles.go | Implemented transactional pre-change authority/revision/audit, live reconciliation and readable actual before/after audit. Production activation remains separate. |
| Alternate transports | auth/integration.go, query/roles.go, query/role_management.go, query/role_inspection.go, query/role_channels.go, query/role_moderation.go, query/role_removal.go, query/role_bans.go, server/role_integrations.go, server/role_integration_channels.go, server/role_integration_moderation.go, server/role_integration_bans.go, grpcserver/roles.go, grpcserver/role_channels.go, grpcserver/role_moderation.go, grpcserver/role_removal.go, grpcserver/role_metadata.go, grpcserver/role_inspection.go, grpcserver/role_discovery.go, server/queryadmin.go, cmd/server queryBackend | Explicit opt-in account identity; Query/SSH clientlist/channellist/channelinfo use filtered snapshots through bounded delivery. Rolelist/rolechange reuse native scoped projection and transactional authority/audit, including admission under the writer barrier. Rolemembers/accesscheck share native roster/decision semantics under bounded delivery. Channelquery/channelchange share native options and transactional lifecycle. Membervoice/membermove/memberdisconnect share native moderation and membership lifecycle, including hierarchy, visibility, current scopes, capacity, admission and source-scoped audit. Memberkick/memberban share exclusive session/account revocation; banquery provides bounded current-admission/BanMembers recovery pages through protected delivery. gRPC integration login and typed ChangeRoles/ChangeChannel/SetMemberVoice/MoveMember/DisconnectMember/KickMember/BanMember use the same authority. Serverinfo/clientinfo/serverconfig share native filtered counts, target privacy and separate statistics/address/configuration gates. gRPC ListChannels now shares the native filtered projection with a socket-completion observer retaining the lease beyond handler return. Typed gRPC GetServerInfo/GetClientInfo/GetServerConfig/ListBans share native metadata/ban callbacks and the same protected delivery boundary. Typed gRPC GetRoleState/ListRoleMembers/CheckAccess/GetChannelOptions share native scoped management, revision-aware roster/decisions and lifecycle preflight callbacks through protected delivery. gRPC ListClients/GetChannelInfo match Query discovery fields through the same native snapshot. Query/SSH auditquery and gRPC ListAuditLog share native scope-aware audit projection and bounded delivery. The operation matrix in docs/integration-operation-inventory.md records the supported complaint, settings, server-text, custom-annotation and filtered gRPC/WebSocket event adapters, plus all 33 closed legacy Query commands. Explicit complaint single-ID/global deletion, log/operator lifecycle decisions and broader bundled E2E coverage remain. Legacy shared authentication stays closed in role mode. See docs/integration-role-access.md. |
| Native/frontend | client/roles.go, generated Wails bindings, role/access modules, clientinfo.js, main connection state | Editors, negotiated model propagation, role cosmetics and scoped/confirmed native paths are implemented as recorded in docs/native-api-inventory.md. Legacy-to-legacy group bridge inventory, full workflow rehearsal and retirement remain. |
| Reset/retirement | migrations 025-029, store/roles.go, store/role_setup.go, cmd/role-setup | Transactional activation and legacy authority removal are implemented. Copied-database owner/content/recovery rehearsal and migration 029 runtime verification remain pending. See docs/role-setup-preflight.md. |

No row in this inventory is evidence that its runtime cutover is complete.

## Legacy authority search audit (2026-09-21, updated for cutover)

All 65 declared legacy keys had a disposition in the table above. That was a
name-coverage result, not proof of runtime enforcement. The old resolver,
numeric-permission tables, account admin flag and native group/token APIs have
since been retired. The current cutover state of the audited families is:

| Family | Current role-mode boundary / remaining action |
| --- | --- |
| Native groups, raw permissions, admin roster and privilege keys | Retired from the native protocol and desktop bindings. Roles-v1 inspection and management replace authority-bearing operations. |
| Trusted Query/SSH/gRPC administration | Old trusted-login entry points are closed. Typed role adapters use current account admission and Authority; see the operation inventory for supported and explicitly retired commands. |
| Chat, files, moderation, media, metadata and server configuration | Handlers select role capability paths before legacy resolvers; named-capability evidence is in `docs/capability-enforcement.md`. Legacy file cross-channel/guest resolvers are still present. |
| Authentication, snapshots and presence | Role projections replace account-admin authority. Account bot metadata remains identity only. |
| Default role and subscription automation | Registration assigns an opt-in default role transactionally. The operator CLI must run offline; a database process lease rejects ordinary overlap with a new server. Registered admission refreshes Authority if a new assignment appeared at the same revision. |
| Raw native channel create/edit/delete | Retired. The acknowledged role-channel API is the current native lifecycle path. |
| Operator CLI and production composition | Startup requires active roles-v1 policy and installs shared Authority before listeners. Migration, registration, activation and key rewrapping take the offline process lease. Copied-database rehearsal and physical-device checks remain. |

The text-path audit found a separate metadata disclosure: mass mentions could
attach hidden/invisible account IDs to a public message. Sender-side mention
resolution now respects visibility, and role-mode queue delivery includes only
the receiving account's own mention ID. Ciphertext and other message metadata
remain intact; the client uses this field only for its own notification state.
The existing legacy mention path remains unchanged.

## Latest enforcement slices

- Existing-channel draft previews use native 151/152 with active Authority and current ManageChannelAccess. Candidate validation reuses ApplyRoleChange; comparisons include prerequisites, owner/Administrator exceptions and member overrides without side effects. Channel Access requires review of the exact draft, with a guest baseline on every bounded member page. Scope is the edited channel only; move/create and explicit descendant-impact views remain. See docs/channel-access-impact.md.

- Server settings use protected Get/SetServerConfigForTab bindings rather than the legacy administrator UI flag. Native tab identity is checked under the activation lock before capturing the connection; current ManageServer/legacy server authorization remains authoritative. Role/member/channel/access-preview/voice-moderation editors and both audit viewers use the same tab-bound capture. Shared chat-filter, ban and complaint dialogs use tab-bound protected queries without legacy UI authorization gates. Member kick/ban/batch/poke, avatar/branding, channel-row joins and member drops capture the initiating tab too; drag data also identifies the exact session and frontend generation. Client/server metadata, cached MOTD/subscriptions, channel icons, all custom emoji operations, recent/quick-switch/notification navigation and leave-channel use scoped bindings; notification history retains the originating server. Channel drag/reorder captures tab/generation and routes role placement through acknowledged settings/move editors; legacy channel create/edit/delete/tree calls also capture the opening tab. The role channel editor now has independent acknowledged icon upload/reuse/preview. Legacy privilege-key list/create/revoke/redeem and the manager's group lookup capture the opening tab; queued credentials retain their destination address. Remaining surfaces include other group actions and a full native chat/file/session binding audit. Write-only channel/moderation/avatar/branding/emoji contracts still need committed acknowledgements where required before cutover.

- Server icon/banner writes use ManageServer; chat-filter reads/writes use ManageChatFilters; complaint list/clear use BanMembers; audit reads use ViewAuditLog. Effects remain inside the authorized policy lease. Audit uses trusted persisted channel scopes and current visibility, ID/time-only protected placeholders, and actual role/channel before/after snapshots. Unclassified historical records require owner/Administrator; JSON text cannot claim trusted provenance.
- Pokes require PokeMembers and visible online targets. Hidden/invisible targets return the same not-found result as missing ones; queued delivery rechecks current sender authority and target visibility. Cooldowns remain independent.
- Channel deletion removes stored icon variants before dropping retry state. Failure retains the old state tree for recovery while file/media revocation still runs.
- Root/database searches confirmed channel-command and request-talker keys have no implemented action path; their legacy grid/mapping/conflict metadata is retained only until legacy retirement. The replacement capability catalog omits them.
