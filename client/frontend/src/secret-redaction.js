// secret-redaction.js — masks pairing and device-link secrets before text
// leaves the app through logs, diagnostics or clipboard exports (item 253).
//
// Device links look like "https://host/link#id=<base64url>&k=<base64url>" and
// come with a typed code formatted "XXXX-XXXX" (alphabet 23456789ABCDEFGHJKMNPQRSTUVWXYZ).
// The QR image is a PNG data URL carrying that same link.

export const REDACTED = "[redacted]";

const LINK_URL_FRAGMENT = /(https?:\/\/[^\s"'<>\\]*?\/link)#[^\s"'<>\\]*/gi;
// Query or fragment parameters that carry link secrets, wherever they appear.
const SECRET_PARAMETERS = /([#&?](?:k|key|secret|token|code|link|pair(?:ing)?[_-]?code|link[_-]?secret|device[_-]?link)=)[^\s&"'<>\\]+/gi;
// JSON members whose value is a secret by construction. "qr" is the PNG data
// URL of the link; "k" and "secret" are the link key; "code" is matched only
// when its value looks like a pairing code (see PAIRING_CODE_VALUE).
const SECRET_JSON_MEMBERS = /("(?:k|secret|token|access_token|refresh_token|link_secret|pairing_code|pairing_token|link_code|device_link|qr)"\s*:\s*")(?:[^"\\]|\\.)*(")/gi;
const CODE_JSON_MEMBER = /("code"\s*:\s*")([A-Za-z0-9]{4}-?[A-Za-z0-9]{4})(")/g;
// A formatted pairing code: two 4-symbol blocks from the link alphabet with at
// least one letter, so plain numeric ranges such as 1080-1920 stay readable.
const PAIRING_CODE = /\b(?=[0-9A-Z]{4}-[0-9A-Z]{4}\b)(?=[0-9-]*[A-Z])[23456789ABCDEFGHJKMNPQRSTUVWXYZ]{4}-[23456789ABCDEFGHJKMNPQRSTUVWXYZ]{4}\b/g;
const QR_DATA_URL = /data:image\/(?:png|svg\+xml|jpeg|gif);base64,[A-Za-z0-9+/=]{64,}/g;

const PATTERNS = [
    [LINK_URL_FRAGMENT, `$1#${REDACTED}`],
    [SECRET_PARAMETERS, `$1${REDACTED}`],
    [SECRET_JSON_MEMBERS, `$1${REDACTED}$2`],
    [CODE_JSON_MEMBER, `$1${REDACTED}$3`],
    [PAIRING_CODE, REDACTED],
    [QR_DATA_URL, `data:image/png;base64,${REDACTED}`],
];

// redactPairingSecrets masks every pairing secret in text. Non-strings are
// returned unchanged so callers can pass whatever they were about to export.
export function redactPairingSecrets(text) {
    if (typeof text !== "string" || text === "") return text;
    let output = text;
    for (const [pattern, replacement] of PATTERNS) output = output.replace(pattern, replacement);
    return output;
}

// containsPairingSecret reports whether redaction would change the text.
export function containsPairingSecret(text) {
    return typeof text === "string" && text !== "" && redactPairingSecrets(text) !== text;
}

// redactPairingSecretsDeep walks plain objects and arrays, masking strings in
// place of values. Keys are kept so JSON exports keep their shape.
export function redactPairingSecretsDeep(value, depth = 0) {
    if (depth > 32) return value;
    if (typeof value === "string") return redactPairingSecrets(value);
    if (Array.isArray(value)) return value.map(item => redactPairingSecretsDeep(item, depth + 1));
    if (value && typeof value === "object" && Object.getPrototypeOf(value) === Object.prototype) {
        const output = {};
        for (const [key, item] of Object.entries(value)) {
            output[key] = /^(?:k|secret|token|access_token|refresh_token|link_secret|pairing_code|pairing_token|link_code|device_link|qr)$/i.test(key) && typeof item === "string" && item !== ""
                ? REDACTED
                : redactPairingSecretsDeep(item, depth + 1);
        }
        return output;
    }
    return value;
}
