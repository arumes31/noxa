# noxa Protocol Buffers

This directory defines the gRPC/Protobuf schema for the noxa voice/video
server. All files use `syntax = "proto3";` and retain the published package
`voicx.v1` so existing clients can continue calling the same RPC names.

## Files

| File | Service(s) | Purpose |
|------|------------|---------|
| [`signaling.proto`](signaling.proto) | `Signaling` (deprecated) | Compatibility descriptors for the intentionally unserved WebRTC signaling RPCs. |
| [`chat.proto`](chat.proto) | `Chat` (deprecated) | Compatibility descriptors for the intentionally unserved chat RPCs. |
| [`events.proto`](events.proto) | `Events` | Server events broadcast to clients (user joined/left, speaking, channel created/deleted, user moved/kicked/banned). |
| [`control.proto`](control.proto) | `Control` | Authentication, role-aware channel/member management and inspection; compatibility descriptors for retired operations. |

Authentication retains the published field numbers, including `user_id = 3` in
the response. Deprecated fields are never reused for new values; credentials
and the `roles-v1` model header are still required on each RPC. The deprecated
`CreateChannel`, `DeleteChannel`, and `QueryPermissions` declarations remain for
source/wire compatibility and return `UNIMPLEMENTED`. Use `ChangeChannel` and
the role inspection APIs instead; retaining descriptors does not restore the
retired permission model.

## Linting

A top-level [`buf.yaml`](../buf.yaml) configures the [buf](https://buf.build)
CLI for linting and breaking-change detection. To lint the schema:

```sh
buf lint
```

## Generating Go code

The Go stubs are generated and committed under [`v1/`](../v1) as package
`noxav1`, imported from `noxa/v1`. The published `go_package` descriptor remains
`voicx/v1;voicxv1` for compatibility. The pinned generators use explicit
[`M` import mappings](https://protobuf.dev/reference/go/go-generated/#packages)
in `buf.gen.yaml` to place the generated code in the renamed Go module without
changing those descriptors. Include a mapping for both plugins when adding a
new schema file. Regenerate after every schema change and commit the result.

Prerequisite (install once):

```sh
go install github.com/bufbuild/buf/cmd/buf@v1.72.0
```

Regenerate from the project root ([`buf.gen.yaml`](../buf.gen.yaml) configures
the pinned remote plugins and output layout):

```sh
buf generate
```

buf compiles the schema itself and downloads the pinned remote Go plugins, so
neither `protoc` nor local `protoc-gen-*` binaries are needed.

## Implementation status

| Service | Status |
|---------|--------|
| `Events` | Served: `Subscribe` streams from the server-side event bus. |
| `Control` | Served: auth, channel create/delete/list, permission query. Authentication returns only `user_id`; it does not mint a session token. The file-transfer RPCs intentionally return `Unimplemented` — transfer tokens are minted by the control channel after a per-client permission check. |
| `Chat` | Deprecated and intentionally unserved: chat is end-to-end/scope-key encrypted on the control channel. |
| `Signaling` | Deprecated and intentionally unserved: WebRTC signaling stays on the control channel. |
