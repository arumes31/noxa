# Reviewing channel access drafts

Channel Access requires a successful **Preview member changes** before saving a
changed policy. The preview compares the saved policy with the exact current
draft, including presets, member exceptions and parent resync. Editing the draft
invalidates the preview. Save still uses the committed revision check and the
normal authoritative mutation; a preview is never a grant to bypass those checks.

Each page shows guests and up to 100 registered members. Owner and Administrator
exceptions appear as unchanged decisions where appropriate. **Next members**
replaces the registered-member page while retaining the guest baseline. The UI
states when more members are available. These are permission changes in the
selected channel, not total affected-member counts or a whole-subtree report.
Passwords, bans, moderation states and resource limits remain separate checks.

The native `ChannelAccessPreview` request (151) accepts a `change` with kind
`channel_access_set` and `user_ids` containing 1–101 distinct nonnegative IDs.
Zero is the anonymous guest. `ChannelAccessImpact` (152) returns the base
`revision`, `channel_id` and one ordered member result per requested ID; each
member contains only capabilities whose allowed state changes, with `before` and
`after` booleans. It returns no names, addresses, content, keys or session data.
The UI obtains names from the separately authorized, revision-bound member roster.
Unknown positive subject IDs use the evaluator's unassigned-member baseline;
the preview is not proof that an account exists.

An active Authority is required. The dispatcher holds its read lease and checks
session revocation through the reply. The actor must have current channel access
management permission; the candidate passes the same `ApplyRoleChange`
validation, hierarchy and pre-change grant checks used when saving, including
validation of changes propagated to synced descendants. Preview evaluation
itself is limited to one selected channel. It never persists a revision, writes
audit, reconciles live sessions or impersonates a member.

`PreviewChannelAccessForTab` captures the opening native connection and verifies
response revision/channel. The UI also verifies ordered member identities and
ignores results after a draft change, dialog closure or server reset. Failed or
stale previews cannot enable Save. Busy preview controls retain keyboard focus
and block duplicate requests; English/German compact-layout checks cover this.

Create and move dialogs also offer member previews to access managers. Creating
with a custom preset and moving with destination sync require a successful review
of the exact policy draft. Role selections, lifetime, preset or destination
changes invalidate that review. Inherited creation and keep-access moves remain
available without review, including temporary creators who cannot inspect access.
Non-policy settings and passwords are excluded from preview requests.

For these operations, request `tree` instead of `change`, using
`ChannelTreeChange` with kind `channel_create` or `channel_move`. The candidate
passes `ApplyChannelTreeChange` checks and requires current ManageChannelAccess
at the creation parent/root or moving source. Creation uses channel ID zero in
the request and response; an unused private in-memory ID is used for evaluation.
No channel ID is allocated in storage. Because the channel does not exist yet,
all before states are denied and the result lists proposed grants. Member rosters
use the creation parent/root scope. A move compares the source channel before
and after the keep/sync choice. The capability catalog accompanies channel
state so every result has localized labels.

Existing-channel and move previews accept optional `scope_channel_id` to inspect
a descendant while applying the same full draft to its source. Zero/omission
retains the original channel behavior. The selected channel must currently be
managed by the actor and have an entirely synced path to the source; custom
branches, their children and unrelated channels are rejected. Creation cannot
specify a descendant. `RoleState` and move `RoleChannelState` expose sorted
`impact_channel_ids` containing only eligible descendant IDs, without their
policies or resource names. The frontend uses the filtered current channel list
for display names, with a localized ID fallback.

The optional channel picker fetches the selected scope's authorized roster and
checks the response channel ID. Switching selection invalidates results and
review, as does a panel recreation that returns to the source. A successful
preview covers only the selected scope/page: it does not certify that every
descendant or member has been reviewed. The actual save always targets the
original source and revalidates the entire change.
This native endpoint does not add Query/SSH/gRPC preview adapters or activate the
role model in production.

The shared `testdata/channel-access-presets.json` contract is checked against
frontend preset generation and all four creation-dialog payloads. The Go privacy
matrix covers 56 combinations: four presets, granted/empty server baselines and
seven transitions (set, customize, resync, keep-move, sync-move, custom creation
and inherited creation). It checks explicit decisions and exact preview changes
for guests, unassigned members, selected-role access, overriding member denial,
owner and Administrator. Synced descendants follow the source, custom branches
remain independent, and a customized channel plus its synced child remain
independent after a later parent restriction. These are evaluator/UI contract
checks; native media and final cutover rehearsals remain separate requirements.
