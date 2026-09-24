# noXa permission replacement: roles and channel access

Status: approved; implementation in progress. The roles-v1 server and desktop require the new model. On 2026-09-22 the user chose a fresh-version launch: old clients and databases need no compatibility or data migration. No production database has been initialized or activated.

## Confirmed direction

The user selected a clean replacement on a new empty database. Do not import old accounts, channels, content or permissions as part of this launch. Client and server require a coordinated breaking update.

Success means a server owner can create a moderator, create a private channel, make a read-only announcement channel, and explain a member's access without learning numeric powers, grant levels, skip, negate, or six precedence tiers.

## Existing implementation findings

- `internal/permissions/tiers.go` evaluates six tiers, despite older five-tier comments. `resolver.go` takes the first defined value; `merge.go` merges group entries. This is not Discord's resolution model.
- `client/frontend/src/perms-ui.js` exposes Server Groups, Clients, Channel, Channel Groups and Server Admins, plus raw key/value/grant/flag editing. It also contains useful audit, ban and cosmetic features that must survive the replacement.
- `internal/server/perms.go` mixes boolean checks, power comparisons and allow-on-unset cases. `users.is_admin` is an independent bypass, not ordinary group membership.
- `internal/server/groups.go`, `internal/store/groups.go` and `client/groups.go` implement group membership and permission writes. Some mutations currently return before authoritative server acknowledgement.
- `internal/permissions/loader.go` caches context-dependent permission data. `internal/server/subscriptions.go` rechecks subscriptions on relay; permissions also determine encrypted key entitlement (`internal/server/e2e.go`), file operations, moderation and voice.
- Alternate administration paths exist in `internal/query`, `internal/grpcserver`, `internal/server/queryadmin.go` and native client bindings. Replacing only the permission dialog would leave inconsistent authorization.
- Existing channels have parent relationships and an inheritance flag. Reuse their hierarchy; introducing new channel types or reorganizing servers is outside this project.

The existing code graph was used as an index, then findings were checked in current source. Its query was truncated, so it is not an exhaustive enforcement inventory.

## Options considered

| Option | Strength | Tradeoff | Decision |
| --- | --- | --- | --- |
| Roles plus private-channel access lists only | Few controls and very easy onboarding | Cannot express read-only channels, channel moderators or member exceptions well | Too restrictive for current features |
| Roles, role order and channel overrides | Familiar, expressive, one understandable model | Needs a clear conflict explanation and correct enforcement | Recommended |
| Roles plus a separate advanced policy/rule engine | Highly flexible | Recreates multiple systems administrators must understand | Defer; no advanced legacy engine |

## Product model

### One role system

- `@everyone` is implicit, cannot be removed, and supplies the baseline for every admitted identity, including guests.
- A member can have multiple roles. Role permissions are switches: **Granted / Not granted**. Off does not cancel a grant from another role.
- Owner is a protected server relationship, not a assignable role. Ownership transfer is an explicit owner-only operation; provide an authenticated offline operator recovery path.
- An Administrator permission grants all configurable capabilities and bypasses channel overrides. It does not bypass ownership protection, role hierarchy, account bans, required server admission checks, storage failures or operational limits.
- Seed editable Member, Moderator and Administrator role templates. Member is assigned automatically to newly registered accounts only if the owner enables it. Other templates start unassigned.
- Drag to order roles, with keyboard Move up/Move down alternatives. Role order controls delegated role management and moderation targets, and primary cosmetic presentation. It does not select the winning channel override.
- A manager needs the relevant capability and a highest role strictly above the target role/member. They cannot edit, assign or move equal/higher roles, elevate themselves, or grant capabilities they do not hold at server scope. Role mutations and reordering use the same checks transactionally.
- Only the owner can create/grant Administrator or transfer ownership. This deliberate restriction makes high-impact delegation simpler to reason about than a numeric grant ceiling.

### Channel access

Every channel exposes **Synced with parent** or **Custom access**. Root channels use server defaults unless customized. A synced child uses the parent's effective override policy, not an extra tier applied on top. Editing a synced child first creates a custom copy. Resync replaces the custom policy after showing the difference. Moving a channel asks whether to keep access or sync with its destination; the safe default is to keep access.

Overrides target roles or individual registered members and use **Inherit / Allow / Deny**. An entry cannot allow and deny the same capability. Member exceptions sit behind an explicit Add member exception action and show a count, so they do not become invisible administrative debt. Guests have no persistent per-member exception.

