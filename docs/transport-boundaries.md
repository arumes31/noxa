# Transport boundaries and HTTPS downloads

This source inventory accompanies the transport hardening package. Deployment-specific addresses, topology, certificates and probe evidence belong in private operator records, not this document. It does not claim that private-media or private-file E2EE is implemented.

| Boundary | Data and authentication | Protection/default | Further acceptance work |
| --- | --- | --- | --- |
| Control TCP 12333 | Credentials, signalling, chat envelopes | TLS 1.3; client pins original destination across SRV lookup | Preserve existing certificate/pin |
| File TCP 12336 | Session-authorized transfers | TLS bound to control certificate; required in production | Certificate identity and rotation |
| Health HTTP 12337 | Probes, metrics/schema; separate capability downloads and integration routes | Native and Compose host publication default to loopback; remote download requests need TLS or explicitly trusted HTTPS proxy | Public HTTPS and sibling-container isolation |
| Raw Query 12335 | Administrative credentials and commands | Loopback default; remote bind requires explicit acknowledgement | Prefer SSH/VPN for remote use |
| Query SSH 12339 | Administrative credentials and commands | Optional SSH, persistent host key | Host identity verification |
| gRPC 12338 | Administrative Basic credentials | Configuration restricts plaintext listener to loopback | Do not publish plaintext remotely |
| UDP 12334 | Ping/pong diagnostic, not account-authenticated | Tiny bounded responses, source rate limit, bounded workers | Optional external monitors; example publication disabled |
| WebRTC | Voice/camera/screen/shared audio | DTLS-SRTP hop encryption | Channel SFU has access unless an endpoint cryptor is actually installed; private-media rollout is separate |
| Managed TURN | Session credentials and relay packets | 60-minute credentials, renewal, session/device revocation, quotas | TLS listener, certificates, long-call/blocked-UDP acceptance |
| PostgreSQL | Accounts, policies, history/storage | No example published port; production TLS validation | Record actual trust/SSL mode in private deployment evidence |
| Redis | Optional cache/pubsub | Remote production authentication/TLS policy; no example published port | Validate if enabled |
| Integrations | Events/webhook secrets | HTTPS guard plus existing authentication/rate limits | Exhaustive outbound integration audit remains separate |
| Updater | Release manifests and executable artifacts | Existing updater/release verification retained | Archived-client/signing transition validation remains separate |

UDP12334 is a legacy ping/pong probe, not voice transport. The current desktop uses control-channel ping; external monitors may still use the optional UDP probe. Its advertised token is not an authentication boundary. Do not describe it as encrypted media.

## Fixed HTTPS origin and proxy trust

Set `download_public_origin: "https://downloads.example.com"` only after that HTTPS endpoint works. Only an HTTPS authority is accepted: no credentials, path, query or fragment. Host and forwarded-host headers never select download destinations.

For a plaintext private backend, configure `health_trusted_proxies` with the immediate proxy's canonical CIDR, preferably /32 or /128. The proxy must overwrite `X-Forwarded-Proto` and refuse/redirect public HTTP before forwarding content or credentials. Broad /0, malformed and mapped IPv6 ranges are refused. Forwarded client IPs confer no trust, and duplicate/list-valued proto headers are rejected. A loopback proxy forwarding an HTTP assertion cannot bypass the guard as a local caller.

The legacy `health_allow_plaintext_credentials` override is rejected in production; explicitly configured proxy trust replaces that escape hatch.

Keep backends inaccessible publicly. Docker host-loopback publication does not isolate a listener from sibling containers: use a dedicated proxy network and narrow allowlist. For Nginx Proxy Manager, configure an HTTPS proxy host, restrict the backend and disable capability-path access logging. TURN/TLS requires its own TCP stream/listener; a normal HTTP proxy is insufficient.

Native health TLS is an alternative, but browsers must trust its certificate. A desktop TOFU pin does not automatically establish browser trust. The existing control SRV record may identify the hostname, but download origins and TURN TLS ports must be configured explicitly for their protocols.

Download capabilities retain 15-minute expiry, session/access revocation, authorization during delivery, deletion checks, no-store and no-referrer headers. Public links remain for server-readable Files-tab content; encrypted chat attachments are refused. Client code rejects HTTP, missing schemes and malformed origins, offering secure in-client download or HTTPS server upgrade/configuration. Old clients ignoring the new origin may not support proxy links; native downloads remain available.

## Validation and rollout

Tests cover unsafe origins, immediate proxy trust, forged/duplicate headers, legacy response rejection, configured origins, clipboard tab isolation and existing expiry/revocation behavior. Deployment acceptance additionally needs external HTTPS identity, restricted plaintext access, correct proxy routing and certificate-renewal evidence. Local success alone does not establish these.

Refresh source before each PR; deploy only the exact green main image with backups. Advertise HTTPS after certificate/proxy checks. Rollback may disable links or restore a known-good HTTPS origin/certificate; it must not silently reopen plaintext capability delivery. Strong identity, private media/files, ratchet/group evolution and old-client upgrade assurance are separate remaining milestones.
