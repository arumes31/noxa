// dm-model.js holds the pure rules of the Direct Messages view: which
// conversations are listed and in what order, what their preview line says,
// how times and unread counts read, and which encryption state the thread
// header claims. dm-view.js only renders what these functions decide, so the
// rules are unit-tested without a DOM (unit/dm-model.test.mjs).
import { t, currentLanguage } from "./i18n.js";
import { encryptionBadge } from "./message-encryption.js";
import { parseFileRef } from "./chat-parsers.js";

const VOICE_EXTS = new Set(["weba", "ogg", "m4a"]);
const IMAGE_EXTS = new Set(["png", "jpg", "jpeg", "webp"]);
const VIDEO_EXTS = new Set(["mp4", "webm", "ogv", "mov"]);
const PREVIEW_MAX = 80;
const DAY = 24 * 60 * 60 * 1000;

// attachmentKind classifies a body that is exactly one [file:...] reference,
// the shape every attachment and voice note is sent as. Anything else is text.
export function attachmentKind(text) {
    const match = /^\[file:([^\]]+)\]$/.exec(String(text ?? "").trim());
    if (!match) return null;
    const ref = parseFileRef(match[1]);
    if (!ref.valid) return null;
    const ext = (ref.name.split(".").pop() || "").toLowerCase();
    const kind = VOICE_EXTS.has(ext) ? "voice" : ext === "gif" ? "gif" : IMAGE_EXTS.has(ext) ? "image" : VIDEO_EXTS.has(ext) ? "video" : "file";
    return { kind, name: ref.name };
}

// plainPreview flattens a text body to one short line. Inline attachment
// references keep only their file name; nothing is interpreted as markup.
export function plainPreview(text) {
    const flat = String(text ?? "")
        .replace(/\[file:([^\]]+)\]/g, (_, capture) => `📎 ${parseFileRef(capture).name}`)
        .replace(/\s+/g, " ")
        .trim();
    return flat.length > PREVIEW_MAX ? flat.slice(0, PREVIEW_MAX - 1).trimEnd() + "…" : flat;
}

// previewLine is the second line of a conversation row. A body that stayed
// sealed or arrived as plaintext is never previewed: its refusal text is shown
// instead, exactly like the message itself. Own messages read "you: …"; in a
// group, other members' messages name their sender.
export function previewLine(last, { group = false, senderName = "" } = {}) {
    if (!last) return "";
    if (last.deleted) return t("polish.deleted");
    const badge = encryptionBadge(last);
    let text;
    if (badge.bodyKey) text = t(badge.bodyKey);
    else {
        const attachment = attachmentKind(last.text);
        text = attachment ? t(`dm.preview.${attachment.kind}`, { name: attachment.name }) : plainPreview(last.text);
    }
    if (last.self) return t("dm.preview.self", { text });
    if (group && senderName) return t("dm.preview.sender", { name: senderName, text });
    return text;
}

// sortConversations orders the list by last activity, newest first. Pending
// group invitations come first because they wait on this user; conversations
// without any activity sink to the end, alphabetically.
export function sortConversations(list) {
    return [...list].sort((a, b) =>
        Number(!!b.invitation) - Number(!!a.invitation)
        || (Number(b.lastAt) || 0) - (Number(a.lastAt) || 0)
        || String(a.name || "").localeCompare(String(b.name || ""))
        || String(a.key).localeCompare(String(b.key)));
}

export function unreadLabel(count) {
    const n = Math.max(0, Math.floor(Number(count) || 0));
    return n > 99 ? "99+" : n ? String(n) : "";
}

export function totalUnread(list) {
    return list.reduce((sum, item) => sum + (item.invitation ? 0 : Math.max(0, Number(item.unread) || 0)), 0);
}

// filterConversations matches the search box against names and members.
export function filterConversations(list, query) {
    const q = String(query ?? "").trim().toLocaleLowerCase();
    if (!q) return list;
    return list.filter(item => [item.name, ...(item.memberNames || [])].some(name => String(name || "").toLocaleLowerCase().includes(q)));
}

function startOfDay(ms) {
    const date = new Date(ms);
    date.setHours(0, 0, 0, 0);
    return date.getTime();
}

function locale() {
    return currentLanguage() === "de" ? "de-DE" : "en-GB";
}

// relativeTime is the row timestamp: a clock time today, "Yesterday", a
// weekday within the last week and a short date before that.
export function relativeTime(ms, now = Date.now()) {
    if (!ms) return "";
    const days = Math.round((startOfDay(now) - startOfDay(ms)) / DAY);
    if (days <= 0) return new Intl.DateTimeFormat(locale(), { hour: "2-digit", minute: "2-digit", hour12: false }).format(ms);
    if (days === 1) return t("dm.yesterday");
    if (days < 7) return new Intl.DateTimeFormat(locale(), { weekday: "short" }).format(ms);
    const sameYear = new Date(ms).getFullYear() === new Date(now).getFullYear();
    return new Intl.DateTimeFormat(locale(), sameYear ? { day: "numeric", month: "short" } : { day: "numeric", month: "short", year: "numeric" }).format(ms);
}

