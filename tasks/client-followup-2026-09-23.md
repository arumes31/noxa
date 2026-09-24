# Client follow-up fixes

Scope: the five follow-up findings requested after the first client audit fixes.

- [x] Merge settings edits against their original snapshot in the backend transaction. Preserve unrelated changes; reject conflicting edits to the same field without partial persistence. Keep baseline metadata out of disk settings.
- [x] Commit contact and note edits only after persistence succeeds; show errors and allow retry. Cover context-menu and Contacts-dialog entry points.
- [x] Fail browser tests on unexpected uncaught exceptions/rejections and repair incomplete module fixtures.
- [x] Replace retired administration examples with current role-aware contracts.
- [x] Verify final media output deadlines/revision checks already present; retire failed output and invalidate buffered/retransmission work without holding authorization locks during teardown.

Validation: deterministic Go/frontend regressions, browser workflows with the new error guard, race tests for settings and media, lint/vet, production client build, and focused native UI checks. Preserve the existing working-tree changes and audit evidence. No deployment or production activation.

Results and limitations: [follow-up verification](../docs/client-followup-fixes-2026-09-24.md).
