import { describe, it, beforeEach } from "node:test";
import assert from "node:assert/strict";
import { setLanguage } from "../src/i18n.js";
import { encryptionBadge } from "../src/message-encryption.js";
import {
    attachmentKind, conversationEncryption, dayKey, dayLabel, daySeparators, filterConversations, mergeDirect,
    plainPreview, presenceLine, presenceOf, previewLine, relativeTime, sortConversations, totalUnread, unreadLabel,
} from "../src/dm-model.js";

const e2e = (text, extra = {}) => ({ text, direct: true, enc: true, encVerified: true, encMode: "e2e", ...extra });
const at = (y, m, d, h = 12, min = 0) => new Date(y, m - 1, d, h, min).getTime();

beforeEach(() => setLanguage("en"));

describe("conversation list order", () => {
    it("sorts by last activity, newest first, with invitations on top and idle rows last", () => {
        const list = [
            { key: "dm:oren", name: "Oren", lastAt: 300 },
            { key: "grp:raid", name: "Raid leads", lastAt: 900 },
            { key: "dm:mira", name: "Mira", lastAt: 1000 },
            { key: "grp:new", name: "Zeta", lastAt: 0 },
            { key: "grp:inv", name: "Loot council", lastAt: 0, invitation: true },
            { key: "grp:alpha", name: "Alpha", lastAt: 0 },
        ];
        assert.deepEqual(sortConversations(list).map(item => item.key), ["grp:inv", "dm:mira", "grp:raid", "dm:oren", "grp:alpha", "grp:new"]);
        assert.equal(list[0].key, "dm:oren", "sorting must not mutate the input");
    });

    it("breaks exact ties by name and then by key so the order is stable", () => {
        const list = [{ key: "dm:b", name: "Same", lastAt: 5 }, { key: "dm:a", name: "Same", lastAt: 5 }];
        assert.deepEqual(sortConversations(list).map(item => item.key), ["dm:a", "dm:b"]);
    });
});

describe("unread counts", () => {
    it("caps the badge and hides zero", () => {
        assert.equal(unreadLabel(0), "");
        assert.equal(unreadLabel(-3), "");
        assert.equal(unreadLabel("7"), "7");
        assert.equal(unreadLabel(2.9), "2");
        assert.equal(unreadLabel(99), "99");
        assert.equal(unreadLabel(100), "99+");
        assert.equal(unreadLabel(undefined), "");
    });

    it("totals DM and group unread without counting invitations", () => {
        assert.equal(totalUnread([{ unread: 2 }, { unread: 1 }, { unread: 5, invitation: true }, { unread: -1 }, {}]), 3);
    });
});

describe("preview line", () => {
    it("prefixes own messages and names group senders", () => {
        assert.equal(previewLine(e2e("perfect, see you there", { self: true })), "you: perfect, see you there");
        assert.equal(previewLine(e2e("gg! M+ after?")), "gg! M+ after?");
        assert.equal(previewLine(e2e("pull schedule"), { group: true, senderName: "Torvald" }), "Torvald: pull schedule");
        assert.equal(previewLine(e2e("own group line", { self: true }), { group: true, senderName: "Me" }), "you: own group line");
        assert.equal(previewLine(null), "");
    });

    it("describes attachments by kind and never shows their storage key", () => {
        assert.equal(previewLine(e2e("[file:store#secret-key#note.weba]")), "Voice note");
        assert.equal(previewLine(e2e("[file:store#secret-key#wipe.mp4]")), "Clip");
        assert.equal(previewLine(e2e("[file:store#secret-key#dance.gif]")), "GIF");
        assert.equal(previewLine(e2e("[file:store#secret-key#shot.PNG]")), "Photo");
        assert.equal(previewLine(e2e("[file:store#secret-key#plan.pdf]", { self: true })), "you: File · plan.pdf");
        const mixed = previewLine(e2e("see [file:store#secret-key#plan.pdf] please"));
        assert.equal(mixed, "see 📎 plan.pdf please");
        for (const text of [mixed, previewLine(e2e("[file:store#secret-key#plan.pdf]"))]) assert.doesNotMatch(text, /secret-key/);
    });

    it("shows the refusal instead of a sealed or plaintext body", () => {
        assert.equal(previewLine({ text: "hello in the clear", direct: true }), "This message was blocked because it arrived unencrypted. Plaintext messages are not allowed.");
        assert.equal(previewLine({ text: "[encrypted message — key unavailable]", direct: true }),
            "This message is encrypted. Reconnect to request available keys; if it stays unavailable, ask the sender to resend it.");
        assert.equal(previewLine({ text: "x", deleted: true }), "Message deleted");
    });

    it("flattens whitespace and truncates long text", () => {
        assert.equal(plainPreview("  a \n\n b\t c  "), "a b c");
        const long = plainPreview("x".repeat(200));
        assert.equal(long.length, 80);
        assert.ok(long.endsWith("…"));
        assert.equal(plainPreview(null), "");
    });

    it("classifies only bodies that are exactly one attachment", () => {
        assert.deepEqual(attachmentKind("[file:s#k#a.m4a]"), { kind: "voice", name: "a.m4a", meta: null });
        assert.deepEqual(attachmentKind("[file:s#k#a.ogv]"), { kind: "video", name: "a.ogv", meta: null });
        assert.deepEqual(attachmentKind("[file:s#k#a.jpeg]"), { kind: "image", name: "a.jpeg", meta: null });
        assert.deepEqual(attachmentKind("[file:legacy.txt]"), { kind: "file", name: "legacy.txt", meta: null });
        assert.equal(attachmentKind("hi [file:s#k#a.pdf]"), null);
        assert.equal(attachmentKind(""), null);
    });

    it("speaks German", () => {
        setLanguage("de");
        assert.equal(previewLine(e2e("bis gleich", { self: true })), "du: bis gleich");
        assert.equal(previewLine(e2e("[file:s#k#n.weba]")), "Sprachnachricht");
    });
});

