# Permission replacement: integration operation inventory

This inventory records the current role-mode boundaries. It does not activate
roles-v1 or claim that cutover is complete. Query and SSH share command dispatch;
gRPC uses a separate explicit RPC allowlist. All supported protected operations
use the native authority and an authenticated, explicitly enabled account.

## Supported equivalents

| Query / SSH | gRPC Control | Native contract |
| --- | --- | --- |
| login | Authenticate; Basic credentials on each subsequent RPC | Canonical integration identity and current eligibility/bans |
| clientlist | ListClients | Filtered live identities, including visible unassigned sessions |
| channellist | ListChannels | Filtered tree, hidden-parent flattening and visible counts |
| channelinfo | GetChannelInfo | Filtered channel metadata and resource settings |
| serverinfo | GetServerInfo | Native public information and visible population counts |
| clientinfo | GetClientInfo | Native target visibility and separate statistics/address gates |
| serverconfig | GetServerConfig | ManageServer-gated runtime settings |
| serverconfigset | SetServerConfig | ManageServer-gated atomic six-setting save, serialized runtime publication and canonical audit |
| medialimits | GetMediaLimits | ManageServer-gated complete media-limit snapshot and process-local revision |
| medialimitsset | SetMediaLimits | Atomic three-setting persistence, coordinated live publication and exact revision acknowledgement |
| chatfilterquery | GetChatFilters | ManageChatFilters-gated lists and config-default provenance |
| chatfilterset | SetChatFilters | Serialized partial edits, explicit clearing, committed cache publication and canonical audit |
| servertextset | SetServerText | ManageServer-gated name/MOTD/announcement/rules writes; encrypted broadcast text, explicit clearing and content-hash acknowledgement |
| rolelist | GetRoleState | Scoped editor projection, roles, overrides and delegation options |
| rolemembers | ListRoleMembers | Revision-aware registered/offline roster and hierarchy |
| accesscheck | CheckAccess | Subject decision, prerequisites and separate actor hierarchy |
| rolechange | ChangeRoles | Revision-checked role/assignment/access transaction and audit |
| channelquery | GetChannelOptions | Visible lifecycle preflight and destination choices |
| channelchange | ChangeChannel | Atomic resource/policy lifecycle and reconciliation |
| membervoice | SetMemberVoice | Current session scope, optional mute/deafen flags |
| membermove | MoveMember | Source/destination checks and native membership lifecycle |
| memberdisconnect | DisconnectMember | Current channel removal while preserving the session |
| memberkick | KickMember | Exclusive revocation of one native session |
| memberban | BanMember | Canonical account ban and matching-session revocation |
| banquery | ListBans | Current BanMembers, bounded pages and unconfirmed-write recovery |
| auditquery | ListAuditLog | Current ViewAuditLog, trusted record scopes, protected placeholders and bounded pages |
| complaintquery | ListComplaints | Current BanMembers, bounded oldest-first pages and native nickname projection |
| complaintclear | ClearComplaints | Target plus optional reporter deletion, canonical audit and deleted-count acknowledgement |
| rulesquery | GetServerRules | ManageServer-gated wording/hash and acceptance count for that exact wording |
| customquery | ListCustomMetadata | ManageServer-gated bounded opaque-subject annotations, current admission and protected delivery |
| customchange | ChangeCustomMetadata | Exact subject/key set or explicit delete; empty-value distinction, committed acknowledgement and value-free canonical audit |

Query `logout`, `help`, `version` and `quit` are local session/control operations.
They do not grant content access. gRPC authenticates each protected RPC instead
of issuing a reusable session token. See [the integration contracts](integration-role-access.md)
for deadlines, bounded delivery, revisions, pagination and pending outcomes.

## Legacy commands and remaining decisions

Every command below is currently rejected in role mode before its legacy backend
handler. Rejection alone does not prove that the corresponding remaining product
requirement is complete.

