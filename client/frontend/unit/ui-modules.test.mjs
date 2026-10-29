import assert from "node:assert/strict";
import test from "node:test";
import { registerHooks } from "node:module";

// Vite owns stylesheet loading; these Node tests exercise module behavior.
registerHooks({
    load(url, context, nextLoad) {
        if (url.endsWith(".css")) return { format: "module", source: "export {};", shortCircuit: true };
        return nextLoad(url, context);
    },
});

const globalRestorations = new WeakMap();

function replaceGlobal(t, name, value) {
    let restorations = globalRestorations.get(t);
    if (!restorations) {
        restorations = new Map();
        globalRestorations.set(t, restorations);
        t.after(() => {
            for (const [property, previous] of restorations) {
                if (previous) Object.defineProperty(globalThis, property, previous);
                else delete globalThis[property];
            }
        });
    }
    if (!restorations.has(name)) restorations.set(name, Object.getOwnPropertyDescriptor(globalThis, name));
    // Browser Window is an EventTarget. Imported media modules register page
    // lifecycle listeners even when this test exercises only another UI area.
    if (name === "window") value = Object.assign(new EventTarget(), value);
    Object.defineProperty(globalThis, name, { configurable: true, value, writable: true });
}

function classList() {
    const values = new Set();
    return {
        add: (name) => values.add(name),
        contains: (name) => values.has(name),
        remove: (name) => values.delete(name),
        toggle(name, enabled) {
            if (enabled) values.add(name);
            else values.delete(name);
            return !!enabled;
        },
    };
}

function element() {
    const attributes = new Map();
    return {
        attributes,
        classList: classList(),
        focusCalls: 0,
        hidden: false,
        tabIndex: 0,
        focus(options) {
            this.focusCalls++;
            this.focusOptions = options;
        },
        getAttribute: (name) => attributes.get(name) || null,
        setAttribute: (name, value) => attributes.set(name, String(value)),
    };
}

function fileBrowserPane() {
    const pane = element();
    const list = {
        attributes: new Map(),
        innerHTML: "",
        removeAttribute(name) { this.attributes.delete(name); },
        setAttribute(name, value) { this.attributes.set(name, String(value)); },
    };
    const crumb = {
        children: [],
        lastChild: null,
        appendChild(child) {
            this.children.push(child);
            this.lastChild = child;
        },
        removeChild(child) {
            this.children.splice(this.children.indexOf(child), 1);
            this.lastChild = this.children.at(-1) || null;
        },
    };
    const quota = { classList: classList(), removeAttribute() {}, setAttribute() {}, title: "" };
    pane.querySelector = (selector) => ({
        ".fb-crumb": crumb,
        ".fb-list": list,
        ".fb-quota": quota,
        ".fb-filter": element(),
        ".fb-refresh": element(), ".fb-upload": element(), ".fb-mkdir": element(),
        ".fb-emoji": element(), ".fb-banner": element(), ".fb-transfers": element(),
        ".fb-upload span": element(),
    }[selector] || null);
    return { list, pane };
}

