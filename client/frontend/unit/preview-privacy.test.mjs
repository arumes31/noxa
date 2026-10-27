import test from "node:test";
import assert from "node:assert/strict";
import { previewConversation, previewConversationFor, previewReferenceConversation, setPreviewConversation } from "../src/preview-privacy.js";

test("only explicit channel messages permit automatic previews", () => {
    for (const channelID of [0, 1, 500]) assert.equal(previewConversation({ channelID }), "channel");
    for (const message of [undefined, {}, { channelID: null }, { channelID: "1" }, { channelID: -1 }, { channelID: 1, isDM: true }, { channelID: 1, direct: true }, { channelID: 1, e2e: true }, { channelID: 1, encMode: "e2e" }]) assert.equal(previewConversation(message), "private");
});

test("hover context follows renderer ownership and private nested contexts override channels", () => {
    const root = {}, body = { parentElement: root }, link = { parentElement: body };
    assert.equal(previewConversationFor(link), "private");
    setPreviewConversation(root, "channel");
    assert.equal(previewConversationFor(link), "channel");
    setPreviewConversation(body, "private");
    assert.equal(previewConversationFor(link), "private");
    // DOM attributes/cloned markup cannot create renderer ownership.
    assert.equal(previewConversationFor({ dataset: { conversation: "channel" } }), "private");
    setPreviewConversation(body, "unknown");
    assert.equal(previewConversationFor(link), "private");
});


test("Activity references authorize only explicit channel destinations", () => {
    for (const kind of ["channel", "thread"]) for (const channel_id of [0, 1]) assert.equal(previewReferenceConversation({ kind, channel_id }), "channel");
    for (const reference of [undefined, {}, { kind: "channel" }, { kind: "channel", channel_id: "0" }, { kind: "dm", channel_id: 0 }, { kind: "group", channel_id: 1 }, { kind: "unknown", channel_id: 0 }]) assert.equal(previewReferenceConversation(reference), "private");
});
