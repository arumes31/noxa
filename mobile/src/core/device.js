// device.js — linking this browser to an account and signing in with it.
//   1. claim a link (QR secret or hidden code) before authentication,
//   2. poll until the creating desktop approves or denies,
//   3. sign in with this device's own Ed25519 key.
// Passkeys sign in a fresh device without a desktop.
import { Connection, ProtocolError } from "./connection.js";
import {
    fromBase64URL, keyFingerprint, linkClaimTranscript, linkProof, linkSAS, normalizeLinkCode, publicKeyPEM, sign,
    toBase64, toBase64URL, uniqueIDFromPublicKey, utf8,
} from "./crypto.js";
import { Msg } from "./frame.js";

export const LinkState = Object.freeze({ pending: "pending", claimed: "claimed", approved: "approved", denied: "denied", expired: "expired" });

/** Reads #id=…&k=… from a link URL fragment, or null. */
export function parseLinkFragment(hash) {
    const params = new URLSearchParams(hash.replace(/^#/, ""));
    const id = params.get("id");
    const k = params.get("k");
    if (!id || !k) return null;
    try {
        const secret = fromBase64URL(k);
        return secret.length === 32 ? { linkID: id, secret } : null;
    } catch {
        return null;
    }
}

export function devicePublicKeys(identity) {
    return { pem: publicKeyPEM(identity.edPublic), x25519: toBase64(identity.xPublic) };
}

export function platformName(nav = globalThis.navigator) {
    const ua = nav?.userAgent || "";
    const os = /iPhone/.test(ua) ? "iPhone" : /iPad/.test(ua) ? "iPad" : /Android/.test(ua) ? "Android" : /Mac/.test(ua) ? "Mac" : /Windows/.test(ua) ? "Windows" : "Browser";
    const browser = /EdgA?\//.test(ua) ? "Edge" : /Firefox|FxiOS/.test(ua) ? "Firefox" : /CriOS|Chrome/.test(ua) ? "Chrome" : /Safari/.test(ua) ? "Safari" : "";
    return browser ? `${os} · ${browser}` : os;
}

/**
 * Claims a link. Pass {linkID, secret} from a QR link or {code} typed by
 * hand. Resolves with {linkID, state, sas, approverFingerprint}.
 */
export async function claimLink(conn, identity, link) {
    const { pem, x25519 } = devicePublicKeys(identity);
    const request = { action: "link_claim", ed25519_public_key: pem, x25519_public_key: x25519, platform: platformName() };
    let linkKey;
    if (link.code) {
        request.code = normalizeLinkCode(link.code);
        linkKey = utf8(request.code);
    } else {
        request.link_id = link.linkID;
        request.proof = toBase64(linkProof(link.secret, linkClaimTranscript(link.linkID, pem, x25519)));
        linkKey = link.secret;
    }
    const result = await conn.request(Msg.DeviceRequest, request, Msg.DeviceResult, { accept: r => r.action === "link_claim" });
    if (result.state !== LinkState.claimed) return { linkID: result.link_id || "", state: result.state || LinkState.expired };
    return {
        linkID: result.link_id,
        state: result.state,
        expiresAt: result.expires_at,
        approverFingerprint: result.approver_fingerprint,
        sas: linkSAS(linkKey, linkClaimTranscript(result.link_id, pem, x25519)),
    };
}

/**
 * Polls link_status until the link is decided or expires. Pre-auth
 * connections close after a few seconds, so the poll reconnects as needed.
 */
export async function waitForDecision(identity, linkID, { open = () => Connection.open(), interval = 2000, signal } = {}) {
    const { pem } = devicePublicKeys(identity);
    let conn = null;
    try {
        for (;;) {
            if (signal?.aborted) throw new DOMException("aborted", "AbortError");
            if (!conn?.open) conn = await open();
            try {
                const result = await conn.request(Msg.DeviceRequest, { action: "link_status", link_id: linkID, ed25519_public_key: pem },
                    Msg.DeviceResult, { accept: r => r.action === "link_status" });
                if (result.state !== LinkState.claimed) return result.state;
            } catch (error) {
                if (error instanceof ProtocolError) throw error;
                conn = null;
            }
            await new Promise(resolve => setTimeout(resolve, interval));
        }
    } finally {
        conn?.close();
    }
}

/**
 * Signs in with this device's key. Resolves with the AuthResponse; rejects
 * with the server's reason, e.g. a revoked device.
 */
export async function signIn(conn, identity, { version, nickname = "", serverPassword = "" } = {}) {
    const uniqueID = uniqueIDFromPublicKey(identity.edPublic);
    const x25519 = toBase64(identity.xPublic);
    const first = await conn.request(Msg.Authenticate, {
        client_version: version, authorization_models: ["roles-v1"], username: uniqueID, anonymous: true,
        nickname, server_password: serverPassword, x25519_public_key: x25519,
    }, [Msg.AuthChallenge, Msg.AuthResponse], { timeout: 10000 });
    if (!first.challenge) throw new Error(first.reason || "sign-in failed");
    const signature = sign(Uint8Array.from(atob(first.challenge), c => c.charCodeAt(0)), identity.edSecret);
    const response = await conn.request(Msg.AuthSignature, {
        unique_id: "", public_key: publicKeyPEM(identity.edPublic), signature: toBase64(signature), x25519_public_key: x25519,
    }, Msg.AuthResponse);
    if (!response.ok) throw new Error(response.reason || "sign-in failed");
    return response;
}

// --- Passkeys ----------------------------------------------------------------

function creationOptions(json) {
    const options = json.publicKey;
    if (globalThis.PublicKeyCredential?.parseCreationOptionsFromJSON) {
        return PublicKeyCredential.parseCreationOptionsFromJSON(options);
    }
    return {
        ...options,
        challenge: fromBase64URL(options.challenge),
        user: { ...options.user, id: fromBase64URL(options.user.id) },
        excludeCredentials: (options.excludeCredentials || []).map(c => ({ ...c, id: fromBase64URL(c.id) })),
    };
}

function requestOptions(json) {
    const options = json.publicKey;
    if (globalThis.PublicKeyCredential?.parseRequestOptionsFromJSON) {
        return PublicKeyCredential.parseRequestOptionsFromJSON(options);
    }
    return {
        ...options,
        challenge: fromBase64URL(options.challenge),
        allowCredentials: (options.allowCredentials || []).map(c => ({ ...c, id: fromBase64URL(c.id) })),
    };
}

function b64u(buffer) {
    return buffer ? toBase64URL(new Uint8Array(buffer)) : undefined;
}

/** Serializes a PublicKeyCredential the way the server parses it. */
export function credentialJSON(credential) {
    if (typeof credential.toJSON === "function") return credential.toJSON();
    const response = credential.response;
    const out = { id: credential.id, rawId: b64u(credential.rawId), type: credential.type, clientExtensionResults: credential.getClientExtensionResults?.() || {}, response: { clientDataJSON: b64u(response.clientDataJSON) } };
    if (response.attestationObject) {
        out.response.attestationObject = b64u(response.attestationObject);
        out.response.transports = response.getTransports?.() || [];
    } else {
        Object.assign(out.response, { authenticatorData: b64u(response.authenticatorData), signature: b64u(response.signature), userHandle: b64u(response.userHandle) });
    }
    return out;
}

export function passkeysSupported() {
    return typeof globalThis.PublicKeyCredential === "function" && !!globalThis.navigator?.credentials;
}

/** Saves a passkey for the signed-in account on this linked device. */
export async function registerPasskey(conn, name, credentials = globalThis.navigator.credentials, session = null) {
    const begin = await conn.request(Msg.DeviceRequest, { action: "passkey_register_begin" }, Msg.DeviceResult, { accept: r => r.action === "passkey_register_begin" });
    const publicKey = creationOptions(begin.webauthn);
    // Use only extensions negotiated by the server. An unsolicited PRF output
    // makes strict WebAuthn registration fail (notably with iCloud passkeys).
    const credential = await credentials.create({ publicKey });
    let dmCertificate;
    const template = begin.dm_certificate;
    const spki = credential.response?.getPublicKey?.();
    const algorithm = credential.response?.getPublicKeyAlgorithm?.();
    if (session?.identity && template && spki && [-7,-35,-36,-257,-258,-259,-37,-38,-39,-8].includes(algorithm) && template.principal === session.uniqueID && template.issuer === session.deviceID && template.rp_id === publicKey.rp.id) {
        dmCertificate = { ...template, algorithm, origin: location.origin, credential_id: b64u(credential.rawId), public_key: toBase64(new Uint8Array(spki)) };
        const transcript = ["noxa-passkey-delegation-v1", dmCertificate.principal, dmCertificate.issuer, dmCertificate.credential_id, dmCertificate.public_key, dmCertificate.rp_id, dmCertificate.origin, String(algorithm)].join("\n");
        dmCertificate.signature = toBase64(sign(utf8(transcript), session.identity.edSecret));
    }
    const finish = await conn.request(Msg.DeviceRequest, { action: "passkey_register_finish", name, webauthn: credentialJSON(credential), ...(dmCertificate ? { dm_certificate: dmCertificate } : {}) },
        Msg.DeviceResult, { accept: r => r.action === "passkey_register_finish" });
    return finish.passkeys || [];
}

/**
 * Signs a fresh device in with a synced passkey: the passkey links this
 * device's keys to its account, then the device signs in with its own key.
 */
export async function linkWithPasskey(conn, identity, credentials = globalThis.navigator.credentials) {
    const { pem, x25519 } = devicePublicKeys(identity);
    const begin = await conn.request(Msg.DeviceRequest, { action: "passkey_login_begin", ed25519_public_key: pem, x25519_public_key: x25519 }, Msg.DeviceResult, { accept: r => r.action === "passkey_login_begin" });
    const assertion = await credentials.get({ publicKey: requestOptions(begin.webauthn) });
    return conn.request(Msg.DeviceRequest, {
        action: "passkey_login_finish", webauthn: credentialJSON(assertion), ed25519_public_key: pem, x25519_public_key: x25519, platform: platformName(),
    }, Msg.DeviceResult, { accept: r => r.action === "passkey_login_finish" });
}

export { keyFingerprint };