test("frontend UI module behaviors", { concurrency: false }, async (t) => {
    await t.test("audio handles mute, PTT, profiles, and local volume", async (t) => {
        const state = {
            settings: {
                capture_device_id: "capture-1",
                echo_cancellation: false,
                muted_users: [],
                noise_suppression: true,
                user_volumes: { alice: 60 },
            },
        };
        const saved = [];
        replaceGlobal(t, "window", {
            __noxa: { state },
            go: {
                main: {
                    App: {
                        GetSettings: async () => saved.at(-1),
                        SaveSettings: async (settings) => { saved.push(settings); return ""; },
                    },
                },
            },
        });
        const audio = await import("../src/audio.js");
        const button = element();
        replaceGlobal(t, "document", { createElement: () => ({ textContent: "" }) });
        button.appendChild = (child) => { button.label = child.textContent; };

        audio.syncMuteButton(button, true);
        assert.equal(button.classList.contains("active"), true);
        assert.equal(button.getAttribute("aria-pressed"), "true");
        assert.equal(button.label, "Microphone muted");
        assert.equal(button.getAttribute("aria-label"), "Unmute microphone");

        audio.syncMuteButton(button, false);
        assert.equal(button.classList.contains("active"), false);
        assert.equal(button.getAttribute("aria-pressed"), "false");
        assert.equal(button.label, "Microphone on");
        assert.equal(button.title, "Mute");
        assert.equal(audio.isMusicChannel({ AudioProfile: "broadcast" }), true);
        assert.equal(audio.isMusicChannel({ IsMusic: false }), false);
        assert.equal(audio.isMusicChannel({ OpusStereo: true, OpusBitrate: audio.MUSIC_MIN_BITRATE }), true);
        assert.deepEqual(audio.captureConstraints({ AudioProfile: "music" }), {
            deviceId: { exact: "capture-1" },
            channelCount: 2,
            echoCancellation: false,
            noiseSuppression: false,
            autoGainControl: false,
        });
        assert.deepEqual(audio.captureConstraints(null), {
            deviceId: { exact: "capture-1" },
            echoCancellation: false,
            noiseSuppression: true,
        });

        const timers = new Map();
        let nextTimer = 0;
        replaceGlobal(t, "setTimeout", (callback) => {
            const id = ++nextTimer;
            timers.set(id, callback);
            return id;
        });
        replaceGlobal(t, "clearTimeout", (id) => timers.delete(id));
        state.settings.ptt_release_delay_ms = 30;
        const ptt = [];
        audio.pttRelease(false, (active) => ptt.push(active));
        audio.pttRelease(true, (active) => ptt.push(active));
        assert.deepEqual(ptt, [true]);
        assert.equal(timers.size, 0);
        audio.pttRelease(false, (active) => ptt.push(active));
        [...timers.values()][0]();
        assert.deepEqual(ptt, [true, false]);

        const captured = [];
        let freshTrack;
        replaceGlobal(t, "navigator", {
            mediaDevices: {
                getUserMedia: async (constraints) => {
                    captured.push(constraints);
                    return { getAudioTracks: () => [freshTrack], getTracks: () => [freshTrack] };
                },
            },
        });
        const oldTrack = { enabled: false, kind: "audio", stopped: false, stop() { this.stopped = true; } };
        freshTrack = { kind: "audio", stopped: false, stop() { this.stopped = true; } };
        const changes = [];
        const stream = {
            addTrack: (track) => changes.push(["add", track]),
            getAudioTracks: () => [oldTrack],
            removeTrack: (track) => changes.push(["remove", track]),
        };
        const sender = { track: oldTrack, replaceTrack: async (track) => changes.push(["replace", track]) };
        const applied = await audio.applyCaptureProfile({ getSenders: () => [sender] }, stream, { AudioProfile: "music" });
        assert.equal(applied.changed, true);
        assert.equal(applied.track, freshTrack);
        assert.equal(freshTrack.enabled, false);
        assert.equal(freshTrack.contentHint, "music");
        assert.equal(oldTrack.stopped, true);
        assert.deepEqual(changes.map(([kind]) => kind), ["replace", "remove", "add"]);
        assert.deepEqual(captured[0], { audio: audio.captureConstraints({ AudioProfile: "music" }) });

        const retained = { enabled: true, kind: "audio", stop() {} };
        audio.markCaptureProfile(retained, { AudioProfile: "music" });
        assert.deepEqual(await audio.applyCaptureProfile(null, { getAudioTracks: () => [retained] }, { AudioProfile: "music" }), {
            changed: false,
            track: retained,
        });

        // A device change inside the same channel must replace the live mic.
        audio.markCaptureProfile(oldTrack, null);
        state.settings.capture_device_id = "capture-2";
        const switched = await audio.applyCaptureProfile({ getSenders: () => [sender] }, stream, null);
        assert.equal(switched.changed, true);
        assert.deepEqual(captured.at(-1).audio.deviceId, { exact: "capture-2" });

        oldTrack.readyState = "ended";
        changes.length = 0;
        freshTrack = { kind: "audio", stop() { this.stopped = true; } };
        const recovered = await audio.applyCaptureProfile({ connectionState: "connected", getSenders: () => [sender] }, stream, null);
        assert.equal(recovered.changed, true, "an ended microphone can be replaced on an open connection");
        assert.deepEqual(changes.map(([kind]) => kind), ["replace", "remove", "add"]);

        const replacementError = new Error("replacement rejected");
        const failed = await audio.applyCaptureProfile({ getSenders: () => [{ track: oldTrack, replaceTrack: async () => { throw replacementError; } }] }, stream, null);
        assert.equal(failed.error, replacementError);
        assert.equal(freshTrack.stopped, true);
        changes.length = 0;
        assert.equal((await audio.applyCaptureProfile({ connectionState: "closed", getSenders: () => [sender] }, stream, null)).changed, false);
        assert.deepEqual(changes, []);

        // A processed microphone replaces the sender output, never the raw
        // capture. Failed and stale swaps must dispose only the new pipeline.
        const processedOld = { kind: "audio" }, processedNext = { kind: "audio" };
        const disposed = [];
        let failPrepare = false, closeDuringPrepare = false, closeDuringReplace = false;
        const gatedPeer = { connectionState: "connected", getSenders: () => [gatedSender] };
        const gatedSender = { track: processedOld, replaceTrack: async track => {
            gatedSender.track = track;
            if (closeDuringReplace) gatedPeer.connectionState = "closed";
        } };
        const gate = {
            sendTrack: () => processedOld,
            prepare: async () => {
                if (failPrepare) throw replacementError;
                if (closeDuringPrepare) gatedPeer.connectionState = "closed";
                return processedNext;
            },
            dispose: track => disposed.push(track),
        };
        const gated = await audio.applyCaptureProfile(gatedPeer, stream, null, gate);
        assert.equal(gated.changed, true);
        assert.equal(gatedSender.track, processedNext);
        assert.equal(disposed.at(-1), oldTrack);
        gatedSender.track = processedOld;
        failPrepare = true;
        assert.equal((await audio.applyCaptureProfile(gatedPeer, stream, null, gate)).error, replacementError);
        assert.equal(gatedSender.track, processedOld);
        failPrepare = false; closeDuringPrepare = true;
        assert.equal((await audio.applyCaptureProfile(gatedPeer, stream, null, gate)).changed, false);
        assert.equal(disposed.at(-1), freshTrack);
        assert.equal(gatedSender.track, processedOld);
        closeDuringPrepare = false; gatedPeer.connectionState = "connected"; closeDuringReplace = true;
        assert.equal((await audio.applyCaptureProfile(gatedPeer, stream, null, gate)).changed, false);
        assert.equal(gatedSender.track, null);
        assert.equal(processedNext.enabled, false);
        assert.equal(disposed.at(-1), freshTrack);

        const gainNode = { gain: { value: 0 } };
        const muteNode = { gain: { value: 0 } };
        audio.registerUserChain("alice", gainNode, muteNode);
        assert.equal(gainNode.gain.value, 0.36);
        audio.setDucking(true, []);
        assert.equal(gainNode.gain.value, 0.09);
        audio.setDucking(true, ["alice"]);
        assert.equal(gainNode.gain.value, 0.36);
        await audio.setUserMuted("alice", true);
        assert.equal(gainNode.gain.value, 0);
        assert.equal(muteNode.gain.value, 0);
        assert.deepEqual(saved[0].muted_users, ["alice"]);
        audio.setDucking(false, []);
        audio.unregisterUserChain("alice", gainNode);

        const compressor = {
            attack: { value: 0 }, knee: { value: 0 }, ratio: { value: 0 }, release: { value: 0 }, threshold: { value: 0 },
        };
        assert.equal(audio.makeLimiter({ createDynamicsCompressor: () => compressor }), compressor);
        assert.equal(compressor.threshold.value, -6);
        assert.equal(compressor.ratio.value, 20);
        const normalized = audio.makeNormalizer(
            { createGain: () => ({ gain: { value: 1 } }) },
            { frequencyBinCount: 2, getByteTimeDomainData: (buffer) => buffer.set([128, 144]) },
        );
        normalized.tick();
        assert.ok(normalized.gain.gain.value > 1);
    });

    await t.test("member volume retains the saved value when persistence fails", async (t) => {
        let persisted = { user_volumes: { mia: 100 } };
        let failure = "";
        replaceGlobal(t, "window", {
            __noxa: { state: { settings: persisted } },
            go: { main: { App: {
                SaveSettings: async (value) => { if (!failure) persisted = value; return failure; },
                GetSettings: async () => persisted,
            } } },
        });
        const audio = await import("../src/audio.js");
        await audio.setUserVolume("mia", 75);
        assert.equal(audio.getUserVolume("mia"), .75);
        failure = "disk unavailable";
        await assert.rejects(audio.setUserVolume("mia", 90), /disk unavailable/);
        assert.equal(audio.getUserVolume("mia"), .75);
    });

    await t.test("chat parses messages, subscriptions, mentions, replies, and switcher scores", async (t) => {
        replaceGlobal(t, "window", { __noxa: { state: { myNickname: "Dan" } } });
        t.mock.module(new URL("../src/sounds.js", import.meta.url), {
            exports: { playEvent() {} },
        });
        const chat = await import("../src/chat-ui.js");

        assert.deepEqual(chat.normalizeSubscriptionState('{"channel_ids":[4,"2",4,0,-1,"bad"]}'), [2, 4]);
        assert.equal(chat.normalizeSubscriptionState("not json"), null);
        assert.deepEqual(chat.normalize({
            body: "hello", enc_verified: true, from_nickname: "Ada", id: "8", reply_to_id: "3", sent_at: 7,
        }, 5), {
            channelID: 5, clientMsgID: "", deleted: false, edited: false, e2e: false, direct: false, enc: true, encVerified: true, encMode: "", from: "Ada", fromUID: "",
            id: 8, mentioned: false, mentions: [], role_mentions: [], offline: false, reactions: null, replyToID: 3, self: false, text: "hello", ts: 7000, version: 1,
        });
        const plaintextDM = chat.normalize({ direct: true, text: "clear" });
        assert.equal(plaintextDM.direct, true);
        assert.equal(plaintextDM.encVerified, false);
        // The encryption mode is local evidence: it counts only next to the
        // native layer's verification flag.
        assert.equal(chat.normalize({ text: "x", enc_verified: true, enc_mode: "server" }).encMode, "server");
        assert.equal(chat.normalize({ direct: true, text: "x", enc_verified: true, enc_mode: "e2e" }).encMode, "e2e");
        assert.equal(chat.normalize({ text: "x", enc_mode: "e2e" }).encMode, "");
        assert.equal(chat.normalize({ text: "x", enc_verified: true, enc_mode: "bogus" }).encMode, "");
        const legacyDM = chat.normalize({ e2e: true, text: "legacy" });
        assert.equal(legacyDM.direct, true);
        assert.equal(legacyDM.encVerified, false);
        assert.equal(chat.mentionsMe("hello @dan and @here"), true);
        assert.equal(chat.mentionsMe("hello @daniela"), false);
        const first = { from: "Ada", id: 1, ts: 10 };
        const reply = { from: "Bob", id: 2, replyToID: 1, text: "plain", ts: 20 };
        const legacyReply = { from: "Bob", id: 3, replyToID: 0, text: "↪ Ada: earlier", ts: 30 };
        assert.equal(chat.resolveParent([first, reply, legacyReply], reply), first);
        assert.equal(chat.resolveParent([first, reply, legacyReply], legacyReply), null);
        assert.equal(chat.fmtSlowMode(3600), "1h");
        assert.equal(chat.fmtSlowMode(120), "2m");
        assert.equal(chat.fmtSlowMode(7), "7s");
        assert.equal(chat.qsScore("alp", "Alpha"), 100);
        assert.ok(chat.qsScore("apa", "Alpha") > 0);
        assert.equal(chat.qsScore("zzz", "Alpha"), -1);
    });

    // P3-7: a locked placeholder's sender/peer/conversation come from the
    // unauthenticated manifest: it is labelled neutrally, never names the
    // claimed sender and never creates a conversation.
    await t.test("chat quarantine placeholders are neutral and never create a conversation", async (t) => {
        const events = [];
        replaceGlobal(t, "window", { __noxa: { state: { myNickname: "Dan" } }, dispatchEvent: event => events.push(event.type) });
        const chat = await import("../src/chat-ui.js");
        const q = { id: "d1", sender: "ceo", code: "invalid", scope: "direct", peer: "mallory", seen_at: 5 };
        const row = chat.quarantinePlaceholder(q);
        assert.equal(row.fromUID, "");
        assert.equal(row.from, "Unverified sender");
        assert.doesNotMatch(JSON.stringify(row), /ceo|mallory/);
        chat.onRatchetQuarantine({ placeholders: [q] });
        assert.equal(chat.directStoreForTest("mallory"), null, "no conversation is created from a placeholder");
        assert.deepEqual(events, ["noxa-ratchet-quarantine"]);
        // An existing conversation with that peer shows the neutral row.
        chat.directStoreForTest("bob", true).msgs.push({ id: 0, clientMsgID: "m1", fromUID: "bob", text: "hi" });
        chat.onRatchetQuarantine({ placeholders: [{ ...q, peer: "bob" }] });
        const shown = chat.directStoreForTest("bob").msgs.filter(m => m.quarantined);
        assert.equal(shown.length, 1);
        assert.equal(shown[0].fromUID, "");
        assert.equal(shown[0].from, "Unverified sender");
        // Malformed or non-direct placeholders are ignored; codes keep their text.
        chat.onRatchetQuarantine({ placeholders: [{ ...q, peer: "bob" }, { ...q, id: "" , peer: "bob" }, { ...q, id: "g1", scope: "group", conversation: "g" },
            { ...q, id: "x1", peer: 7 }, null, { ...q, id: "d2", peer: "bob", code: "fenced_copy", seen_at: undefined }, { ...q, id: "d3", peer: "bob", code: "lost_key", seen_at: 0 }] });
        const locked = chat.directStoreForTest("bob").msgs.filter(m => m.quarantined);
        assert.deepEqual(locked.map(m => m.clientMsgID).sort(), ["quarantine:d1", "quarantine:d2", "quarantine:d3"]);
        assert.equal(locked.find(m => m.clientMsgID === "quarantine:d2").text, chat.quarantineText("fenced_copy"));
        assert.equal(locked.find(m => m.clientMsgID === "quarantine:d3").text, chat.quarantineText("lost_key"));
        assert.ok(locked.every(m => m.ts > 0 && m.fromUID === ""));
        chat.onRatchetQuarantine(null);
        assert.equal(chat.directStoreForTest("bob").msgs.some(m => m.quarantined), false);
        chat.onRatchetQuarantine({ placeholders: [{ ...q, peer: "bob" }] });
        // Gone from the list: the row disappears.
        chat.onRatchetQuarantine({ placeholders: [] });
        assert.equal(chat.directStoreForTest("bob").msgs.some(m => m.quarantined), false);
    });

    await t.test("chat notifications never carry attachment keys or unopened bodies", async (t) => {
        replaceGlobal(t, "window", { __noxa: { state: { myNickname: "Dan" } } });
        const chat = await import("../src/chat-ui.js");
        const channel = (text, extra = {}) => chat.normalize({ text, from_nickname: "Ada", enc_verified: true, enc_mode: "server", ...extra }, 4);
        const file = chat.notificationLine(channel("[file:store#secret-key#nx1;size=4#plan.pdf]"));
        assert.equal(file, "Ada: File · plan.pdf");
        assert.doesNotMatch(file, /secret-key|store#/);
        assert.equal(chat.notificationLine(channel("see [file:store#secret-key#plan.pdf] now")), "Ada: see 📎 plan.pdf now");
        const sealed = chat.notificationLine(channel("[encrypted message — key unavailable]"));
        assert.doesNotMatch(sealed, /\[encrypted message/);
        assert.match(sealed, /^Ada: This message is encrypted\./);
        assert.match(chat.notificationLine(chat.normalize({ text: "in the clear", from_nickname: "Ada" }, 4)), /^Ada: This message was blocked/);
        assert.equal(chat.notificationLine(channel("x".repeat(200))).length <= "Ada: ".length + 80, true);
        // addChat builds keyword, mention and channel notifications only from it.
        const source = await (await import("node:fs/promises")).readFile(new URL("../src/chat-ui.js", import.meta.url), "utf8");
        const addChat = source.slice(source.indexOf("export function addChat("), source.indexOf("function pushMsg("));
        assert.doesNotMatch(addChat, /m\.text \|\| ""\)\.slice/);
        assert.equal(addChat.match(/notificationLine\(m\)/g)?.length, 3);
    });

    await t.test("chat keyword highlights match only sanitized message text", async (t) => {
        replaceGlobal(t, "window", { __noxa: { state: { myNickname: "Dan" } } });
        const chat = await import("../src/chat-ui.js");
        const channel = (text, extra = {}) => chat.normalize({ text, from_nickname: "Ada", enc_verified: true, enc_mode: "server", ...extra }, 4);
        const token = channel("[file:raid#secretkey#nx1;size=4#plan.pdf]");
        assert.equal(chat.keywordText(token), "plan.pdf");
        assert.doesNotMatch(chat.keywordText(token), /raid|secretkey|nx1/);
        assert.equal(chat.keywordText(channel("see\n[file:raid#secretkey#plan.pdf]  now")), "see plan.pdf now");
        const long = "x ".repeat(60) + "raid tonight";
        assert.equal(chat.keywordText(channel(long)), long.trim(), "no preview truncation");
        assert.equal(chat.keywordText(channel("[encrypted message — key unavailable]")), "");
        assert.equal(chat.keywordText(chat.normalize({ text: "raid in the clear", from_nickname: "Ada" }, 4)), "");
        assert.equal(chat.keywordText({ ...channel("raid"), deleted: true }), "");
        // With the real matcher: a keyword inside the token or key never matches.
        window.__noxa.state.lastConnect = { addr: "srv" };
        window.__noxa.state.settings = { keywords: { srv: ["raid", "secretkey", "plan"] } };
        const { matchKeyword } = await import("../src/notifications.js");
        assert.equal(matchKeyword(chat.keywordText(token)), "plan");
        window.__noxa.state.settings.keywords.srv = ["raid", "secretkey"];
        assert.equal(matchKeyword(chat.keywordText(token)), "");
        assert.equal(matchKeyword(chat.keywordText(channel(long))), "raid");
        const source = await (await import("node:fs/promises")).readFile(new URL("../src/chat-ui.js", import.meta.url), "utf8");
        assert.match(source, /matchKeyword\(keywordText\(m\)\)/);
        assert.doesNotMatch(source, /matchKeyword\(m\.text/);
    });

    await t.test("chat keeps a downgraded DM unverified live, after reload and in search", async (t) => {
        replaceGlobal(t, "window", { __noxa: { state: { myNickname: "Dan" } } });
        const chat = await import("../src/chat-ui.js");
        const { encryptionBadge } = await import("../src/message-encryption.js");
        const { conversationEncryption } = await import("../src/dm-model.js");
        const live = chat.normalize({ direct: true, text: "raid at 8", from: "Mia", from_unique_id: "u-mia", enc_mode: "downgraded" });
        assert.equal(live.encVerified, false);
        assert.equal(live.encMode, "downgraded");
        assert.deepEqual(encryptionBadge(live), { kind: "warning", label: "chat.downgradedHelp", bodyKey: "" });
        // The body is shown (it was opened with the server key), never as verified.
        assert.equal(chat.notificationLine(live), "Mia: raid at 8");
        assert.equal(chat.keywordText(live), "");
        assert.equal(conversationEncryption({ last: live }).kind, "warning");
        assert.equal(conversationEncryption({ last: live }).helpKey, "chat.downgradedHelp");
        // A forged verified pair never becomes downgraded-verified or verified-e2e.
        assert.equal(chat.normalize({ direct: true, text: "x", enc_verified: true, enc_mode: "downgraded" }).encMode, "");
        // Reload from the local log draws the same badge.
        const stored = chat.directMessageFromEntry({ from_unique_id: "u-mia", from_nickname: "Mia", body: "raid at 8", sent_at: 5, enc_mode: "downgraded" });
        assert.equal(stored.encVerified, false);
        assert.equal(stored.encMode, "downgraded");
        assert.equal(encryptionBadge(stored).label, "chat.downgradedHelp");
        // A record verified as "server" (the retired fallback) reloads as
        // downgraded, never verified; legacy verified records stay e2e.
        const retired = chat.directMessageFromEntry({ from_unique_id: "u-mia", body: "old", sent_at: 6, enc_verified: true, enc_mode: "server" });
        assert.deepEqual([retired.encVerified, retired.enc, retired.encMode, encryptionBadge(retired).kind], [false, false, "downgraded", "warning"]);
        const legacy = chat.directMessageFromEntry({ from_unique_id: "u-mia", body: "old", sent_at: 6, enc_verified: true });
        // A verified legacy (static-key) record gets the legacy badge, never the Olm lock.
        assert.deepEqual([legacy.encVerified, legacy.encMode, encryptionBadge(legacy).kind], [true, "e2e", "legacy"]);
        const bogus = chat.directMessageFromEntry({ from_unique_id: "u-mia", body: "old", sent_at: 6, enc_verified: true, enc_mode: "bogus" });
        assert.deepEqual([bogus.encVerified, bogus.encMode, encryptionBadge(bogus).label], [false, "", "chat.unverifiedHelp"]);
        // A live DM in "server" mode is a warning too; channels keep the shield.
        assert.equal(encryptionBadge(chat.normalize({ direct: true, text: "x", enc_verified: true, enc_mode: "server" })).kind, "warning");
        assert.equal(encryptionBadge(chat.normalize({ text: "x", enc_verified: true, enc_mode: "server" }, 4)).kind, "shield");
        const source = await (await import("node:fs/promises")).readFile(new URL("../src/chat-ui.js", import.meta.url), "utf8");
        // The local log keeps the mode, and search hits map it the same way.
        assert.match(source, /enc_mode: m\.encVerified \? m\.encMode : encModeFrom\(false, m\.encMode\)/);
        assert.equal(chat.searchEncryptionBadge({ body: "raid", enc_mode: "downgraded" }, true).label, "chat.downgradedHelp");
        assert.equal(chat.searchEncryptionBadge({ body: "raid", enc_verified: true, enc_mode: "server" }, true).kind, "warning");
        assert.equal(chat.searchEncryptionBadge({ body: "raid", enc_verified: true, enc_mode: "e2e" }, true).kind, "legacy");
        assert.equal(chat.searchEncryptionBadge({ body: "raid", enc_verified: true, enc_mode: "e2e", protocol: "noxa-olm-v1" }, true).kind, "lock");
    });

    await t.test("channel edit offers no new password on a text channel", async (t) => {
        replaceGlobal(t, "window", { __noxa: { state: { channels: [{ ChannelID: 3, TextOnly: true }, { ChannelID: 4 }] } } });
        const { editingTextChannel } = await import("../src/channel-lifecycle-ui.js");
        assert.equal(editingTextChannel(3), true);
        assert.equal(editingTextChannel(4), false);
        assert.equal(editingTextChannel(9), false);
        assert.equal(editingTextChannel(3, []), false);
        const source = await (await import("node:fs/promises")).readFile(new URL("../src/channel-lifecycle-ui.js", import.meta.url), "utf8");
        // "set" is disabled for a text channel; "remove" stays available.
        assert.match(source, /\["set", t\("roles\.channel\.passwordSet"\), textChannel\]/);
        assert.match(source, /\["remove", t\("roles\.channel\.passwordRemove"\)\]/);
        assert.match(source, /passwordAction === "set" && !textChannel\) field\("password"/);
    });

    await t.test("chat replaces an unopened copy with a later verified copy", async (t) => {
        const changed = [];
        replaceGlobal(t, "window", { __noxa: { state: { myNickname: "Dan" } } });
        window.addEventListener("noxa-direct-changed", () => changed.push(true));
        const chat = await import("../src/chat-ui.js");
        const placeholder = { text: "[encrypted message — key unavailable]", direct: true, enc: false, encVerified: false, encMode: "" };
        assert.equal(chat.upgradeEncryption(placeholder, { text: "hello", encVerified: false }), false);
        assert.equal(placeholder.text, "[encrypted message — key unavailable]");
        assert.equal(chat.upgradeEncryption(placeholder, { text: "[encrypted message — decryption failed]", encVerified: true, encMode: "e2e" }), false);
        assert.equal(chat.upgradeEncryption(placeholder, { text: "hello", encVerified: true, encMode: "e2e", e2e: true }), true);
        assert.deepEqual({ text: placeholder.text, enc: placeholder.enc, encVerified: placeholder.encVerified, encMode: placeholder.encMode },
            { text: "hello", enc: true, encVerified: true, encMode: "e2e" });
        assert.equal(changed.length, 1);
        // A verified body is never replaced; an equal body only gains evidence.
        assert.equal(chat.upgradeEncryption(placeholder, { text: "other", encVerified: true, encMode: "e2e" }), false);
        assert.equal(placeholder.text, "hello");
        const weak = { text: "same", enc: false, encVerified: false, localUnverified: true };
        assert.equal(chat.upgradeEncryption(weak, { text: "same", encVerified: true, encMode: "server" }), true);
        assert.equal(weak.encVerified, true);
        assert.equal(weak.localUnverified, false);
        assert.equal(chat.upgradeEncryption({ text: "a", encVerified: false }, { text: "b", encVerified: true }), false);
        assert.equal(chat.upgradeEncryption({ text: "[encrypted message — key unavailable]", deleted: true }, { text: "b", encVerified: true }), false);
    });

    await t.test("chat log trimming counts message rows, not day separators", async (t) => {
        replaceGlobal(t, "window", { __noxa: { state: {} } });
        const chat = await import("../src/chat-ui.js");
        const log = { children: [] };
        Object.defineProperty(log, "firstElementChild", { get() { return this.children[0] || null; } });
        const child = (name, separator = false) => {
            const item = { name, classList: { contains: cls => separator && cls === "dm-day" } };
            item.remove = () => { const i = log.children.indexOf(item); if (i >= 0) log.children.splice(i, 1); };
            Object.defineProperty(item, "nextElementSibling", { get() { return log.children[log.children.indexOf(item) + 1] || null; } });
            return item;
        };
        log.children.push(child("dayA", true), child("a1"), child("a2"), child("dayB", true), child("b1"), child("b2"));
        const names = () => log.children.map(item => item.name);
        chat.trimLog(log, 4); // four messages fit; the two separators do not count
        assert.deepEqual(names(), ["dayA", "a1", "a2", "dayB", "b1", "b2"]);
        chat.trimLog(log, 3);
        assert.deepEqual(names(), ["dayA", "a2", "dayB", "b1", "b2"]);
        chat.trimLog(log, 2); // dayA loses its last message and goes with it
        assert.deepEqual(log.children.map(item => item.name), ["dayB", "b1", "b2"]);
        chat.trimLog(log, 5);
        assert.deepEqual(log.children.map(item => item.name), ["dayB", "b1", "b2"]);
    });

    await t.test("chat connection setup keeps best-effort failures and late replies scoped", async (t) => {
        replaceGlobal(t, "document", { getElementById: () => null });
        const state = { activeTabID: "a", serverGeneration: 1, myUniqueID: "initial" };
        const notices = [], reads = [];
        let delayed = "", release;
        const app = {
            DMHistoryContextForTab: async tab => {
                if (delayed === "identity") return new Promise(resolve => { release = () => resolve({ tab_id: tab, identity_uid: "old", activation: "0", identity_revision: "0" }); });
                throw new Error("identity unavailable");
            },
            DMHistoryPeersForContext: async () => [],
            SubscriptionsForTab: async tabID => {
                reads.push(["subscriptions", tabID]);
                if (delayed === "subscriptions") return new Promise(resolve => { release = () => resolve([2]); });
                throw new Error("subscriptions unavailable");
            },
            MOTDForTab: async tabID => {
                reads.push(["motd", tabID]);
                if (delayed === "motd") return new Promise(resolve => { release = () => resolve("old notice"); });
                return "current notice";
            },
        };
        replaceGlobal(t, "window", { __noxa: { state, sysMsg: text => notices.push(text) }, go: { main: { App: app } } });
        const chat = await import("../src/chat-ui.js");
        await chat.onConnect();
        assert.deepEqual(reads, [["subscriptions", "a"], ["motd", "a"]]);
        assert.deepEqual(notices, ["server notice — current notice"]);
        for (const stage of ["identity", "subscriptions", "motd"]) {
            state.serverGeneration++;
            delayed = stage;
            release = null;
            reads.length = 0;
            notices.length = 0;
            const connecting = chat.onConnect();
            for (let i = 0; i < 10 && !release; i++) await Promise.resolve();
            assert.equal(typeof release, "function", stage + " reached its delayed read");
            state.serverGeneration++;
            state.myUniqueID = "replacement";
            release();
            await connecting;
            assert.equal(state.myUniqueID, "replacement");
            assert.deepEqual(notices, []);
            if (stage !== "motd") assert.equal(reads.some(([name]) => name === "motd"), false);
        }
    });

    await t.test("chat send preserves failed and replacement drafts and serializes native writes", async (t) => {
        const state = { activeTabID: "a", serverGeneration: 1, myChannelID: 7 };
        const nodes = new Map(["chat-text", "chat-send", "chat-scope", "file-preview-row", "reply-bar", "chat-send-error", "chat-retry"].map(id => [id, { ...element(), value: "" }]));
        const errorText = element();
        nodes.get("chat-send-error").querySelector = selector => selector === "span" ? errorText : null;
        nodes.get("chat-scope").value = "channel";
        const input = nodes.get("chat-text"), button = nodes.get("chat-send");
        const notices = [], calls = [];
        let resolve;
        const app = { SendChatForTab: (...args) => {
            calls.push(args);
            return new Promise(done => { resolve = done; });
        } };
        replaceGlobal(t, "window", { __noxa: { state, sysMsg: message => notices.push(message) }, go: { main: { App: app } } });
        replaceGlobal(t, "document", { getElementById: id => nodes.get(id) });
        const chat = await import("../src/chat-ui.js");
        input.value = "original";
        const failed = chat.sendMessage();
        assert.equal(button.disabled, true);
        await chat.sendMessage();
        assert.deepEqual(calls, [["a", "channel", "7", "original"]]);
        resolve("write failed");
        await failed;
        assert.equal(input.value, "original");
        assert.equal(button.disabled, false);
        assert.deepEqual(notices, []);
        assert.equal(errorText.textContent, "Message not sent: write failed");
        assert.equal(nodes.get("chat-retry").hidden, false);
        notices.length = 0;
        const stale = chat.sendMessage();
        state.serverGeneration++;
        input.value = "replacement";
        resolve("");
        await stale;
        assert.equal(input.value, "replacement");
        assert.deepEqual(notices, []);
        const current = chat.sendMessage();
        resolve("");
        await current;
        assert.equal(input.value, "");
        assert.equal(button.disabled, false);
        assert.equal(calls.length, 3);
    });

    await t.test("files switches both workspace tabs, restores focus, and protects sealed data", async (t) => {
        const tabChat = element();
        const tabFiles = element();
        const chatPane = element();
        const { pane: filesPane, list } = fileBrowserPane();
        const voiceMute = Object.assign(element(), {
            closest: () => null,
            getClientRects: () => [{}],
            isConnected: true,
        });
        const elements = new Map([
            ["tab-chat", tabChat],
            ["tab-files", tabFiles],
            ["chat-pane", chatPane],
            ["files-pane", filesPane],
            ["voice-mute", voiceMute],
        ]);
        replaceGlobal(t, "window", {
            __noxa: { state: { channels: [], myChannelID: 0 } },
            getComputedStyle: () => ({ display: "block", visibility: "visible" }),
        });
        replaceGlobal(t, "document", {
            activeElement: null,
            body: {},
            createElement: () => element(),
            createTextNode: (text) => ({ text }),
            getElementById: (id) => elements.get(id) || null,
        });
        const files = await import("../src/files-ui.js");

        assert.equal(files.activateWorkspaceTab("unknown"), false);
        assert.equal(files.activateWorkspaceTab("chat", { focus: true }), true);
        assert.equal(tabChat.classList.contains("active"), true);
        assert.equal(tabFiles.classList.contains("active"), false);
        assert.equal(tabChat.getAttribute("aria-selected"), "true");
        assert.equal(tabFiles.getAttribute("aria-selected"), "false");
        assert.equal(chatPane.hidden, false);
        assert.equal(filesPane.hidden, true);
        assert.equal(tabChat.focusCalls, 1);
        assert.equal(files.activateWorkspaceTab("files", { focus: true }), true);
        assert.equal(tabChat.getAttribute("aria-selected"), "false");
        assert.equal(tabFiles.getAttribute("aria-selected"), "true");
        assert.equal(chatPane.hidden, true);
        assert.equal(filesPane.hidden, false);
        assert.equal(tabFiles.focusCalls, 1);
        assert.match(list.innerHTML, /Join a channel/);
        assert.equal(files.restoreVisibleWorkspaceFocus(), true);
        assert.equal(voiceMute.focusCalls, 1);
        assert.deepEqual(voiceMute.focusOptions, { preventScroll: true });
        document.activeElement = voiceMute;
        assert.equal(files.restoreVisibleWorkspaceFocus(), false);
        assert.equal(files.isChatAttachment({ encrypted: true, name: "plain.txt" }), true);
        assert.equal(files.isChatAttachment({ encrypted: false, name: "sealed.vcx" }), true);
        assert.equal(files.isChatAttachment({ name: "plain.txt" }), false);
        assert.equal(files.bytesToBase64(new Uint8Array([0, 255, 1])), "AP8B");
        files.activateWorkspaceTab("chat");
    });

    await t.test("grid compositor rejects unavailable canvases and composes empty through four streams", async (t) => {
        replaceGlobal(t, "OffscreenCanvas", undefined);
        const { GridCompositor } = await import("../src/grid-compositor.js");
        assert.throws(() => new GridCompositor(), /unavailable/);

        class TestCanvas {
            constructor(width, height) {
                this.width = width;
                this.height = height;
                this.draws = [];
                this.ctx = {
                    drawImage: (...args) => this.draws.push(args),
                    fillRect: (...args) => { this.fill = args; },
                    fillStyle: "",
                };
            }

            getContext(kind) { return kind === "2d" ? this.ctx : null; }
            transferToImageBitmap() { return { draws: this.draws, fill: this.fill }; }
        }
        replaceGlobal(t, "OffscreenCanvas", TestCanvas);
        assert.deepEqual(await new GridCompositor(200, 200).compose([]), { draws: [], fill: [0, 0, 200, 200] });
        const first = { readyState: 2 };
        assert.deepEqual((await new GridCompositor(200, 200).compose([first])).draws, [[first, 0, 0, 200, 200]]);
        const videos = [{ readyState: 2 }, { readyState: 2 }, { readyState: 2 }, { readyState: 2 }];
        assert.deepEqual((await new GridCompositor(200, 200).compose(videos)).draws, [
            [videos[0], 0, 0, 100, 100], [videos[1], 100, 0, 100, 100],
            [videos[2], 0, 100, 100, 100], [videos[3], 100, 100, 100, 100],
        ]);
    });

    await t.test("grid compositor closes WebGL bitmaps and skips decode errors", async (t) => {
        const calls = { close: 0, draw: 0, flush: 0, viewport: [] };
        const gl = {
            ARRAY_BUFFER: 1, CLAMP_TO_EDGE: 2, COLOR_BUFFER_BIT: 3, COMPILE_STATUS: 4, FLOAT: 5, FRAGMENT_SHADER: 6,
            LINEAR: 7, LINK_STATUS: 8, RGBA: 9, STATIC_DRAW: 10, TEXTURE_2D: 11, TEXTURE_MAG_FILTER: 12,
            TEXTURE_MIN_FILTER: 13, TEXTURE_WRAP_S: 14, TEXTURE_WRAP_T: 15, TRIANGLE_STRIP: 16, UNSIGNED_BYTE: 17, VERTEX_SHADER: 18,
            attachShader() {}, bindBuffer() {}, bindTexture() {}, bufferData() {}, clear() {}, clearColor() {}, compileShader() {},
            createBuffer: () => ({}), createProgram: () => ({}), createShader: () => ({}), createTexture: () => ({}),
            drawArrays() { calls.draw++; }, enableVertexAttribArray() {}, flush() { calls.flush++; }, getAttribLocation: () => 0,
            getProgramParameter: () => true, getShaderParameter: () => true, linkProgram() {}, shaderSource() {}, texImage2D() {}, texParameteri() {},
            useProgram() {}, vertexAttribPointer() {}, viewport: (...args) => calls.viewport.push(args),
        };
        class WebGLCanvas {
            constructor(width, height) { this.width = width; this.height = height; }
            getContext(kind) { return kind === "webgl" ? gl : null; }
            transferToImageBitmap() { return { composed: true }; }
        }
        replaceGlobal(t, "OffscreenCanvas", WebGLCanvas);
        replaceGlobal(t, "createImageBitmap", async (video) => {
            if (video.bad) throw new Error("decode failed");
            return { close: () => { calls.close++; } };
        });
        const { GridCompositor } = await import("../src/grid-compositor.js");
        assert.deepEqual(await new GridCompositor(400, 200).compose([{ readyState: 2 }, { bad: true, readyState: 2 }]), { composed: true });
        assert.equal(calls.draw, 1);
        assert.equal(calls.close, 1);
        assert.equal(calls.flush, 1);
        assert.deepEqual(calls.viewport, [[0, 0, 200, 200]]);
    });

    await t.test("image pickers cancel, enforce limits, scale icons, and release object URLs", async (t) => {
        let selected = null;
        let canvas;
        const toasts = [];
        replaceGlobal(t, "document", {
            createElement(tag) {
                if (tag === "input") {
                    return {
                        click() { this.onchange?.(); },
                        files: selected ? [selected] : [],
                    };
                }
                if (tag === "canvas") {
                    canvas = {
                        draw: [], height: 0, width: 0,
                        getContext: () => ({ drawImage: (...args) => canvas.draw.push(args), fillRect() {}, fillStyle: "" }),
                        toDataURL: () => "data:image/jpeg;base64,YQ==",
                    };
                    return canvas;
                }
                return element();
            },
        });
        replaceGlobal(t, "window", {
            __noxa: { state: { serverGeneration: 7 }, toast: (...args) => toasts.push(args) },
        });
        const imageTools = await import("../src/image-tools.js");

        assert.equal(await imageTools.pickAvatar(), null);
        selected = { size: 9 * 1024 * 1024, type: "image/png" };
        assert.equal(await imageTools.pickAvatar(), null);
        assert.match(toasts.at(-1)[0], /8 MiB/);
        selected = { size: 300 * 1024, type: "image/gif" };
        assert.equal(await imageTools.pickAvatar(), null);
        assert.match(toasts.at(-1)[0], /animated image too large/);

        const revoked = [];
        replaceGlobal(t, "URL", {
            createObjectURL: () => "blob:icon",
            revokeObjectURL: (url) => revoked.push(url),
        });
        replaceGlobal(t, "Image", class {
            constructor() { this.height = 1000; this.width = 2000; }
            set src(value) { this.value = value; this.onload?.(); }
        });
        selected = { size: 1024, type: "image/png" };
        assert.deepEqual(await imageTools.pickIcon(1000, 0.85), { contentType: "image/jpeg", dataBase64: "YQ==" });
        assert.equal(canvas.width, 1000);
        assert.equal(canvas.height, 500);
        assert.deepEqual(canvas.draw[0].slice(1), [0, 0, 1000, 500]);
        assert.deepEqual(revoked, ["blob:icon"]);
        assert.equal(imageTools.generationCurrent({}), true);
        assert.equal(imageTools.generationCurrent({ serverGeneration: 7 }), true);
        assert.equal(imageTools.generationCurrent({ serverGeneration: 6 }), false);
        assert.equal(imageTools.base64Bytes("YQ=="), 1);
        assert.equal(imageTools.base64Bytes("YWI="), 2);
        assert.equal(imageTools.base64Bytes("YWJj"), 3);
    });

    await t.test("client info derives audio stats and durations", async (t) => {
        replaceGlobal(t, "window", {
            __noxa: {
                state: {
                    channels: [
                        { ChannelID: 1 }, { ChannelID: 2, ParentID: 1 }, { ChannelID: 3, ParentID: 2 }, { ChannelID: 4 },
                    ],
                },
            },
        });
        const clientInfo = await import("../src/clientinfo.js");

        const direct = { kind: "audio", trackIdentifier: "42", type: "inbound-rtp" };
        const legacyTrack = { id: "track-a", trackIdentifier: "99" };
        const legacyInbound = { kind: "audio", trackId: "track-a", type: "inbound-rtp" };
        const video = { kind: "video", trackIdentifier: "no", type: "inbound-rtp" };
        const mapped = clientInfo.inboundAudioByPublisher([direct, legacyTrack, legacyInbound, video]);
        assert.equal(mapped.get("42"), direct);
        assert.equal(mapped.get("99"), legacyInbound);
        // Chromium can retain the track ID generated before a publisher
        // joined. The current SDP's MID/MSID identifies whose audio it is.
        const received = { id: "voice", type: "inbound-rtp", kind: "audio", mid: "0", trackIdentifier: "browser-generated-id" };
        const screen = { id: "share", type: "inbound-rtp", kind: "audio", mid: "1", trackIdentifier: "another-generated-id" };
        const sdp = "v=0\r\nm=audio 9 UDP/TLS/RTP/SAVPF 111\r\na=mid:0\r\na=msid:noxa-member member\r\nm=audio 9 UDP/TLS/RTP/SAVPF 111\r\na=mid:1\r\na=msid:noxa-member|screenaudio member|screenaudio\r\n";
        const negotiated = clientInfo.inboundAudioByPublisher([received, screen], sdp);
        assert.equal(negotiated.get("member"), received);
        assert.equal(negotiated.size, 1);
        assert.equal(clientInfo.inboundAudioByPublisher([received], sdp.replace("a=msid:noxa-member member", "a=msid:noxa-replacement replacement")).has("member"), false);
        assert.equal(clientInfo.inboundAudioByPublisher([received], sdp.replace("m=audio 9", "m=audio 0")).size, 0);
        assert.equal(clientInfo.humanBytes(0), "0 B");
        assert.equal(clientInfo.humanBytes(1024), "1.0 KiB");
        assert.equal(clientInfo.humanBytes(1024 * 1024), "1.0 MiB");
        assert.equal(clientInfo.humanDuration(65), "1m 5s");
        assert.equal(clientInfo.humanDuration(3661), "1h 1m 1s");
    });

    await t.test("open profile role chips follow the current session and clear after revocation", async (t) => {
        const role = { id: 20, name: "Helper <img src=x>", icon: "★", position: 1, color: "#abcdef" };
        const client = { client_id: "member", unique_id: "member", roles: [role] };
        const state = { authorizationModel: "roles-v1", clients: [client] };
        replaceGlobal(t, "window", { __noxa: { state } });
        replaceGlobal(t, "document", {
            createElement: () => ({ style: {}, textContent: "", setAttribute() {}, append(...nodes) { this.textContent += nodes.map(n => n.textContent).join(""); } }),
            createTextNode: text => ({ textContent: text }),
        });
        const label = {}, chips = { children: [], replaceChildren() { this.children = []; }, append(...items) { this.children.push(...items); } };
        const row = { querySelector: (selector) => selector === ".ci-label" ? label : chips };
        const { renderClientRoles } = await import("../src/clientinfo.js");
        renderClientRoles(row, client);
        assert.equal(label.textContent, "Roles");
        assert.equal(row.hidden, false);
        assert.equal(chips.children[0].textContent, "★ Helper <img src=x>");
        assert.equal(chips.children[0].style.color, "#abcdef");
        state.clients = [{ ...client, roles: [] }];
        renderClientRoles(row, client);
        assert.equal(row.hidden, true);
        assert.equal(chips.children.length, 0);
        state.clients = [{ ...client, client_id: "another-session" }];
        renderClientRoles(row, client);
        assert.equal(row.hidden, true);
        state.clients = [{ ...client, roles: [{ ...role, name: "Member" }] }];
        renderClientRoles(row, client);
        assert.equal(label.textContent, "Roles");
        assert.equal(chips.children[0].textContent, "★ Member");
    });

});
