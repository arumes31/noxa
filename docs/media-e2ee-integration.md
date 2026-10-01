# Media encryption integration boundary

Status: experimental adapter, not integrated into channel media. No channel E2EE claim is currently valid.

The encoded-frame adapter uses the pinned LiveKit 2.22.3 worker for codec-aware AES-GCM encryption. The wrapper disallows plaintext/SIF bypass and rejects authenticated frame replays. Chromium tests cover actual VP8 transport, missing keys, and tampered encrypted data. Key bytes are copied before asynchronous work; installed keys and transform identities are immutable. A new epoch requires fresh transform endpoints. Identity, key, transform, request, replay and session lifetime limits fail closed.

## Required protocol work

1. Commit encryption membership epochs atomically with authorized channel membership changes. Gate packet delivery, queued egress and retransmissions on the committed epoch; client notification alone cannot close the membership-change race.
2. Distribute each publication's keys in authenticated envelopes scoped to connection, slot and membership epoch. Use a distinct worker identity per scope. Recheck current membership on every accepted envelope; a wire key-index byte is not an epoch.
3. Rotate keys and replace affected transform endpoints on membership changes and before the adapter's ten-minute expiry. Bound old handlers and reject delayed envelopes from previous epochs.
4. Replace plaintext JPEG previews with opaque encrypted payloads. Authenticate epoch, publication generation, type and counter inside the encrypted content; the worker's data encryption alone provides neither replay protection nor application-context binding.
5. Give whisper routing its own recipient/key scope. Sharing the ordinary channel microphone key would let excluded channel participants decrypt misrouted whispers.
6. Define recording policy before integration. Existing server FFmpeg taps consume plaintext. The outstanding user choice is a visible recording participant with keys, recording on the initiating client, or disabling recording for encrypted media. Do not silently give the SFU decryption keys.
7. Verify peer identity keys and expose meaningful verification state. Static identity-key envelope encryption does not provide forward secrecy; do not claim it does.
8. Test codec compatibility, join/leave/rejoin, connection replacement, failed key delivery, concurrent membership updates, retransmissions, replay, recording, and the server's inability to decode without explicit recording participation.

## Source references

- [LiveKit encryption overview](https://docs.livekit.io/transport/encryption/)
- [LiveKit encryption setup](https://docs.livekit.io/transport/encryption/start/)
- Pinned dependency source: `client/frontend/node_modules/livekit-client/src/e2ee/` (worker queue, frame identity and key-handler lifetime verified against installed code).
- [WebRTC standard](https://www.w3.org/TR/webrtc/) for transport integration and authenticated trickle ICE ordering in private calls.

An adapter-only browser test does not prove Vite includes the worker in the production application. Verify the production worker asset and native runtime when the channel integration imports it.
