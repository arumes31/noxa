# Managed TURN behind a TLS proxy

Managed TURN retains its one-hour, renewable session/device credentials and
immediate allocation revocation when a trusted TCP proxy terminates TLS.
The proxy does not issue credentials or replace the Pion authorization manager.
UDP and existing TCP routes remain available. TLS protects the client-to-proxy
hop; it is not an end-to-end media encryption assertion.

## Private backend

Configure a dedicated private container network with fixed addresses. Only the
proxy and noXa should join it. Bind the backend to its address on that network,
not `0.0.0.0`, and never publish its port. The proxy peer allowlist contains exact
IPv4 addresses, not subnets. Example addresses below are placeholders:

```yaml
turn:
  managed: true
  credentials_ttl: 1h
  tls_proxy_listen_addr: "10.200.10.3:12341"
  tls_proxy_trusted_ips: ["10.200.10.2"]
  # Keep the existing realm, public IP, relay pool, quotas and peer policy.
  uris:
    - "turn:relay.example.com:12340?transport=udp"
    - "turns:relay.example.com:448?transport=tcp"
```

Environment equivalents are `NOXA_TURN_TLS_PROXY_LISTEN_ADDR` and
`NOXA_TURN_TLS_PROXY_TRUSTED_IPS`. A `turns:` URI requires the explicit backend
configuration. The backend requires a valid PROXY v1/v2 header from an allowlisted
peer, rejects missing/UNKNOWN/LOCAL headers, and applies existing pre-auth quotas
to the original client IP. Header parsing has a fixed two-second deadline and a
separate 64-connection budget. Public TURN listeners do not interpret headers.

## Nginx Proxy Manager / Nginx stream

Publish TCP 448 on the proxy container. TCP 443 remains available for HTTPS.
Use a certificate trusted by the intended clients and valid for the URI hostname.
An SRV record can discover a hostname; it does not replace that hostname's A/AAAA
records or certificate SAN. The backend connection below is plaintext only on
the dedicated private network.

```nginx
# In the stream context, for example NPM's custom/stream.conf include.
server {
    listen 448 ssl;
    ssl_certificate /path/to/managed/fullchain.pem;
    ssl_certificate_key /path/to/managed/privkey.pem;
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_handshake_timeout 5s;
    proxy_connect_timeout 3s;
    proxy_timeout 2m;
    proxy_pass 10.200.10.3:12341;
    proxy_protocol on;
    access_log off;
}
```

Use the proxy's certificate renewal mechanism and reload Nginx after renewal.
Custom Certbot lineages outside NPM's certificate database need their own renewal
schedule and deploy hook; NPM does not automatically manage them. Check `nginx -t`
before reload. Do not copy certificate private keys into noXa or disable client
certificate verification. Keep the previous certificate/config for rollback.

Advertise the TLS URI only after checking the public certificate chain and a real
TURN allocation. Verify an active allocation across proxy reload, automatic
credential renewal, session/device revocation, and UDP-blocked relay-only calls.
Do not silently weaken certificate checks if a client cannot validate the proxy.

## Verification

`go test ./internal/turn ./internal/config` covers proxy address trust, parser
failures, slow headers, per-client quotas, verified TLS allocations, bidirectional
relay traffic, credential renewal/revocation, expiry, and invalid certificates.
`NOXA_TURN_BROWSER=1 go test ./internal/turn -run TestBrowserRelayContinuity -v`
exercises Chromium audio, camera, synthetic screen video and a data channel over
UDP, TCP and TLS through multiple accelerated credential generations. The browser
fixture trusts only its ephemeral certificate SPKI; Go TLS tests separately enforce
trust chain, hostname and expiry. This short fixture is not a packaged-client or
hour-long production acceptance test.