describe("times and day separators", () => {
    const now = at(2026, 10, 17, 21, 30);
    it("uses a clock time today, Yesterday, a weekday this week and a date before", () => {
        assert.equal(relativeTime(at(2026, 10, 17, 21, 24), now), "21:24");
        assert.equal(relativeTime(at(2026, 10, 16, 9, 0), now), "Yesterday");
        assert.equal(relativeTime(at(2026, 10, 14, 9, 0), now), "Wed");
        assert.equal(relativeTime(at(2026, 9, 2, 9, 0), now), "2 Sept");
        assert.equal(relativeTime(at(2025, 9, 2, 9, 0), now), "2 Sept 2025");
        assert.equal(relativeTime(0, now), "");
        setLanguage("de");
        assert.equal(relativeTime(at(2026, 10, 16, 9, 0), now), "Gestern");
        assert.equal(relativeTime(at(2026, 10, 14, 9, 0), now), "Mi");
    });

    it("labels and places a separator at the first message of each day", () => {
        const stamps = [at(2026, 10, 16, 20), at(2026, 10, 16, 21), 0, at(2026, 10, 17, 9), at(2026, 10, 17, 10)];
        const separators = daySeparators(stamps, now);
        assert.deepEqual(separators.map(item => [item.index, item.label]), [[0, "Yesterday"], [3, "Today"]]);
        assert.equal(separators[1].key, dayKey(stamps[3]));
        assert.equal(dayLabel(at(2026, 10, 12), now), "Monday 12 October");
        assert.equal(dayLabel(at(2025, 1, 3), now), "3 January 2025");
    });
});

describe("thread encryption claim", () => {
    it("is end-to-end only while the peer key resolves, and warns that nothing can be sent without it", () => {
        assert.deepEqual(conversationEncryption({ peerKey: true }), { kind: "lock", labelKey: "dm.encryption.e2e", helpKey: "chat.directHelp" });
        assert.deepEqual(conversationEncryption({ peerKey: false, last: e2e("x") }), { kind: "warning", labelKey: "dm.encryption.noKey", helpKey: "dm.encryption.noKeyHelp" });
    });

    it("falls back to the newest message when the key could not be checked", () => {
        assert.equal(conversationEncryption({ last: e2e("x") }).kind, "lock");
        // A server-mode DM is never shielded.
        assert.deepEqual(conversationEncryption({ last: { text: "x", direct: true, enc: true, encVerified: true, encMode: "server" } }),
            { kind: "warning", labelKey: "dm.encryption.unverified", helpKey: "chat.downgradedHelp" });
        // Even a shield-drawing message without the direct flag is no shield here.
        assert.equal(conversationEncryption({ last: { text: "x", enc: true } }).kind, "warning");
        const refused = conversationEncryption({ last: { text: "plain", direct: true } });
        assert.equal(refused.kind, "warning");
        assert.equal(refused.helpKey, "chat.plaintextHelp");
        assert.deepEqual(conversationEncryption({}), { kind: "shield", labelKey: "dm.encryption.pending", helpKey: "dm.encryption.pendingHelp" });
    });

    it("always claims end-to-end for private groups", () => {
        assert.equal(conversationEncryption({ group: true, peerKey: false }).kind, "lock");
    });

    it("matches the per-message badge for the same message", () => {
        for (const message of [e2e("a"), { text: "b", direct: true, enc: true, encVerified: true, encMode: "server" }, { text: "c", direct: true }]) {
            assert.equal(conversationEncryption({ last: message }).kind, encryptionBadge(message).kind);
        }
    });
});