| Legacy command(s) | Replacement or remaining work |
| --- | --- |
| clientmove, clientkick, banclient | Replaced by membermove, memberdisconnect/memberkick and memberban; ambiguous old scope contracts remain closed. |
| channelcreate, channeldelete, channeledit | Replaced by channelchange with explicit revision/access behavior. |
| permoverview, channelpermlist, channeladdperm, channeldelperm | Numeric tier/permission contracts are retired in role mode; rolelist/accesscheck/rolechange replace them. |
| servergroupadd, servergroupdel, servergroupaddclient, servergroupdelclient, servergrouplist, servergroupclientlist | Old group authority is retired in role mode; role and member contracts replace it. |
| tokenadd, tokenlist, tokendelete | Legacy group-grant tokens are retired by the approved reset design; invalidation/removal still belongs to reset rehearsal. |
| sendtextmessage | Privileged plaintext integration injection stays closed. Native encrypted chat is retained; the already-deprecated gRPC Chat service is unserved in both models. |
| serverset, serveredit | serverconfigset/SetServerConfig replaces the runtime capacity/timeout/Opus save; medialimitsset/SetMediaLimits manages the independent video ceilings; chatfilterquery/chatfilterset and gRPC equivalents manage moderation lists. servertextset/SetServerText handles name/MOTD/announcement/rules. Arbitrary setting keys remain closed. |
| complaintlist, complaintdel, complaintdelall | complaintquery/ListComplaints replaces listing; complaintclear/ClearComplaints uses the native target plus optional reporter scope. Single-ID and global deletion are retired: operators can clear a target's complaints, optionally limited to one reporter, without a server-wide destructive operation. |
| auditlog | Replaced by auditquery/ListAuditLog with native trusted-scope filtering, protected placeholders and bounded pagination. No raw legacy audit fallback. |
| customset, customdel, custominfo | Replaced by customquery/customchange and typed gRPC equivalents under ManageServer. Opaque/orphan subjects are preserved; annotations never confer role authority. Legacy entry points remain closed. |
| logview | Retired as a remote integration operation. Process logs remain available through the server operator's local logging path; scoped auditquery/ListAuditLog is the remote management history. The raw-log proposal in docs/server-diagnostics-proposal.md is not part of this cutover. |
| serverrules | Replaced by rulesquery/GetServerRules with ManageServer, current admission and protected delivery. Published native join rules remain available to ordinary clients. |
| serverstop, serverrestart | Retired as remote integration operations. The deployment supervisor owns stop/restart; roles-v1 grants do not control the server process lifecycle. |

`TestRoleQueryLegacyOperationInventoryHasNoFallback` covers all 33 currently
closed legacy command names. Native role-aware feature availability is not a
claim of integration parity for operations deliberately retired above.

## Other transports

| Surface | Current evidence and remaining work |
| --- | --- |
| gRPC CreateChannel/DeleteChannel/QueryPermissions | Closed in role mode; revision-aware lifecycle and role inspection contracts replace them. |
| gRPC file-transfer RPCs | Intentionally unimplemented; native session-bound file capabilities remain the transfer path. |
| gRPC Chat/Signaling | Already-deprecated, unregistered services; encrypted chat and WebRTC negotiation use native control. |
| gRPC Events.Subscribe | Role mode supports mandatory filtered ROLE_SNAPSHOT plus optional USER_SPEAKING, with fresh admission/policy through each socket write, idle revocation refresh and bounded subscriptions. Raw transition filters remain rejected. |
| HTTP /events WebSocket | Role-aware handler uses canonical integration admission, explicit model negotiation and the same filtered snapshot/activity contract as gRPC via protobuf JSON. Every bounded frame write retains current policy; legacy mode keeps its original handler. |
| Native control | Role-aware paths and mandatory model negotiation are implemented. Full-client and physical-device validation remain cutover checks. |

Do not remove the role-mode rejection boundaries while any replacement lacks
current-admission, current-policy and protected-delivery guarantees.