export function dayKey(ms) {
    const date = new Date(ms);
    return `${date.getFullYear()}-${date.getMonth() + 1}-${date.getDate()}`;
}

// dayLabel names a day separator in the thread.
export function dayLabel(ms, now = Date.now()) {
    const days = Math.round((startOfDay(now) - startOfDay(ms)) / DAY);
    if (days <= 0) return t("dm.today");
    if (days === 1) return t("dm.yesterday");
    const sameYear = new Date(ms).getFullYear() === new Date(now).getFullYear();
    return new Intl.DateTimeFormat(locale(), sameYear ? { weekday: "long", day: "numeric", month: "long" } : { day: "numeric", month: "long", year: "numeric" }).format(ms);
}

// daySeparators returns, for timestamps in display order, the indexes that
// start a new calendar day together with that day's label.
export function daySeparators(stamps, now = Date.now()) {
    const out = [];
    let previous = "";
    stamps.forEach((ms, index) => {
        if (!ms) return;
        const key = dayKey(ms);
        if (key !== previous) out.push({ index, key, label: dayLabel(ms, now) });
        previous = key;
    });
    return out;
}

// conversationEncryption is the claim the thread header makes. A direct
// message is end-to-end only while the peer's key resolves (the same lookup
// the send path uses); without it the client falls back to the server-held
// key, which is said plainly. When the probe is unavailable the newest
// message is the evidence. Private groups are always sealed per member.
export function conversationEncryption({ group = false, peerKey = null, last = null } = {}) {
    if (group) return { kind: "lock", labelKey: "dm.encryption.e2e", helpKey: "dm.encryption.groupHelp" };
    if (peerKey === true) return { kind: "lock", labelKey: "dm.encryption.e2e", helpKey: "chat.directHelp" };
    if (peerKey === false) return { kind: "shield", labelKey: "dm.encryption.server", helpKey: "chat.directServerHelp" };
    const badge = last ? encryptionBadge(last) : null;
    if (badge?.kind === "lock") return { kind: "lock", labelKey: "dm.encryption.e2e", helpKey: "chat.directHelp" };
    if (badge?.kind === "shield") return { kind: "shield", labelKey: "dm.encryption.server", helpKey: "chat.directServerHelp" };
    if (badge?.kind === "warning") return { kind: "warning", labelKey: "dm.encryption.unverified", helpKey: badge.label };
    return { kind: "shield", labelKey: "dm.encryption.pending", helpKey: "dm.encryption.pendingHelp" };
}

// presence summarises what the header and avatar dot say about a peer.
export function presenceOf(uid, clients = [], channels = []) {
    const client = clients.find(item => item.unique_id === uid);
    if (!client) return { state: "offline", channel: "" };
    const state = ["away", "busy"].includes(client.status) ? client.status : client.status === "invisible" ? "offline" : "online";
    const channel = Number(client.channel_id) > 0 ? channels.find(item => item.ChannelID === Number(client.channel_id))?.Name || "" : "";
    return { state, channel };
}

export function presenceLine({ state, channel }) {
    if (state === "offline") return t("dm.status.offline");
    if (channel) return t("dm.status.inVoice", { channel });
    return t(`dm.status.${state}`);
}

// mergeDirect combines the sealed on-disk index (one entry per stored log,
// carrying its newest entry) with the live tab state of this session. A live
// message newer than the stored one wins, and live unread counts are kept.
export function mergeDirect(stored = [], live = [], toMessage = entry => entry) {
    const byID = new Map();
    for (const peer of stored || []) {
        if (!peer?.unique_id) continue;
        const last = peer.last ? toMessage(peer.last) : null;
        byID.set(peer.unique_id, {
            uid: peer.unique_id, name: peer.nickname || "", unread: 0, offline: false,
            last, lastAt: last?.ts || (Number(peer.last_at) || 0) * 1000,
        });
    }
    for (const tab of live || []) {
        if (!tab?.uid) continue;
        const entry = byID.get(tab.uid) || { uid: tab.uid, name: "", unread: 0, offline: false, last: null, lastAt: 0 };
        entry.name = tab.name || entry.name;
        entry.unread = Math.max(0, Number(tab.unread) || 0);
        entry.offline = !!tab.offline;
        if (tab.last && (Number(tab.last.ts) || 0) >= entry.lastAt) { entry.last = tab.last; entry.lastAt = Number(tab.last.ts) || entry.lastAt; }
        byID.set(tab.uid, entry);
    }
    return [...byID.values()];
}
