// harness.mjs — end-to-end support: starts the real server binary with the
// web listener on loopback, and drives a "desktop" session over the TLS
// control port with the same JS core the app uses.
import { execFileSync, spawn } from "node:child_process";
import { mkdtempSync, mkdirSync, writeFileSync } from "node:fs";
import dgram from "node:dgram";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import tls from "node:tls";
import { fileURLToPath } from "node:url";

import { Connection } from "../src/core/connection.js";
import { generateIdentity, publicKeyPEM, sign, toBase64, uniqueIDFromPublicKey } from "../src/core/crypto.js";
import { Msg } from "../src/core/frame.js";

const here = path.dirname(fileURLToPath(import.meta.url));
export const repoRoot = path.resolve(here, "..", "..");

async function freePort() {
    return new Promise((resolve, reject) => {
        const server = net.createServer();
        server.listen(0, "127.0.0.1", () => {
            const { port } = server.address();
            server.close(() => resolve(port));
        });
        server.on("error", reject);
    });
}

async function freeUDPPort() {
    return new Promise((resolve, reject) => {
        const socket = dgram.createSocket("udp4");
        socket.bind(0, "127.0.0.1", () => {
            const { port } = socket.address();
            socket.close(() => resolve(port));
        });
        socket.on("error", reject);
    });
}

const slash = p => p.replace(/\\/g, "/");

function tool(name) {
    return path.join(process.env.NOXA_E2E_BIN, `noxa-${name}${process.platform === "win32" ? ".exe" : ""}`);
}

/**
 * Provisions an empty database like an operator: schema, one owner and one
 * member account, then roles-v1 activation. Returns the member credentials.
 */
function provision(dir, databaseURL) {
    const env = { ...process.env, NOXA_DATABASE_URL: databaseURL, NOXA_DEV_MODE: "true" };
    execFileSync(tool("migrate"), [], { env, stdio: "pipe" });
    const accounts = {};
    const run = Date.now().toString(36);
    // Each spec file has its own members so phones and first devices do not
    // collide with the other specs' device limits: pax..val presence-notify,
    // dia/quin/dot/tao dm-activity, gus/gil/gwen group-manage, vern/yara voice-notes,
    // abe/bea/cid the chat tools specs.
    for (const name of ["owner", "rowan", "ivy", "kai", "lou", "mia", "noa", "vic", "wes", "xan", "pia", "ray", "sol",
        "pax", "quinn", "remy", "sam", "tess", "uma", "val",
        "dia", "quin", "dot", "tao", "gus", "gil", "gwen", "vern", "yara", "abe", "bea", "cid", "zed", "zoe"]) {
        const password = `pw-${name}-${run}`;
        const nickname = `${name}-${run}`;
        // The owner may also log in to ServerQuery (e.g. to set the server rules).
        const flags = name === "owner" ? ["-integration"] : [];
        const out = execFileSync(tool("adduser"), ["-nickname", nickname, "-password-stdin", ...flags], { env, input: `${password}\n` }).toString();
        accounts[name] = { uid: /unique_id: (\S+)/.exec(out)[1], password, nickname };
    }
    execFileSync(tool("role-setup"), ["-owner-uid", accounts.owner.uid, "-activate", "-confirm", "ACTIVATE-ROLES-V1",
        "-chat-master-key-file", path.join(dir, "data", "keys", "chat_master.key")], { env, stdio: "pipe" });
    return accounts;
}