Channel creation offers Public, Private, Read-only and Listen-only presets. They generate ordinary editable overrides, not another evaluator. Private applies a View channel denial to @everyone and allows selected roles/members; server Administrator remains an exception and is stated in the preview. Preset changes show affected capabilities and member-access changes before saving.

Do not introduce a Muted role: additive roles make it unreliable as a hard restriction. Preserve existing server-enforced mute/deafen and ban states separately. Timeout can be added later as moderation state if desired; it is not required for this replacement.

### Resolve access consistently

1. Validate session/admission and load the target resource's current policy. Unknown capabilities and missing required policy data deny access.
2. Owner and Administrator receive all configurable capabilities, subject to the independent restrictions above.
3. Union @everyone and all assigned role grants.
4. Apply the selected channel policy's @everyone deny/allow entries.
5. Aggregate overrides for all of the member's roles, applying their combined denies then combined allows. Thus an explicit allow from one role wins over a deny from another at this stage, independent of role order.
6. Apply the member-specific channel deny/allow entries last.
7. Apply capability prerequisites, moderation state and action-specific hierarchy/resource checks. For example, hidden channel content cannot be fetched by ID; Connect gates voice admission and Speak gates audio publishing. Reading channel chat does not require joining voice.

The evaluator returns allowed/denied plus an explanation referencing stable role/channel IDs and a policy revision. Display explanations with localized names, for example: “Allowed by Moderator in Support” or “Denied by your channel exception.” Explain why turning a role switch off may leave access granted elsewhere.

Example: @everyone cannot view Staff; Moderator allows View channel; Alice has Moderator and can view Staff. A member-specific View channel denial blocks Alice unless she is owner/Administrator. A Listener role denying Speak does not reliably silence someone whose other channel role explicitly allows Speak; use server mute for an enforced restriction.

### Permissions versus settings

Group switches by Access, Text, Voice/video, Files, Moderation and Server management. Put uncommon administration capabilities in collapsed groups; keep search across the whole catalog.

Candidate labels include View channel, Read history, Send messages, Mention everyone, Manage messages, Connect, Speak, Share camera, Share screen, Whisper, Priority speaker, Upload files, Download files, Manage others' files, Move members, Mute members, Deafen members, Kick members, Ban members, Manage channels, Manage channel access, Manage roles, Manage server, Manage invitations, Manage emoji, Manage chat filters and View audit log. These are an initial UX inventory, not a promise to delete other existing actions. Task 1 must map every current permission/check to a new capability, a setting, an identity attribute, or an explicitly retired feature.

Upload quotas, channel capacity, bitrate/quality ceilings, channel passwords, slowmode and flood protection belong in Server/Channel settings. They are not numeric permission powers. Retain password checks as an optional separate channel condition. Any bypass capability must be explicit. Recording and remote-address visibility stay distinct sensitive capabilities rather than being folded into generic moderation. Bot identity becomes an account/service attribute, not a privilege switch; bots receive roles and operational rate limits like other service identities.

## UI design

| Surface | Main controls | Helpful behavior |
| --- | --- | --- |
| Server settings → Roles | Ordered role list; name/color/icon; Permissions and Members tabs | Search; duplicate role; templates; sticky Save/Discard; member count |
| Server settings → Members | Member search and role chips | Multi-select role assignment; explain rejected targets; authoritative save result |
| Channel settings → Access | Parent-sync state, presets, role/member overrides | Three-state controls, source labels, impacted-member preview, resync diff |
| Check access | Select member and channel; filter denied capabilities | Explain grant source, hierarchy restriction and sync origin using the server evaluator |
| Audit log | Actor, target, before/after, time | Readable role/access changes, without exposing unauthorized channel details |

Use the current visual system, reusable modal lifecycle and English/German localization. Do not render raw protocol keys in the normal flow. Permission preview is read-only computation, not session impersonation; it never returns messages, files or keys and is itself authorized. Existing member colors/icons/hoisting should use roles after cutover.

Usability acceptance targets: a first-time admin completes “make Bob moderator” and “private project channel” in under a minute each using a fixture; explains a conflicting-role result from Check access; saves using keyboard only at the existing supported compact viewport. Measure with a short manual task script; do not claim usability from automated tests alone.

## Architecture

