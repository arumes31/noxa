// Public operator guides. Keep commands aligned with the checked-out Compose
// services, provisioning tools and production validation, not old screenshots.
const repository = 'https://github.com/arumes31/noxa';

export default [
  {
    slug: 'server-setup',
    title: 'Set up your own noXa server',
    summary: 'Build the Docker stack, create the owner, open your first channels and check that people can connect.',
    category: 'Server administration',
    sections: [
      {
        id: 'before-you-start',
        title: '1. Prepare a fresh installation',
        body: `<p>You need a host with Docker Engine, the Docker Compose plugin, Git, and an empty PostgreSQL database. The repository stack includes PostgreSQL 16 and Redis 7. Use matching current server and client versions.</p>
<p>The commands below use Bash and the repository’s default database name and owner, <code>noxa</code>. They initialize a fresh installation. Do not run initial setup against an existing server to reset its owner or password.</p>
<aside class="guide-note"><strong>Start on a restricted host.</strong> The sample environment selects development mode and sample database credentials. The Compose file publishes control and file-transfer ports on all host interfaces. Limit access with host bindings or firewall rules while setting up; complete the production steps before inviting remote users.</aside>
<pre><code>git clone https://github.com/arumes31/noxa.git
cd noxa
cp .env.example .env
printf '\\nNOXA_WEB_DOMAIN=localhost\\n' &gt;&gt; .env
docker compose build noxa
docker compose up -d postgres redis
docker compose stop noxa</code></pre>
<p>The current Compose file requires <code>NOXA_WEB_DOMAIN</code> even before the optional web profile is started. The local placeholder above keeps initial setup valid; replace that entry with your real app hostname when configuring HTTPS.</p>
<p>Wait for the database to become healthy with <code>docker compose ps</code>. The server must remain stopped during offline account creation and role activation.</p>
<p>Using an external PostgreSQL service? Add <code>-f docker-compose.yml -f docker-compose.external-db.yml</code> to every Compose command, configure the external database URLs, and start only Redis instead of the bundled PostgreSQL service.</p>`,
      },
      {
        id: 'initialize-owner',
        title: '2. Initialize the database and create the owner',
        body: `<p>The current Compose stack separates the serving database account from the offline operator account. Run schema and account tools through <code>noxa-operator</code>, which has the migration owner credential and shares the server’s data volume.</p>
<pre><code>docker compose --profile tools run --rm --no-deps noxa-operator /out/migrate
read -r -s -p 'Choose an owner account password: ' OWNER_PASSWORD; printf '\\n'
printf '%s\\n' "$OWNER_PASSWORD" | docker compose --profile tools run -T --rm --no-deps noxa-operator /out/adduser -nickname owner -password-stdin
unset OWNER_PASSWORD</code></pre>
<p>Keep the owner password securely. Copy the exact <code>unique_id</code> printed by <code>adduser</code>; the next command needs that ID, not the account nickname. Replace the placeholder below before running it.</p>
<pre><code>docker compose --profile tools run --rm --no-deps noxa-operator /out/role-setup -owner-uid '&lt;OWNER_UNIQUE_ID&gt;' -activate -confirm ACTIVATE-ROLES-V1 -chat-master-key-file /data/keys/chat_master.key -query-timeout 30s</code></pre>
<p>The setup report must say <code>active: true</code>. Activation is a one-time operation and refuses to run alongside a serving server. The selected account becomes <strong>Server owner</strong>; joining first does not grant administration and there is no privilege token.</p>
<aside class="guide-note">The current <code>adduser</code> tool requires <code>-password-stdin</code>. The hidden prompt and standard-input pipe keep the password out of command-line arguments and shell history. Run offline account tools on a trusted operator host. Repeating <code>adduser</code> for an existing account does not change its password or flags.</aside>`,
      },
      {
        id: 'database-service-roles',
        title: '3. Provision the serving and backup database roles',
        body: `<p>The <code>noxa</code> service connects as <code>noxa_runtime</code>, not as the PostgreSQL owner. Provision its permissions after offline schema setup. The tracked SQL script also creates a separate read-only backup role. Choose two different passwords of at least 24 characters.</p>
<pre><code>read -r -s -p 'Runtime database password: ' NOXA_RUNTIME_PASSWORD; printf '\\n'
read -r -s -p 'Backup database password: ' NOXA_BACKUP_PASSWORD; printf '\\n'
export NOXA_RUNTIME_PASSWORD NOXA_BACKUP_PASSWORD
docker compose exec -T -e NOXA_RUNTIME_PASSWORD -e NOXA_BACKUP_PASSWORD postgres psql -U noxa -d noxa &lt; scripts/provision-database-roles.sql
export NOXA_RUNTIME_DB_PASSWORD="$NOXA_RUNTIME_PASSWORD"
export BACKUP_POSTGRES_PASSWORD="$NOXA_BACKUP_PASSWORD"
unset NOXA_RUNTIME_PASSWORD NOXA_BACKUP_PASSWORD</code></pre>
<p>These exports configure this shell only. Before future restarts, persist the runtime and backup credentials using the corresponding <code>NOXA_RUNTIME_DB_PASSWORD_FILE</code> and <code>BACKUP_POSTGRES_PASSWORD_FILE</code> settings, or your deployment’s secret mechanism. Configure exactly one nonempty value or file source for each secret. Never reuse the migration owner password for a serving account.</p>
<p>If you changed <code>POSTGRES_USER</code> or <code>POSTGRES_DB</code>, update the <code>psql</code> arguments. For an external database, run the same tracked SQL script as its offline database owner with an authenticated, TLS-verified PostgreSQL client.</p>`,
      },
      {
        id: 'first-start',
        title: '4. Start the server and sign in',
        body: `<pre><code>docker compose up -d noxa
docker compose logs --tail=50 noxa
docker compose exec noxa wget -qO- http://127.0.0.1:12337/readyz</code></pre>
<p>Readiness should return <code>ok</code>. Open the Windows desktop client and connect to <code>localhost:12333</code>, or the host’s reachable address. Enter <code>owner</code> in <strong>Account login / guest name</strong> and use the account password you chose. An optional server join password is a separate field.</p>
<p>Compare the certificate fingerprint with the server’s startup log before trusting the first connection. An optional display name changes the name people see without changing the owner login, identity or roles.</p>
<p>If the service cannot connect to PostgreSQL, first check that the runtime role was provisioned and that its password is available in this shell or configured secret file. Do not work around the problem by giving the serving container the database owner credential.</p>`,
      },
      {
        id: 'roles-and-channels',
        title: '5. Give members access to channels',
        body: `<p>A fresh installation starts closed: <code>@everyone</code> has no grants, Member, Moderator and Administrator are unassigned, and there is no default member role.</p>
<ol><li>Open <strong>Tools → Roles</strong>. Select a role, choose <strong>Start from a template</strong> when useful, review its permissions and save.</li><li>In <strong>Members</strong>, assign Member to a registered account. As owner, set <strong>Role for new members</strong> if future registrations should receive it. This does not change existing accounts or guests.</li><li>Right-click a channel and choose <strong>Channel access</strong>. Existing channels deny <strong>View channel</strong> to <code>@everyone</code>, so assigning Member alone does not make them visible.</li><li>For a members-only channel, retain the everyone deny and allow the Member role to view it, with the desired chat and voice permissions and their prerequisites. For a public channel, review the <strong>Public</strong> preset before saving.</li><li>Use <strong>Preview role changes</strong> before saving and <strong>Check access</strong> afterward. Test with an ordinary member and a guest; owner/admin access bypasses configurable channel overrides.</li></ol>
<figure class="guide-figure"><a href="../../assets/screenshots/app-roles-guide.png"><img src="../../assets/screenshots/app-roles-guide.png" width="960" height="1012" loading="lazy" decoding="async" alt="The actual noXa Roles dialog showing a Member role and its permission controls with fictional demo data."></a><figcaption>Actual desktop Roles dialog · Fictional demonstration policy, not the default installation policy.</figcaption></figure>
<p>Only the owner can grant Administrator or transfer ownership. Keep administration limited to trusted operators and use dedicated moderation roles for routine work. See the <a href="../roles-permissions/">roles and channel access guide</a> for inheritance and hierarchy.</p>`,
      },
      {
        id: 'networking',
        title: '6. Configure reachable voice and file connections',
        body: `<p>A successful control login does not prove that voice works. WebRTC media uses its own UDP sockets. The legacy UDP keepalive port <code>12334</code> is not the voice transport.</p>
<table><thead><tr><th>Port</th><th>Purpose</th><th>Exposure</th></tr></thead><tbody><tr><td>TCP 12333</td><td>Desktop TLS control</td><td>Reachable by your members</td></tr><tr><td>TCP 12336</td><td>TLS file transfer</td><td>Reachable for client uploads and downloads</td></tr><tr><td>Configured UDP media port</td><td>WebRTC voice, camera and screen sharing</td><td>Forward the same published port</td></tr><tr><td>UDP 12334</td><td>Optional legacy keepalive</td><td>Only if your deployment uses it</td></tr><tr><td>TCP 12335 / 12338</td><td>Raw Query / gRPC administration</td><td>Keep private; gRPC must remain loopback-only</td></tr><tr><td>TCP 12337</td><td>Health, metrics, events, hooks and file links</td><td>Keep private; expose selected authenticated paths through HTTPS</td></tr><tr><td>TCP 12339</td><td>Optional Query over SSH</td><td>Publish deliberately when enabled</td></tr></tbody></table>
<p>For Docker bridge networking or a port-forwarded server, create <code>compose.media.yml</code> with a fixed UDP listener. <code>203.0.113.10</code> is a documentation address: replace it with your real public IPv4 address.</p>
<pre><code>services:
  noxa:
    environment:
      NOXA_WEBRTC_UDP_ADDR: ":12367"
      NOXA_WEBRTC_EXTERNAL_IPS: "203.0.113.10"
    ports:
      - "12367:12367/udp"</code></pre>
<pre><code>docker compose -f docker-compose.yml -f compose.media.yml up -d noxa</code></pre>
<p>Use the same override on subsequent Compose commands. Forward UDP <code>12367</code> at the router and allow it through the host firewall. Advertising an external IP does not create a forwarding rule. Configure STUN/TURN for restrictive networks and test from a different network. Private/LAN WebRTC destinations require an explicit, narrow <code>NOXA_WEBRTC_ALLOWED_REMOTE_CIDRS</code> allowlist.</p>`,
      },
      {
        id: 'production-check',
        title: '7. Prepare for real users',
        body: `<ul><li>Replace development database credentials, set <code>NOXA_DEV_MODE=false</code>, and use <code>sslmode=verify-full</code>. The current production validator requires verified database TLS. Set up PostgreSQL certificates and trust correctly; changing <code>POSTGRES_SSLMODE</code> alone does not enable database TLS.</li><li>Choose an admission policy: a strong server password, or deliberate anonymous admission with <code>NOXA_ALLOW_ANONYMOUS=true</code>. Authentication and roles still control protected actions.</li><li>Set a strong Redis password for a non-loopback Redis service. Keep control and file-transfer TLS enabled.</li><li>Pass extra settings explicitly in the Compose service’s <code>environment</code> section when the supplied Compose file does not reference them. A new line in <code>.env</code> is not automatically an application setting.</li><li>Back up the database, matching chat and PII keys, files, recordings, TLS identity and configuration. Complete an isolated restore drill.</li><li>Verify ordinary member login, channel visibility, voice both ways, screen sharing, and an upload/download from outside the host network.</li></ul>
<p>For the browser client, configure an HTTPS domain and the web listener separately. Follow <a href="../mobile/">the mobile and browser guide</a>; passkeys belong to that origin.</p>`,
      },
    ],
    related: ['configuration', 'roles-permissions', 'operations'],
    sources: [
      { label: 'Repository setup reference', href: `${repository}#new-server-setup` },
      { label: 'Docker Compose services', href: `${repository}/blob/main/docker-compose.yml` },
      { label: 'Database role provisioning script', href: `${repository}/blob/main/scripts/provision-database-roles.sql` },
    ],
  },
  {
    slug: 'configuration',
    title: 'Configure networking, TLS and server limits',
    summary: 'Understand environment settings, Docker overrides, DNS discovery, certificate trust and WebRTC networking.',
    category: 'Server administration',
    sections: [
      {
        id: 'configuration-order',
        title: 'Know which configuration is active',
        body: `<p><code>NOXA_*</code> environment variables override <code>config.yaml</code>. The loader looks for the YAML file in the working directory and then <code>/etc/noxa</code>; unspecified settings use built-in defaults. Nested keys use underscores, so <code>webrtc.udp_addr</code> becomes <code>NOXA_WEBRTC_UDP_ADDR</code>.</p>
<p>The application does not load <code>.env</code> itself. Docker Compose reads it for interpolation, and the supplied service configures noXa through its explicit <code>environment</code> mapping. For an extra setting such as the displayed server name, add it to an override rather than assuming a new <code>.env</code> line is passed through.</p>
<pre><code>services:
  noxa:
    environment:
      NOXA_SERVER_NAME: "The Common Room"
      NOXA_MAX_CLIENTS: "100"</code></pre>
<p>Review the sample YAML, Compose service and full config schema when changing a setting. Recreate the service with your chosen override files, then inspect startup validation and logs. Redact passwords and complete database URLs before sharing configuration output.</p>`,
      },
      {
        id: 'secrets',
        title: 'Separate development settings from production secrets',
        body: `<p><code>.env.example</code> and <code>config.yaml</code> are development starting points. Production requires <code>NOXA_DEV_MODE=false</code>, unique database credentials with <code>sslmode=verify-full</code>, an intentional admission policy and authenticated Redis when it is remote.</p>
<p>The Compose stack separates the database owner used by <code>noxa-operator</code>, the <code>noxa_runtime</code> account used by the serving process and the <code>noxa_backup</code> read-only account. Run the tracked provisioning script after offline schema setup.</p>
<p>Supported Compose secrets accept a plain value or an <code>_FILE</code> source. Configure one nonempty source for each secret. An <code>_FILE</code> path is a host file mounted read-only into the selected container; keep it outside the repository. On Linux, protect the parent directory from unrelated users and mount only the selected file.</p>
<aside class="guide-note">Changing a password variable does not change a PostgreSQL role’s existing password automatically. Coordinate database role changes and application secret changes during maintenance.</aside>`,
      },
      {
        id: 'hostname-discovery',
        title: 'Let people join with a hostname',
        body: `<p>The desktop client accepts a hostname without a port. With no SRV record, it uses control TCP port <code>12333</code>. An explicit <code>hostname:port</code> bypasses SRV discovery.</p>
<pre><code>_noxa._tcp.voice.example.com. 3600 IN SRV 0 5 23456 node.example.com.
node.example.com.            3600 IN A   203.0.113.10</code></pre>
<p>This documentation example makes <code>voice.example.com</code> resolve to the control listener at <code>node.example.com:23456</code>. Replace both hostnames and the reserved IP. The SRV target needs an A or AAAA record. SRV advertises control discovery; it does not configure media forwarding.</p>
<p>Bookmarks retain the entered hostname and resolve it again on reconnect. Certificate pins remain associated with that entered host; changing an SRV destination does not bypass an existing pin. Authentication and certificate trust failures stop connection recovery.</p>`,
      },
      {
        id: 'media-and-turn',
        title: 'Choose reachable WebRTC and TURN addresses',
        body: `<p>Set a fixed <code>NOXA_WEBRTC_UDP_ADDR</code> when publishing one media port through Docker or NAT. <code>NOXA_WEBRTC_EXTERNAL_IPS</code> requires that fixed listener. Publish and forward the same UDP port, and verify reachable ICE candidates from another network.</p>
<p><code>NOXA_WEBRTC_ALLOWED_REMOTE_CIDRS</code> is an explicit allowlist for intended private, LAN or VPN client networks. Default remote ICE validation blocks private and loopback destinations. Add only networks your deployment actually serves.</p>
<p>The optional <code>turn</code> profile supports clients behind restrictive NATs. Configure the relay’s public address, allowed peer policy, listener and relay ports. The server and coturn must use matching shared-secret and realm settings; the client-facing TURN URIs must name reachable listeners. Follow the comments in the current Compose file and environment sample for the selected topology.</p>
<p>Test control login, microphone audio, camera, screen sharing and file transfer independently. A working login or UDP keepalive response does not establish that WebRTC is reachable.</p>`,
      },
      {
        id: 'certificates-and-web',
        title: 'Preserve certificate identity and HTTPS boundaries',
        body: `<p>By default, control and file-transfer connections use TLS. With no supplied certificate, the server generates and reuses its identity under <code>NOXA_TLS_DIR</code>. Back up that directory. Losing it changes the fingerprint clients have pinned.</p>
<ol><li>On a planned rotation, record the new startup fingerprint.</li><li>Verify it with users through a separate trusted channel.</li><li>In the changed-certificate warning, choose <strong>Trust new fingerprint</strong> only when the values match.</li><li>Reconnect and verify the connection-security message.</li></ol>
<p>Do not delete the client’s known-server records to bypass a mismatch. If the new fingerprint cannot be verified, stop and restore the prior certificate and key.</p>
<p>Secure messaging is bound to that fingerprint too. Readable history stays, but a desktop may need <strong>Settings → Security → Recover secure messaging</strong> after rotation. Link each phone or browser again from a desktop that already trusts the new certificate through <strong>Connections → PWA / Mobile login</strong>. Browser guests must explicitly review and accept a changed fingerprint before secure messaging resumes.</p>
<p>Keep raw administration and operational listeners private. Use native HTTPS or a correctly configured trusted HTTPS reverse proxy for remote events, incoming hooks and file links. Set <code>NOXA_DOWNLOAD_PUBLIC_ORIGIN</code> to a fixed HTTPS origin and allow only the immediate trusted proxy CIDRs. Proxies must overwrite forwarded-protocol headers.</p>`,
      },
      {
        id: 'web-hosting',
        title: 'Host the mobile and browser app over HTTPS',
        body: `<p>The Docker image includes the built PWA; the product website is a separate static deployment. Choose a stable HTTPS domain for the app. Its origin is also the passkey relying party, so use the domain people should keep using rather than a temporary hostname.</p>
<h3>Fresh host with the bundled Caddy proxy</h3>
<ol><li>Create DNS records for your app hostname that point to this host. The example <code>chat.example.com</code> below must be replaced with your own domain.</li><li>Confirm host TCP ports <code>80</code> and <code>443</code> are available and reachable for Caddy and certificate issuance. The supplied Caddy service also publishes UDP <code>443</code>. If another proxy already owns these ports, use that proxy instead of starting a competing listener.</li><li>Set the following values in the existing <code>.env</code> file alongside the database and other deployment settings.</li></ol>
<pre><code>NOXA_WEB_ENABLED=true
NOXA_WEB_PUBLIC_URL=https://chat.example.com
NOXA_WEB_DOMAIN=chat.example.com</code></pre>
<p><code>NOXA_WEB_PUBLIC_URL</code> is the HTTPS origin, without <code>/app/</code>. The Compose file passes it and <code>NOXA_WEB_ENABLED</code> to the application. <code>NOXA_WEB_DOMAIN</code> configures Caddy’s hostname; it is not a separate application setting. The web listener is internal on <code>:12342</code>, and the supplied Compose network trusts only Caddy’s fixed address for forwarded headers.</p>
<p>Start the mobile profile, preserving the same media override used earlier:</p>
<pre><code>docker compose -f docker-compose.yml -f compose.media.yml --profile mobile up -d noxa caddy
docker compose -f docker-compose.yml -f compose.media.yml --profile mobile logs --tail=50 caddy noxa</code></pre>
<p>If you did not create <code>compose.media.yml</code>, omit that <code>-f</code> argument. Retain any external-database or other deployment overrides on every command. Caddy obtains and renews the public certificate, proxies the app and its WebSockets, and stores its state in persistent named volumes. Keep the media UDP forwarding from the server setup guide; HTTPS alone does not make voice reachable.</p>
<p>Open <code>https://chat.example.com/app/</code> and check <code>https://chat.example.com/api/v1/web/info</code>. Then link a device through <strong>Connections → PWA / Mobile login</strong>, compare the confirmation codes and test a chat and voice session.</p>
<h3>Use an existing HTTPS reverse proxy</h3>
<p>Leave the bundled Caddy service stopped. Connect your proxy to the intended backend network and forward to the web listener, <code>noxa:12342</code> when both services can resolve that Docker name. The backend should stay private. Keep <code>NOXA_WEB_ENABLED=true</code> and the app’s public HTTPS origin.</p>
<p>The base Compose file sets <code>NOXA_WEB_TRUSTED_PROXIES</code> directly to Caddy’s address. Adding a different value only to <code>.env</code> does not replace it. Override the service’s environment explicitly for your own proxy:</p>
<pre><code>services:
  noxa:
    environment:
      NOXA_WEB_ENABLED: "true"
      NOXA_WEB_PUBLIC_URL: "https://chat.example.com"
      NOXA_WEB_TRUSTED_PROXIES: "192.0.2.10/32"</code></pre>
<p><code>192.0.2.10/32</code> is a reserved documentation example, not a working proxy address. Replace it with the exact immediate proxy address or narrow CIDR the backend actually sees. The proxy must overwrite <code>X-Forwarded-For</code> and <code>X-Forwarded-Proto</code>, preserve WebSocket upgrades for <code>/ws</code> and <code>/ft</code>, and allow long-lived connections. Never trust forwarded headers from arbitrary clients or a whole public network.</p>
<h3>Choose a dedicated domain or share the product website’s origin</h3>
<p>On a dedicated app domain, proxy the whole origin to the web listener; its root redirects to <code>/app/</code>. This is the simplest fit for the bundled Caddy service.</p>
<p>On a shared marketing origin, keep <code>/</code>, <code>/guides/</code> and product assets on the static website host. Route <code>/app/</code>, <code>/link</code>, <code>/ws</code>, <code>/ft</code>, <code>/api/v1/web/</code> and app invitations under <code>/invite/</code> to the noXa web listener, preserving their paths and methods. The app needs those API and linking routes as well as its HTML, service worker and assets. Route remote hooks, events and server download links to their separate health-service backend only when deliberately configured; they are not the PWA’s <code>/ft</code> WebSocket.</p>
<p>Test a direct app reload, device linking, an invitation, WebSocket connection, file transfer and installation after changing proxy routes. Keep one intended HTTPS origin throughout login and linking.</p>`,
      },
      {
        id: 'capacity',
        title: 'Set practical capacity and media limits',
        body: `<p>Start with a capacity your network and host can support, then measure. Common settings include <code>NOXA_MAX_CLIENTS</code>, <code>NOXA_CLIENT_TIMEOUT_SECONDS</code>, file size and connection limits, and the default Opus bitrate, FEC, DTX and stereo controls.</p>
<p>A stream publishes one current encoding, and every viewer receives that source. A slower viewer can require the sender to lower the source quality. Server video limits and explicitly selected low bandwidth mode still apply.</p>
<p>Channel voice, video and screen sharing are always end-to-end encrypted on current releases. They require the database device directory, control-channel TLS and supported client encryption transforms. There is no unencrypted fallback. Audio uses Opus; updated servers and clients support encrypted H.264 video as well as VP8. Sending camera or screen video requires verified runtime hardware-encoder evidence; an unsupported or unverified encoder stops that video publication. HEVC publication remains unavailable while channel media is encrypted. Encrypted channels hold at most 64 participants and cannot be used while invisible. Server recording cannot be started; whisper, uploaded stream previews and the connection benchmark remain unavailable.</p>
<p>Keep metrics private unless remote exposure is deliberate. Profiling is opt-in and remains direct-loopback-only. Server limits control resource use; they are separate from channel permissions and do not grant access.</p>`,
      },
    ],
    related: ['server-setup', 'operations', 'troubleshooting'],
    sources: [
      { label: 'Configuration reference', href: `${repository}#configuration` },
      { label: 'Environment sample', href: `${repository}/blob/main/.env.example` },
      { label: 'Configuration schema and validation', href: `${repository}/blob/main/internal/config/config.go` },
    ],
  },
  {
    slug: 'operations',
    title: 'Back up, update and troubleshoot a server',
    summary: 'Keep recoverable data, protect the server identity, verify health and plan maintenance with a restore path.',
    category: 'Server administration',
    sections: [
      {
        id: 'recovery-point',
        title: 'Back up data and matching keys together',
        body: `<p>A noXa recovery point needs PostgreSQL and the matching files, server/channel assets, recordings, encryption keys, TLS identity and configuration. A database-only backup can reference missing files. A filesystem-only copy lacks the matching ownership and authorization state.</p>
<ol><li>Record the noXa version, image digest, PostgreSQL version, UTC timestamp and redacted configuration.</li><li>Stop writes or use a snapshot system that provides a documented common point in time. Avoid a best-effort recursive copy of a live mutable directory.</li><li>Create a PostgreSQL custom-format dump and capture the corresponding file and asset roots.</li><li>Preserve the actual PII key and complete chat master key ring. Protect private keys in encrypted secret backup storage with a documented relationship to that recovery point.</li><li>Hash artifacts, encrypt them for operator-controlled recipients, upload to separate storage, and verify the uploaded sizes and digests.</li></ol>
<p>The current Compose backup image creates an encrypted bundle containing the database and actual PII/chat keys. It requires an age recipients file; keep decryption keys off the server. It does not replace a coordinated backup of uploaded files, recordings, TLS identity and the rest of your deployment. Preserve the web push VAPID key if the deployment should retain existing push subscriptions.</p>
<p>Direct and private-group messages use device-held Olm sessions; a server backup does not give the operator their plaintext or recreate a device’s readable history. Keep client history recovery and the server recovery plan distinct.</p>`,
      },
      {
        id: 'compose-backups',
        title: 'Use the optional Compose backup service',
        body: `<p>Provision <code>noxa_backup</code> with the tracked database role script. Configure exactly one backup password source, <code>BACKUP_AGE_RECIPIENTS_FILE</code>, and any selected remote-storage settings before enabling the <code>backup</code> profile.</p>
<pre><code>docker compose --profile backup build postgres-backup
docker compose --profile backup run --rm --no-deps -e BACKUP_RUN_ONCE=1 postgres-backup</code></pre>
<p>For scheduled service operation, start <code>postgres-backup</code> under the same profile without <code>BACKUP_RUN_ONCE=1</code>. A one-shot job should use <code>run --rm</code>; setting the one-shot flag on a service with a restart policy can repeat it.</p>
<p>External databases require the explicit <code>BACKUP_PGHOST</code>, <code>BACKUP_PGPORT</code>, <code>BACKUP_PGUSER</code>, <code>BACKUP_PGDATABASE</code> and <code>BACKUP_PGSSLMODE</code> settings, along with the backup password. Use both the base and external-database Compose files on every command. Supply the required CA/client TLS files for the external database. The backup service does not infer its password from the serving database URL.</p>
<p>Check backup logs, the encrypted artifact, and the remote upload. A successful process exit is only one part of verification; complete the restore drill below.</p>`,
      },
      {
        id: 'restore-drill',
        title: 'Verify a restore before depending on it',
        body: `<ol><li>Use an isolated network, a new empty database and new empty file roots. Do not overlay production directories.</li><li>Verify artifact signatures or digests before decrypting and extracting. Reject unsafe archive paths, links and unexpected special files.</li><li>Restore the database, matched files/assets and required key material from the same recovery point.</li><li>Start the matching noXa version. Check readiness and schema version, stop cleanly, and verify a second start accepts the existing roles policy.</li><li>With synthetic non-admin accounts, test login, channel access, encrypted history, one upload/download digest and server/channel assets. Confirm unauthorized access is denied.</li><li>Record the result, recovery-point ID and recovery time. Remove isolated credentials and plaintext after the drill.</li></ol>
<aside class="guide-note">The restored channel table is authoritative: startup can remove canonical numeric file directories for channels absent from it. Never pair a newer file snapshot with an older or incomplete database restore. Current fresh-install roles-v1 releases reject incompatible older database generations; do not hand-edit schema markers to force a start.</aside>`,
      },
      {
        id: 'updates',
        title: 'Prepare a controlled update',
        body: `<p>Read the release notes and record the current version and image digest. Create a verified recovery point before changing binaries or schema. Rehearse the candidate against an isolated restore when compatibility is uncertain.</p>
<p>Stop the serving process before offline migration or account tools. Use the migration owner through <code>noxa-operator</code> for schema work, then refresh service-role grants with the tracked provisioning script when required. The serving database account should retain only its intended runtime privileges.</p>
<p>For the end-to-end encryption release, deploy the server before or together with updated clients. Updated clients refuse an older server’s login challenge and do not fall back. Older clients cannot join encrypted channel voice or use current end-to-end encrypted private messaging. Keep client and server versions compatible.</p>
<p>After starting the candidate, check owner login and ordinary-member access, chat, voice, media and files. Roll back using a compatible version and matched recovery point; replacing only the executable does not reverse a database migration.</p>
<p>Preserve the TLS directory during maintenance so clients keep the same server identity. Planned certificate rotation requires independent fingerprint verification.</p>`,
      },
      {
        id: 'health-checks',
        title: 'Distinguish health from a working user session',
        body: `<pre><code>docker compose ps
docker compose logs --tail=100 noxa
docker compose exec noxa wget -qO- http://127.0.0.1:12337/readyz</code></pre>
<p><code>/healthz</code> is the liveness probe; <code>/readyz</code> checks readiness. When native health HTTPS is enabled, use the appropriate trusted HTTPS client and endpoint. Keep these listeners private.</p>
<p>Use an authenticated ordinary-member probe for the experience that matters: login, visible channels, chat, voice and a known file. For media failures, compare sender, server and viewer evidence in stream details before changing quality settings.</p>
<p>When collecting logs, remove account secrets, tokens, complete database URLs and personal information. Production logs sample repeated messages, so a low repeated-log count does not by itself prove the underlying issue stopped.</p>`,
      },
    ],
    related: ['server-setup', 'configuration', 'troubleshooting'],
    sources: [
      { label: 'Repository operations reference', href: `${repository}#operations` },
      { label: 'Compose backup service', href: `${repository}/blob/main/docker-compose.yml` },
      { label: 'Backup implementation', href: `${repository}/blob/main/scripts/postgres-backup.sh` },
    ],
  },
  {
    slug: 'integrations',
    title: 'Connect webhooks and administration tools',
    summary: 'Create channel-scoped incoming webhooks and use role-aware Query, SSH and gRPC integrations.',
    category: 'Server administration',
    sections: [
      {
        id: 'incoming-webhooks',
        title: 'Create an incoming channel webhook',
        body: `<p>In the channel, open <strong>Threads and forum → Incoming webhooks</strong>. A channel manager can create a named integration, copy its one-time bearer token, list integrations and revoke one.</p>
<p>The creator needs current View Channel, Read History, Manage Channels and Send Messages permissions. The webhook is fixed to that channel. Revocation, lost creator permissions, a creator ban or deletion of the account/channel stops future posts.</p>
<ol><li>Create the integration in the intended channel.</li><li>Copy the token once into your automation’s secret store.</li><li>Expose the required <code>/hooks/&lt;id&gt;</code> path through your HTTPS proxy.</li><li>Post a test message and verify its attribution in that channel.</li></ol>
<p>Tokens belong in the Authorization header, never in the URL. Do not publish the token in screenshots, logs or repository files.</p>`,
      },
      {
        id: 'post-message',
        title: 'Send and verify a webhook message',
        body: `<p>Replace the example hostname and hook ID. Supply <code>NOXA_WEBHOOK_TOKEN</code> from your secret manager; do not paste a live token into a shared command.</p>
<pre><code>curl --request POST 'https://voice.example.com/hooks/123' --header "Authorization: Bearer $NOXA_WEBHOOK_TOKEN" --header 'Content-Type: application/json' --data '{"content":"Build passed"}'</code></pre>
<p>A successful post returns HTTP <code>201</code> and a <code>message_id</code>. Only the JSON <code>content</code> property is supported. Messages appear as <strong>Integration name [Webhook]</strong>. The server receives and encrypts this channel message; it is not a direct-message E2EE channel.</p>
<table><thead><tr><th>Response</th><th>Action</th></tr></thead><tbody><tr><td>400</td><td>Check supported content and size.</td></tr><tr><td>401</td><td>Check the secret, revocation and creator eligibility.</td></tr><tr><td>429</td><td>Respect Retry-After and reduce the posting rate.</td></tr><tr><td>503</td><td>Check service, storage and policy availability.</td></tr></tbody></table>
<p>The documented webhook limit is five posts per three seconds, up to twenty hooks per channel and 12,000 content bytes or the server’s smaller chat limit. Channel slow mode and moderation also apply. Avoid blindly retrying an ambiguous connection failure: the first message may already have committed.</p>`,
      },
      {
        id: 'query-account',
        title: 'Provision a least-privilege integration account',
        body: `<p>Stop the server and use the offline <code>adduser -integration</code> option for a new integration account. It enables integration login and grants no roles. The <code>-bot</code> option alone does not enable integration access; rerunning <code>adduser</code> for an existing nickname changes nothing.</p>
<p>Restart, then assign only the roles the integration requires through the owner’s Roles manager. Every protected operation rechecks current capabilities, hierarchy, bans and account eligibility. A long-lived connection does not preserve permissions after revocation.</p>
<p>Use the operator service and password handling described in the <a href="../server-setup/#initialize-owner">server setup guide</a>. Do not give an automation the owner account simply to make its first request work.</p>`,
      },
      {
        id: 'transports',
        title: 'Choose Query, SSH or gRPC',
        body: `<p>Raw ServerQuery is a line-based administration interface on <code>127.0.0.1:12335</code> by default. It is plaintext and intentionally private. For remote administration, enable the SSH transport and deliberately publish its configured port, normally <code>12339</code>. Compose does not publish either administration port by default.</p>
<pre><code>login &lt;integration_nickname&gt; &lt;account_password&gt; authorization_model=roles-v1
channellist
quit</code></pre>
<p>The text above is a protocol session example, not a shell command. Escape Query spaces as <code>\\s</code>. SSH sessions separately negotiate <code>NOXA_AUTHORIZATION_MODEL=roles-v1</code>; gRPC unary requests send <code>noxa-authorization-model: roles-v1</code> metadata. The plaintext gRPC listener must stay on loopback.</p>
<p>Use <code>help</code> for supported Query commands. Read <code>channellist</code> or <code>rolelist</code> to obtain current IDs and revisions before constructing a mutation. Role changes require the current expected revision and the same delegation and hierarchy checks used by the native client. Refresh after a revision conflict rather than reusing stale IDs.</p>`,
      },
      {
        id: 'integration-checklist',
        title: 'Verify access and revocation',
        body: `<ul><li>Confirm the integration sees only the intended channels and members.</li><li>Test an allowed operation and a denied operation with a dedicated test account.</li><li>Revoke a required role or webhook and confirm future operations stop.</li><li>Keep bearer tokens, account passwords and response data out of public logs.</li><li>Review creator permissions when an existing hook stops working; permission loss is an enforced boundary.</li></ul>
<p>There is no compatibility fallback to retired numeric permissions or administrator privilege tokens. Build against the current roles-v1 contract.</p>`,
      },
    ],
    related: ['roles-permissions', 'configuration', 'operations'],
    sources: [
      { label: 'ServerQuery reference', href: `${repository}#serverquery` },
      { label: 'Account provisioning tool', href: `${repository}/tree/main/cmd/adduser` },
      { label: 'Server configuration', href: `${repository}/blob/main/config.yaml` },
    ],
  },
];
