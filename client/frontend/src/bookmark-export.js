// (318) Bookmark export. Credentials are excluded unless the user opts in for
// this one export; the default output never carries a password, token or secret.
export const BOOKMARK_EXPORT_VERSION = 1;

// Any key naming a credential, in snake_case, camelCase or short form (pw/spw).
const CREDENTIAL_KEY = /(^|_|[a-z])(password|passwd|passphrase|secret|token|credential|api_?key|private_?key)s?($|_|[A-Z])|^(pw|spw|server_pw|account_pw)$/i;

export function isCredentialKey(key) {
    return CREDENTIAL_KEY.test(String(key || ""));
}

// Returns a deep copy of the bookmark without credential fields (recursively).
export function stripBookmarkCredentials(bookmark) {
    if (!bookmark || typeof bookmark !== "object") return bookmark;
    if (Array.isArray(bookmark)) return bookmark.map(stripBookmarkCredentials);
    const result = {};
    for (const [key, value] of Object.entries(bookmark)) {
        if (isCredentialKey(key)) continue;
        result[key] = value && typeof value === "object" ? stripBookmarkCredentials(value) : value;
    }
    return result;
}

export function bookmarkExportPayload(bookmarks, { includeCredentials = false, now = () => new Date() } = {}) {
    const list = Array.isArray(bookmarks) ? bookmarks.filter(entry => entry && typeof entry === "object") : [];
    return {
        format: "noxa-bookmarks",
        version: BOOKMARK_EXPORT_VERSION,
        exported_at: now().toISOString(),
        include_credentials: !!includeCredentials,
        bookmarks: includeCredentials ? list.map(entry => ({ ...entry })) : list.map(stripBookmarkCredentials),
    };
}

export function serializeBookmarkExport(payload) {
    return JSON.stringify(payload, null, 2);
}

// True when a serialized export still contains a credential-like key.
export function exportContainsCredentials(text) {
    try {
        const payload = JSON.parse(text);
        const walk = value => {
            if (!value || typeof value !== "object") return false;
            if (Array.isArray(value)) return value.some(walk);
            return Object.entries(value).some(([key, inner]) => isCredentialKey(key) || walk(inner));
        };
        return walk(payload.bookmarks ?? payload);
    } catch { return false; }
}