describe("presence", () => {
    const clients = [
        { unique_id: "mira", channel_id: 2, status: "" },
        { unique_id: "nyx", channel_id: 0, status: "away" },
        { unique_id: "ghost", channel_id: 2, status: "invisible" },
        { unique_id: "busy", channel_id: 9, status: "busy" },
    ];
    const channels = [{ ChannelID: 2, Name: "Raid Night" }];
    it("reports voice channel, status and offline peers", () => {
        assert.deepEqual(presenceOf("mira", clients, channels), { state: "online", channel: "Raid Night" });
        assert.deepEqual(presenceOf("nyx", clients, channels), { state: "away", channel: "" });
        assert.deepEqual(presenceOf("ghost", clients, channels), { state: "offline", channel: "Raid Night" });
        assert.deepEqual(presenceOf("busy", clients, channels), { state: "busy", channel: "" });
        assert.deepEqual(presenceOf("gone", clients, channels), { state: "offline", channel: "" });
        assert.equal(presenceLine(presenceOf("mira", clients, channels)), "In voice · Raid Night");
        assert.equal(presenceLine(presenceOf("nyx", clients, channels)), "Away");
        assert.equal(presenceLine(presenceOf("ghost", clients, channels)), "Offline");
    });
});

describe("search and merge", () => {
    it("matches names and group members, case-insensitively", () => {
        const list = [{ name: "Mira" }, { name: "Raid leads", memberNames: ["Torvald", "Mira"] }, { name: "Oren" }];
        assert.deepEqual(filterConversations(list, " tor ").map(item => item.name), ["Raid leads"]);
        assert.equal(filterConversations(list, "").length, 3);
        assert.deepEqual(filterConversations(list, "MIRA").map(item => item.name), ["Mira", "Raid leads"]);
    });

    it("merges stored logs with live tabs, keeping the newer message and live unread", () => {
        const stored = [
            { unique_id: "mira", nickname: "Mira", last_at: 100, last: { body: "old", sent_at: 100 } },
            { unique_id: "kestrel", nickname: "Kestrel", last_at: 50 },
            { nickname: "broken" },
        ];
        const live = [
            { uid: "mira", name: "Mira R.", unread: 2, offline: true, last: { text: "new", ts: 200000 } },
            { uid: "jinx", name: "Jinx", unread: 1, last: null },
            {},
        ];
        const merged = mergeDirect(stored, live, entry => ({ text: entry.body, ts: entry.sent_at * 1000 }));
        const byID = Object.fromEntries(merged.map(item => [item.uid, item]));
        assert.deepEqual(Object.keys(byID).sort(), ["jinx", "kestrel", "mira"]);
        assert.equal(byID.mira.last.text, "new");
        assert.equal(byID.mira.lastAt, 200000);
        assert.equal(byID.mira.unread, 2);
        assert.equal(byID.mira.offline, true);
        assert.equal(byID.mira.name, "Mira R.");
        assert.equal(byID.kestrel.lastAt, 50000);
        assert.equal(byID.kestrel.last, null);
        assert.equal(byID.jinx.unread, 1);
        const olderLive = mergeDirect([{ unique_id: "a", last: { body: "stored", sent_at: 300 } }], [{ uid: "a", last: { text: "live", ts: 1000 } }], entry => ({ text: entry.body, ts: entry.sent_at * 1000 }));
        assert.equal(olderLive[0].last.text, "stored");
        assert.deepEqual(mergeDirect(), []);
        assert.deepEqual(mergeDirect(null, null).length, 0);
    });
});
