# Role capability enforcement audit

Source audit: 2026-09-22. This records implemented role-mode paths, not production
activation or complete transport/physical-media rehearsal. The shared
`internal/server/role_access.go` lease pins policy through protected effects and
delivery; hierarchy, admission, channel membership and operational limits remain
additional checks. Existing tests named below are evidence for their particular
cases, not proof of every possible interleaving.

Paths in the table are relative to `internal/` unless noted. Related transport
coverage is recorded in `docs/integration-operation-inventory.md` and
`docs/native-api-inventory.md`.

| Capability | Current enforcement | Regression evidence / limits |
| --- | --- | --- |
| Administrator | `authorization/roles.go` bypasses configurable grants only after scope validation; protected ownership and hierarchy remain separate. | `TestRoleResolution`, `TestRoleHierarchyAndSnapshotIsolation`, `TestRoleChangesUsePreChangeAuthority`, `TestOwnershipTransfer`. |
| ViewChannel | `server/role_snapshot.go`, `subscriptions.go`, `e2e.go`, `role_delivery.go` gate discovery, subscription, current keys and queued channel data. `role_chat_reconcile.go` revokes subscriptions and rotates affected keys. | `TestRoleSnapshotHidesResourcesCountsAndParentReferences`, `TestRoleQueuedDeliveryUsesCurrentPolicy`, `TestRoleChatRevocationRotatesBeforeAcknowledgement`. |
| ReadHistory | `server/chatx.go` gates history and pins; `e2e.go` gates archived key generations. | `TestRoleChatReadsRejectLegacyAdminAndUnknownScopes`. |
| SendMessages | `server/handlers.go` gates sends; `chatx.go` checks edits/reactions/typing. Stored message scope is authoritative for mutations; own-delete retains access requirements. | `TestRoleChatMessageMutationsUseStoredScope`; confirmed native chat tests cover acknowledgements/session ownership separately. |
| MentionEveryone | `server/chatx.go:parseMentions` gates all three mass-mention spellings in the actual scope and filters subjects by sender visibility. Role-mode delivery exposes only the recipient's own notification ID, never the complete resolved recipient list. | `TestRoleMentionsRespectSenderVisibilityAndGrant`, `TestRoleQueuedChatMentionsDiscloseOnlyRecipient`, `TestRoleMentionsOverTCPAndLiveRevocation` cover global/channel overrides, hidden/invisible subjects, owner/statistics exceptions, ciphertext preservation and same-session revocation. |
| ManageMessages | `server/chatx.go` checks other-author deletion and pin changes against stored scope. | `TestRoleChatMessageMutationsUseStoredScope`. |
| BypassSlowmode | `server/chatx.go` checks current scope before bypassing channel slowmode. Infrastructure limits remain independent. | Dedicated role grant/revocation slowmode matrix remains desirable; ordinary rate-limit tests are not substitute evidence. |
| Connect | `server/handlers.go` gates destination joins; `role_media.go`/`voice.go` gate signaling and media recipients. | `TestRoleJoinEnforcesPasswordAndOwnerCapacityLimit`, `TestRoleMediaSetupRequiresCurrentVoiceAccess`. |
| Speak | `server/voice.go` gates microphone publication; `role_media.go` checks microphone/screen-audio writes and moderation mute. | `TestRoleMediaDeliveryChecksSlotScopesAndLiveRevocation`, WebRTC continuous-speech revocation tests. |
| ShareCamera / ShareScreen | `server/voice.go` admits video setup; exact camera/screen slots are checked on writes in `role_media.go`. `role_media_controls.go` checks screen-state changes. | `TestRoleMediaDeliveryChecksSlotScopesAndLiveRevocation`; destination changes clear denied sharing state. Physical capture rehearsal remains separate. |
| Whisper | `server/role_media_controls.go` checks source and destinations; `role_media.go` and `role_publishers.go` recheck packet recipients and source visibility. | `TestRoleMediaControlsCheckDestinationsAndAllowRevokedCleanup`, `TestRoleChannelWhisperHidesSourceMetadataFromIneligibleRecipient`. |
| PrioritySpeaker | `server/role_media_controls.go` gates activation; policy reconciliation and movement clear denied flags. Snapshot/event projections independently reject stale active flags. | `TestRoleMovementRechecksActiveMediaFlags`, `TestRoleSnapshotRejectsStaleMediaFlags`, `TestRoleQueuedMediaFlagsRequireCurrentAuthority`. |
| UploadFiles / DownloadFiles | `server/role_files.go` issues session/scoped tokens; `filetransfer` rechecks activation, chunks, final commit and link writes. | Transfer authorization, consumed-token revocation/regrant, chunk/final-commit and link revocation tests. |
| ManageFiles | `server/files.go` checks ownership or current management grant; backend mutation lock rechecks ownership. Cross-channel moves additionally require destination upload rights. | `TestRoleFilesUseExplicitScopeAndProtectOtherUploaders`, `TestUploadCannotTakeAnotherMembersFile`, `TestFileMutationOwnershipUsesCurrentScopedRights`. |
| MoveMembers | `server/role_member_move.go` checks both scopes, hierarchy, visible target, target destination Connect and admission. | `TestRoleMoveRequiresVisibleTargetAndBothScopes`; movement media regression includes moderator moves. |
| MuteMembers / DeafenMembers | `server/role_voice_moderation.go` independently checks each requested flag, hierarchy and current target scope; media writes honor the resulting state. | `TestRoleVoiceModerationEnforcesHierarchyAndAudioDirections`, `TestRoleVoiceModerationChecksEveryFlagBeforeChangingEither`. |
| DisconnectMembers | `server/role_member_disconnect.go` checks source/hierarchy/expected channel and uses normal leave lifecycle while retaining the connection. | `TestRoleChannelDisconnectRetainsLeaveLifecycle`, `TestAcknowledgedDisconnectRechecksAfterTargetLock`. |
| KickMembers / BanMembers | `server/role_session_removal.go` and `role_ban.go` check global authority, hierarchy and visibility under exclusive policy; revoke sessions/resources. Bans also persist and block final admission. | Session revocation/queued writer tests; canonical multi-session ban and acknowledgement tests require PostgreSQL integration tags. |
| ManageChannels / CreateTemporaryChannels | `authorization/channel_changes.go`, `server/role_channel_api.go`, `store/role_channels.go` validate parent/target/subtree and atomically write lifecycle/policy. Temporary creators have a separate limited path. | `TestRoleChannelQueryFiltersVisibilitySubtreeAndDestinations`, `TestRoleChannelTemporaryCreatorAndAcknowledgements`, channel store integration tests. |
| ManageChannelAccess | `authorization/role_changes.go` and channel changes validate current authority, grant ceiling, subject hierarchy and explicit sync/custom policy. | Shared privacy matrix, impact preview tests and `TestRoleManagementProjectionProtectsParentPolicy`. |
| BypassChannelPassword | `server/handlers.go:joinChannelAllowed` checks destination grant, separately from Connect and capacity. | `TestRoleJoinEnforcesPasswordAndOwnerCapacityLimit`. |
| ManageRoles | `server/roles.go`, `authorization/role_changes.go`, `store/role_members.go` check pre-change authority, hierarchy, revision and roster visibility. | Role API commit/conflict/forged-actor tests; `TestIntegrationRoleManagementUsesTransactionalAuthority`. |
| ManageServer | Native settings/branding and role integration settings/text/rules/custom metadata check this grant through effect or protected delivery. | `TestDelegatedServerConfigurationUsesCurrentGrant`, `TestRoleServerManagementCapabilities`, individual integration adapter tests. |
| ManageEmoji | `server/chatx.go` gates upload/delete/rename before asset mutation. Resulting public emoji events do not disclose management data. | `TestEmojiDenialPreventsMutation`, `TestRoleEmojiManagementUsesCapability`; no dedicated upload-after-revocation case identified. |
| ManageChatFilters | `server/chatx.go` and `role_integration_filters.go` protect both configuration reads and writes. | `TestRoleServerManagementCapabilities`, `TestIntegrationFiltersUseSeparateCapabilityAndCurrentAdmission`. |
| ViewAuditLog | `server/role_audit.go` and `role_integration_audit.go` gate queries and redact every protected field by trusted stored scopes; unclassified history requires Administrator. | `TestRoleAuditRedactsEveryProtectedField`, `TestIntegrationAuditPreservesScopesAndDeliveryLease`. |
| ViewConnectionInfo | `server/role_metadata.go` protects statistics; snapshot/member/activity/poke paths also use this grant to reveal invisible members. Localized catalog labels disclose both effects. | Native and integration metadata tests prove statistics and invisible-member access. Hidden channels still require ViewChannel. |
| ViewRemoteAddresses | `server/role_metadata.go` separately gates IP/port after target visibility. Native own-session metadata has a self exception; integrations do not. | `TestRoleClientMetadataFiltersHiddenMembersAndSensitiveFields`, `TestIntegrationMetadataFiltersCurrentScopeAndSensitiveFields`. |
| RecordChannel | `server/voice.go` gates recording control; `role_media.go` checks the initiating account on recorder taps; reconciliation stops revoked recordings. | `TestRoleMediaReconciliationDisconnectsAndStopsRevokedRecording`, recorder media-delivery tests. |
| UploadAvatar | `server/extras.go` checks grant and authenticated identity before storage; reads/events require current member visibility. | `TestRoleAvatarsFollowMemberVisibilityAndUploadCapability`; no dedicated upload-after-revocation case identified. |
| PokeMembers | `server/role_presence.go` checks sender/visible recipient; `role_delivery.go` rechecks queued authority and visibility. Cooldown remains independent. | `TestRolePokesCheckCapabilityVisibilityAndQueuedRevocation`. |

## Explicit omissions and remaining work

- ManageInvitations / UseInvitations had catalog entries but no role-native
  handlers. They are removed from validation and the editor. All legacy token
  operations remain explicitly retired in role mode. Role-grant invitations are
  deferred automation, not an implemented replacement for old privilege keys.
  `TestUnimplementedInvitationsAreNotGrantable` rejects these keys. Development
  staging that used them now fails policy validation; this is not a live-data
  migration or silent grant removal.
- DefaultMemberRoleID has policy validation, owner-only mutation, audit,
  persistence and transactional registration assignment. Operator registration
  requires the exclusive offline process lease; admission refreshes the loaded
  policy. An owner-facing setting and a fresh PostgreSQL rehearsal remain open.
- The named-capability audit does not close the exhaustive legacy-key/admin
  bypass inventory, retired native bridge cleanup, end-to-end transport/chaos
  rehearsal, runtime media configuration propagation or physical capture tests.
- Read-only setup preflight and transactional activation/key rotation exist.
  This release requires a fresh database; old database migration is excluded.
  Fresh setup, owner recovery and matching-version backup restoration require a
  disposable PostgreSQL rehearsal. Production activation remains separate.