/** Starts the server binary; resolves with {webURL, controlAddr, accounts, stop}. */
export async function startServer({ databaseURL = process.env.NOXA_E2E_DATABASE_URL } = {}) {
    if (!process.env.NOXA_E2E_BIN || !databaseURL) throw new Error("set NOXA_E2E_BIN and NOXA_E2E_DATABASE_URL (an empty database)");
    const binary = tool("server");
    const dir = mkdtempSync(path.join(os.tmpdir(), "noxa-mobile-e2e-"));
    for (const sub of ["files", "tls", "keys", path.join("data", "keys")]) mkdirSync(path.join(dir, sub), { recursive: true });
    const accounts = provision(dir, databaseURL);
    const ports = {};
    for (const name of ["tcp", "grpc", "health", "query", "file", "web"]) ports[name] = await freePort();
    ports.udp = await freeUDPPort();
    ports.sfu = await freeUDPPort();
    ports.turn = await freeUDPPort();
    // Chrome gathers on real interfaces only, so a loopback relay never
    // answers it: serve TURN on a private LAN address when there is one.
    const turnHost = process.env.NOXA_E2E_TURN_HOST || Object.values(os.networkInterfaces()).flat()
        .find(i => i?.family === "IPv4" && !i.internal && /^(10\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.)/.test(i.address))?.address || "127.0.0.1";
    const relayBase = 41000 + Math.floor(Math.random() * 2000);
    const yaml = [
        `server_name: "noXa e2e"`, `client_min_version: "0.0.0"`, `server_password: ""`, `allow_anonymous: true`,
        `log_level: info`, `dev_mode: true`,
        // Specs send bursts from one account over several sessions; keep the per-user chat meter out of the way.
        `chat_rate_msgs: 100`, `chat_rate_window_seconds: 3`,
        `tcp_addr: "127.0.0.1:${ports.tcp}"`, `udp_addr: "127.0.0.1:${ports.udp}"`, `grpc_addr: "127.0.0.1:${ports.grpc}"`,
        `health_addr: "127.0.0.1:${ports.health}"`, `query_addr: "127.0.0.1:${ports.query}"`, `file_addr: "127.0.0.1:${ports.file}"`,
        `file_root: "${slash(path.join(dir, "files"))}"`, `tls_enabled: true`, `tls_dir: "${slash(path.join(dir, "tls"))}"`,
        `pii_key_file: "${slash(path.join(dir, "keys", "pii.key"))}"`, `query_ssh_host_key: "${slash(path.join(dir, "keys", "ssh.key"))}"`,
        `database_url: "${databaseURL}"`, `redis_enabled: false`,
        // Voice tests connect browsers on this machine to the SFU.
        `webrtc:`, `  ice_servers: []`, `  udp_addr: "127.0.0.1:${ports.sfu}"`,
        `  allowed_remote_cidrs: ["127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"]`,
        // Private calls are relay-only; a loopback TURN relays them.
        `turn:`, `  managed: true`, `  listen_addr: "${turnHost}:${ports.turn}"`, `  public_ip: "${turnHost}"`,
        `  relay_min_port: ${relayBase}`, `  relay_max_port: ${relayBase + 200}`, `  realm: "noxa-e2e"`,
        `  uris: ["turn:${turnHost}:${ports.turn}?transport=udp"]`, `  credentials_ttl: 1h`,
        `web:`, `  enabled: true`, `  addr: "127.0.0.1:${ports.web}"`, `  public_url: "https://chat.example"`, `  max_devices_per_account: 100`,
        // Every spec connects from 127.0.0.1 and some keep pages open; the default of 8 sockets per IP runs out.
        `  ws_connections_per_ip: 200`, `  ws_attempts_per_minute: 600`,
        `  trusted_proxies: ["127.0.0.0/8"]`, `  app_dir: "${slash(path.join(repoRoot, "web", "app"))}"`,
    ].join("\n");
    writeFileSync(path.join(dir, "config.yaml"), yaml + "\n");
    const child = spawn(binary, [], { cwd: dir, stdio: ["ignore", "pipe", "pipe"] });
    let log = "";
    child.stdout.on("data", d => { log += d; });
    child.stderr.on("data", d => { log += d; });
    const webURL = `http://127.0.0.1:${ports.web}`;
    const deadline = Date.now() + 30000;
    for (;;) {
        if (child.exitCode !== null) throw new Error(`server exited: ${log.slice(-2000)}`);
        try {
            const response = await fetch(`${webURL}/api/v1/web/info`);
            if (response.ok) break;
        } catch { /* not up yet */ }
        if (Date.now() > deadline) {
            child.kill();
            throw new Error(`server did not start: ${log.slice(-2000)}`);
        }
        await new Promise(r => setTimeout(r, 250));
    }
    return {
        webURL,
        accounts,
        controlAddr: { host: "127.0.0.1", port: ports.tcp },
        // ServerQuery (plain TCP on loopback) sets operator texts such as the rules.
        queryAddr: { host: "127.0.0.1", port: ports.query },
        log: () => log,
        stop: () => new Promise(resolve => {
            if (child.exitCode !== null) return resolve();
            child.once("exit", resolve);
            child.kill();
        }),
    };
}

/** A WebSocket-shaped adapter over the TLS control port (desktop transport). */
class TLSSocketAdapter {
    constructor({ host, port }) {
        this.readyState = 0;
        this.socket = tls.connect({ host, port, rejectUnauthorized: false }, () => {
            this.readyState = 1;
            this.onopen?.();
        });
        this.socket.on("data", data => this.onmessage?.({ data: data.buffer.slice(data.byteOffset, data.byteOffset + data.byteLength) }));
        this.socket.on("error", () => this.onerror?.());
        this.socket.on("close", () => { this.readyState = 3; this.onclose?.(); });
    }
    send(bytes) { this.socket.write(Buffer.from(bytes)); }
    close() { this.socket.end(); }
}

/**
 * Signs a registered account in over the TLS control port the way the
 * desktop does: password plus its identity key, which binds the key.
 */
export async function desktopSession(controlAddr, account, { version = "0.0.0" } = {}) {
    const identity = generateIdentity();
    const conn = await Connection.open("unused", function Adapter() { return new TLSSocketAdapter(controlAddr); });
    const auth = await conn.request(Msg.Authenticate, {
        client_version: version, authorization_models: ["roles-v1"], username: account.uid, password: account.password,
        public_key: publicKeyPEM(identity.edPublic), x25519_public_key: toBase64(identity.xPublic),
    }, Msg.AuthResponse);
    if (!auth.ok) throw new Error(auth.reason);
    return { conn, identity, auth, uniqueID: auth.unique_id };
}

export { sign, publicKeyPEM, uniqueIDFromPublicKey };


/** Signs a guest in over the TLS control port with a fresh key, like an unregistered desktop. */
export async function guestSession(controlAddr, { version = "0.0.0", nickname = "Guest" } = {}) {
    const { signIn } = await import("../src/core/device.js");
    const identity = generateIdentity();
    const conn = await Connection.open("unused", function Adapter() { return new TLSSocketAdapter(controlAddr); });
    const auth = await signIn(conn, identity, { version, nickname });
    return { conn, identity, auth, uniqueID: auth.unique_id };
}
