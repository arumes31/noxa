# Passkey device approval

New passkeys saved by an already confirmed PWA device include a signed delegation of future device approval to that credential. The delegation preserves the signing chain to the account identity, including after normal sign-out of the registering phone.

A passkey login challenge commits both new device keys. The server verifies WebAuthn normally and stores the assertion and delegation with the linked device. An online trusted desktop independently checks the lineage against its own identity, the RP/origin, user presence and verification, and the assertion signature and key binding. It then issues the existing Ed25519 device endorsement. Contacts retain their existing pinned-key and device-chain checks.

The automatic check runs on device-linked events and reconnect. It needs a trusted desktop online; it does not automatically copy historical messages. Legacy passkeys or browsers without credential public-key export continue to sign in, with the existing manual DM-device confirmation fallback. Save a new passkey from a confirmed PWA device to enable delegation.

Migration 059 adds bounded nullable proof columns. No existing credentials or sessions are changed. A server response alone never establishes E2EE trust; a forged proof, other account, different device key, or missing user verification is rejected.
