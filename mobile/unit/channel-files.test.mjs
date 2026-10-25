import assert from "node:assert/strict";
import { test } from "node:test";

import { breadcrumbs, buildFileLink, ChannelFiles, checksum, formatBytes, isSealed, normalizeFolder, quotaPercent, splitPath, subfolders, verifyChecksum, visibleEntries } from "../src/core/channel-files.js";
import { utf8 } from "../src/core/crypto.js";
import { Msg } from "../src/core/frame.js";

test("folders: normalize, children and breadcrumbs", () => {
    assert.equal(normalizeFolder("/a//b/../c/"), "a/b/c");
    assert.deepEqual(subfolders(["docs", "docs/2026", "docs/2026/q1", "img", "docs-old"], ""), ["docs", "docs-old", "img"]);
    assert.deepEqual(subfolders(["docs", "docs/2026", "docs/2026/q1", "docs/b"], "docs"), ["2026", "b"]);
    assert.deepEqual(breadcrumbs("a/b"), [{ label: "a", folder: "a" }, { label: "b", folder: "a/b" }]);
    assert.deepEqual(splitPath("x/y/z.txt"), { folder: "x/y", name: "z.txt" });
    assert.deepEqual(splitPath("z.txt"), { folder: "", name: "z.txt" });
});

test("filter and sort like the desktop", () => {
    const entries = [
        { name: "b10.txt", size: 5, uploaded_at: "2026-01-03T00:00:00Z" },
        { name: "B2.txt", size: 50, uploaded_at: "2026-01-01T00:00:00Z" },
        { name: "a.png", size: 5, uploaded_at: "2026-01-02T00:00:00Z" },
    ];
    assert.deepEqual(visibleEntries(entries).map(e => e.name), ["a.png", "B2.txt", "b10.txt"]);
    assert.deepEqual(visibleEntries(entries, { sort: "size" }).map(e => e.name), ["a.png", "b10.txt", "B2.txt"]);
    assert.deepEqual(visibleEntries(entries, { sort: "date", descending: true }).map(e => e.name), ["b10.txt", "a.png", "B2.txt"]);
    assert.deepEqual(visibleEntries(entries, { filter: "TXT" }).map(e => e.name), ["B2.txt", "b10.txt"]);
});

test("quota, sizes, sealed rows and checksums", () => {
    assert.equal(quotaPercent(50, 200), 25);
    assert.equal(quotaPercent(500, 200), 100);
    assert.equal(quotaPercent(5, 0), null);
    assert.equal(formatBytes(512), "512 B");
    assert.equal(formatBytes(1536), "1.5 KB");
    assert.equal(formatBytes(20 * 1024 * 1024), "20 MB");
    assert.equal(isSealed({ name: "abc.VCX" }), true);
    assert.equal(isSealed({ name: "x.txt", encrypted: true }), true);
    assert.equal(isSealed({ name: "x.txt" }), false);
    const bytes = utf8("abc");
    assert.equal(checksum(bytes), "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad");
    assert.equal(verifyChecksum(bytes, "BA7816BF8F01CFEA414140DE5DAE2223B00361A396177A9CB410FF61F20015AD"), true);
    assert.equal(verifyChecksum(bytes, "00"), false);
    assert.equal(verifyChecksum(bytes, ""), false);
});

test("temporary links are https /dl/ paths only", () => {
    const path = `/dl/${"a".repeat(32)}`;
    assert.equal(buildFileLink({ path, scheme: "https", health_port: 8443 }, "chat.example"), `https://chat.example:8443${path}`);
    assert.equal(buildFileLink({ path, scheme: "https", health_port: 8443, public_origin: "https://files.example" }, "x"), `https://files.example${path}`);
    assert.equal(buildFileLink({ path, scheme: "http", health_port: 80 }, "x"), "");
    assert.equal(buildFileLink({ path: "/etc/passwd", scheme: "https", health_port: 1 }, "x"), "");
    assert.equal(buildFileLink({ path, scheme: "https", health_port: 0 }, "x"), "");
    assert.equal(buildFileLink({ path, scheme: "https", health_port: 1, public_origin: "http://files.example" }, "x"), "");
    assert.equal(buildFileLink({ path, scheme: "https", health_port: 1, public_origin: "https://a.example/x?y" }, "x"), "");
});

test("requests carry the desktop's fields and wait for the mutation ack", async () => {
    const calls = [];
    const conn = {
        request: async (type, payload, reply, options) => {
            calls.push({ type, payload, reply, options });
            if (type === Msg.FileList) return { entries: [{ name: "a" }], folders: ["d"], used_bytes: 3, quota_bytes: 10 };
            if (type === Msg.FileVersions) return { entries: [{ name: "a.1" }] };
            if (type === Msg.FileLink) return { path: `/dl/${"b".repeat(32)}`, scheme: "https", health_port: 9, public_origin: "https://f.example", expires_at: 100, session_bound: true };
            return { operation: type, name: payload.name, channel_id: payload.channel_id };
        },
    };
    const files = new ChannelFiles(conn, 4);
    assert.deepEqual(await files.list("d"), { entries: [{ name: "a" }], folders: ["d"], used: 3, quota: 10 });
    assert.deepEqual(calls[0].payload, { channel_id: 4, folder: "d" });
    await files.rename("d", "a", { newName: "b", newFolder: "/e/" });
    assert.deepEqual(calls[1].payload, { channel_id: 4, folder: "d", name: "a", new_name: "b", new_folder: "e", ack_requested: true });
    assert.equal(calls[1].reply, Msg.FileMutationSaved);
    assert.equal(calls[1].options.accept({ operation: Msg.FileRename, name: "a", channel_id: 4 }), true);
    assert.equal(calls[1].options.accept({ operation: Msg.FileDelete, name: "a", channel_id: 4 }), false);
    await files.rename("", "a", { newChannelID: 7 });
    assert.equal(calls[2].payload.new_channel_id, 7);
    assert.equal(calls[2].payload.new_name, "a");
    await files.delete("", "a");
    assert.deepEqual(calls[3].payload, { channel_id: 4, folder: "", name: "a", ack_requested: true });
    assert.deepEqual(await files.versions("", "a"), [{ name: "a.1" }]);
    const link = await files.link("", "a");
    assert.equal(link.url, `https://f.example/dl/${"b".repeat(32)}`);
    assert.equal(link.expiresAt, 100000);
});
