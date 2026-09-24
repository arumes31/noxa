# Explicit complaint deletion scopes — awaiting approval

Automatic approval review rejected implementation because it expands destructive
complaint-deletion API scope. No implementation or protobuf change was applied,
and no complaints were deleted. The initial failing test was removed while this
proposal is pending so the current build remains valid.

## Proposed behavior

Extend the existing complaint-clear request in native control, Query/SSH
`complaintclear`, and gRPC `ClearComplaints` with two optional positive IDs:

- `complaint_id`: delete exactly that complaint.
- `through_id`: delete all complaint rows whose IDs are at or below this
  inclusive boundary, preserving higher-ID reports.

Existing target plus optional reporter clearing remains available. Exactly one
scope is required. Empty, negative and mixed scopes are rejected. A boundary is
an ID fence, not a transaction timestamp or proof of a reviewed snapshot.

Role-mode calls retain current authenticated admission and global BanMembers
authorization under the existing policy lease. Integration operations return
the committed deleted count, including zero for no matching rows, without a
protected list refresh after deletion. Native control retains its existing
refreshed-list reply contract. Existing unsupported legacy integration commands
remain closed.

The store uses a parameterized inclusive ID-range DELETE. A single complaint
uses equal lower/upper IDs; bounded global clearing uses IDs 1 through the
requested upper boundary. A separate bounded audit attempt records the canonical
actor, explicit ID scope and deleted count after a successful deletion, including
when the operation context expires immediately after commit.

## Validation required before delivery

- Table tests: each valid scope, negative IDs, reporter without target, mixed
  selectors, empty requests and safe zero-row repetition.
- Real disposable PostgreSQL: single-ID precision, range fence, preservation of
  higher IDs, errors before commit and canonical audit.
- Current BanMembers, revoked grants, disabled integration accounts, and no
  legacy administrator fallback.
- Native, Query/SSH and gRPC routing/validation, committed acknowledgements,
  cancellation behavior and generated protobuf consistency.

Approval requested is for implementing and testing these APIs, not executing
deletions against a production server or activating the replacement model.