- Implement the pure, deterministic role evaluator and capability catalog in `internal/authorization`. Keep storage loading separate from evaluation and explanation. A separate package avoids a dependency cycle with the legacy loader's store-backed tests while both implementations exist during development; retire the old engine at cutover.
- Store named capability grants, not TS3 integer values. Expose named keys in the protocol; internal sets can be optimized later. Avoid JavaScript numeric bitset size issues.
- Proposed tables: roles, role_permissions, member_roles, channel_role_overrides, channel_member_overrides, authorization_config. Separate override tables give real role/user foreign keys. Enforce unique role positions, one @everyone role, valid capability scope and one effect per subject/channel/capability. Model sync state explicitly and reject parent cycles.
- Maintain an authorization revision. Mutations require expected_revision, are authorized against pre-change state, and write configuration + audit + revision atomically. A stale editor receives a conflict with the current revision, not silent last-write-wins. A successful response acknowledges committed state.
- Use one server authorization service with actor, action and target channel/member context. It combines evaluator output with hierarchy and feature rules. Avoid “current voice channel” as an implicit scope when operating on another channel.
- Query/SSH, gRPC, TCP, file transfer, voice/video signaling and client bindings must use the same policy authority. Accountless infrastructure credentials must have an explicit operator/service authority; do not silently become role members or bypass roles through old helpers.
- Cache decisions by identity, target scope and revision; treat invalidation as an enforcement concern, not just a UI refresh. A permissions-changed event refreshes presentation but cannot grant access.
- Restrictive changes promptly stop unauthorized publishing/forwarding, drop subscriptions and reject subsequent resource operations. Rotate affected channel keys and distribute only to entitled users before sending later ciphertext. Already delivered plaintext or keys cannot be revoked retroactively. Recheck archival-key requests and active file transfer policy as well as new requests.
- Add an authorization-model capability/version to the handshake. Reject incompatible clients/tools with a clear upgrade message. Legacy permission mutations must be disabled at activation, with no live fallback into the old resolver.

## Fresh-version launch

1. Initialize an empty PostgreSQL database. Production entrypoints mark it for
   roles-v1 and reject unmarked databases with existing public tables. There is
   no migration of old accounts, permissions, channels or content.
2. Register the owner account offline and retain its exact unique ID and
   credentials. Inspect the inactive policy and activate it explicitly.
3. Activation seeds a closed baseline, rotates the global and current channel
   scope keys, writes audit and enables the policy transactionally. Interrupted
   preparation remains closed and resumable; repeated activation is refused.
4. Rehearse a fresh install, owner login, role changes and backup restoration
   against a disposable database before serving production traffic.

No production database has been initialized or activated by this document.

## Delivery boundaries

Ordered slices and acceptance checks are in `tasks/todo.md`. The server now
requires an active roles-v1 policy before opening listeners. Complete the
remaining enforcement inventory and fresh-install rehearsal before launch.

Keep the first release focused on roles, hierarchy, channel sync/overrides, preview, audit, live revocation and reset. Defer custom policy languages, role expressions, reaction/self-assigned roles, redesigned channel types, role-based resource quotas and a legacy compatibility editor. Existing time-limited grants and automatic group rules are reset; any later role automation must call the same authorized role API.

## Validation and exit criteria

- Resolver table/property tests: additive grants, override ordering, multiple roles, member exceptions, unknown keys, scope restrictions, owner/Admin, parent sync, hierarchy equality and reordered roles.
- Mutation tests: unauthorized direct calls, stale revisions, cross-target IDs, self-escalation, assigning Administrator, concurrent reorder/delete/assign, last-owner loss and audit atomicity.
- Enforcement fixtures: private-channel ID guessing, history/search/files/key bundles, current and archived encryption generations, query/gRPC visibility, guest/bot behavior and voice already active when access is revoked.
- Fresh-install rehearsal: interrupted/repeated setup, missing owner, incompatible clients, no accidental reopening, and tested backup restoration.
- Browser workflows: create/assign/reorder role, private/read-only/listen-only presets, sync/custom/resync, preview explanations, server conflict handling, English/German, keyboard navigation and responsive layout.
- Run focused Go tests per slice, root `go test ./...`, separate client-module tests, frontend unit/browser/lint/build checks, and relevant race tests. Use a real disposable PostgreSQL database for schema/store and activation tests; skipped DB tests are not a release pass. Exercise native audio behavior separately.
- Final audit confirms no active enforcement path depends on old tiers, grant caps, numeric privilege powers or `users.is_admin`. Pure informational copies of historical audit text may remain.

## References

- Discord permission evaluation and hierarchy: https://docs.discord.com/developers/topics/permissions
- Discord role/channel setup and category syncing: https://support.discord.com/hc/en-us/articles/206029707-Setting-Up-Permissions-FAQ

These references inform the role/override interaction only. Owner-only Administrator delegation, parent sync on noXa's existing channel tree, operational limits and the reset procedure are proposed noXa decisions.
