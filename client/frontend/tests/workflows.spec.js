import { expect, test } from "@playwright/test";

test.describe("persistent polls", () => {
    test.beforeEach(async ({ page }) => {
        await page.evaluate(() => {
            const v = window.__noxa;
            v.showWorkspace(false);
            Object.assign(v.state, { activeTabID: "server-a", myChannelID: 1, myUniqueID: "self", channels: [{ ChannelID: 1, Name: "Lobby" }] });
            window.__pollCalls = [];
            window.__pollState = { message_id: 9100, counts: [0, 0], choices: [], total_voters: 0, closed: false, version: 1, closes_at: Math.floor(Date.now() / 1000) + 3600 };
            const app = window.go.main.App;
            window.go.main.App = new Proxy(app, { get(target, key) {
                if (key === "CreatePollForTab") return async (...args) => { window.__pollCalls.push(args); return ""; };
                if (key !== "PollForTab") return target[key];
                return async (tab, request) => {
                    window.__pollCalls.push([tab, structuredClone(request)]);
                    if (window.__pollGate) await window.__pollGate;
                    const saved = window.__pollState;
                    if (request.action === "vote") {
                        saved.choices = request.choices;
                        saved.counts = [0, 1].map(index => request.choices.includes(index) ? 1 : 0);
                        saved.total_voters = request.choices.length ? 1 : 0;
                        saved.version++;
                    }
                    if (request.action === "close") { saved.closed = true; saved.version++; }
                    return { ...structuredClone(saved), action: request.action };
                };
            }});
        });
    });
    async function postPoll(page) {
        await page.evaluate(() => window.__noxaChat.addChat({ id: 9100, channel_id: 1, from_unique_id: "other", from: "Bob", text: '[noxa-poll:v1]' + JSON.stringify({ question: "Choose <script>", options: ["Forest", "Harbor"], multiple: false, closes_at: window.__pollState.closes_at }) }));
        await expect(page.locator(".poll-status")).toContainText("0 voters");
    }
    test("single ballots replace, clear, persist after rerender and close", async ({ page }) => {
        await postPoll(page);
        const forest = page.locator(".poll-option").filter({ hasText: "Forest" });
        const harbor = page.locator(".poll-option").filter({ hasText: "Harbor" });
        await forest.click();
        await expect(forest).toHaveAttribute("aria-pressed", "true");
        await harbor.click();
        await expect(forest).toHaveAttribute("aria-pressed", "false");
        await expect(harbor).toHaveAttribute("aria-pressed", "true");
        await harbor.click();
        await expect(page.locator(".poll-status")).toContainText("0 voters");
        await forest.click();
        await page.getByRole("button", { name: "Refresh results", exact: true }).click();
        await expect(forest).toHaveAttribute("aria-pressed", "true");
        await page.getByRole("button", { name: "Close poll", exact: true }).click();
        await expect(page.locator(".poll-status")).toContainText("Closed");
        await expect(forest).toBeDisabled();
        await expect(page.locator(".poll-question")).toHaveText("Choose <script>");
        await expect(page.locator(".chat-poll script")).toHaveCount(0);
    });
    test("creation validates options and binds the saved poll to its original server", async ({ page }) => {
        await page.getByRole("button", { name: "Create poll", exact: true }).click();
        await page.getByLabel("Question", { exact: true }).fill("Next map?");
        await page.getByLabel("Options — one per line (2–10)", { exact: true }).fill("Forest\nforest");
        await page.locator(".poll-dialog").getByRole("button", { name: "Create poll", exact: true }).click();
        await expect(page.locator(".poll-dialog [role=alert]")).toContainText("different");
        expect(await page.evaluate(() => window.__pollCalls.length)).toBe(0);
        await page.getByLabel("Options — one per line (2–10)", { exact: true }).fill("Forest\nHarbor");
        await page.locator(".poll-dialog").getByRole("button", { name: "Create poll", exact: true }).click();
        await expect(page.locator(".poll-dialog")).toHaveCount(0);
        const call = await page.evaluate(() => window.__pollCalls[0]);
        expect(call.slice(0, 3)).toEqual(["server-a", "channel", "1"]);
        expect(call[3]).toMatchObject({ question: "Next map?", options: ["Forest", "Harbor"], multiple: false });
    });
    test("a late vote response cannot update another server", async ({ page }) => {
        await postPoll(page);
        await page.evaluate(() => { window.__pollGate = new Promise(resolve => { window.__finishPoll = resolve; }); });
        await page.locator(".poll-option").first().click();
        await page.evaluate(() => {
            window.__noxa.state.activeTabID = "server-b";
            window.__noxa.state.serverGeneration++;
            window.__finishPoll();
        });
        await expect(page.locator(".poll-option").first()).toHaveAttribute("aria-pressed", "false");
        expect(await page.evaluate(() => window.__pollCalls.every(call => call[0] === "server-a"))).toBe(true);
    });
});

test("custom role mentions complete stable IDs and render safe role names", async ({ page }) => {
    await page.evaluate(() => {
        const v = window.__noxa;
        v.showWorkspace(false);
        Object.assign(v.state, { myChannelID: 1, myUniqueID: "self", channels: [{ ChannelID: 1, Name: "Lobby" }], clients: [{ client_id: "other", nickname: "Bob", roles: [{ id: 20, name: "Raid <team>" }] }] });
    });
    await page.locator("#chat-text").fill("@Raid");
    await page.locator("#chat-text").press("Tab");
    await expect(page.locator("#chat-text")).toHaveValue("<@&20> ");
    await page.evaluate(() => {
        window.__noxaChat.addChat({ id: 8001, channel_id: 1, from_unique_id: "other", from: "Bob", text: "Hello <@&20>", role_mentions: ["self"] });
        window.__noxaChat.addChat({ id: 8002, channel_id: 1, from_unique_id: "other", from: "Bob", text: "Unresolved @admin <@&20>" });
    });
    await expect(page.locator('#chat-log [data-msg-id="8001"]')).toHaveClass(/mentioned/);
    await expect(page.locator('#chat-log [data-msg-id="8002"]')).not.toHaveClass(/mentioned/);
    await expect(page.locator('#chat-log [data-msg-id="8001"] .mention-tok')).toHaveText("@Raid <team>");
    await expect(page.locator("#chat-log team")).toHaveCount(0);
});

test.describe("own role overview", () => {
    test("role overview uses visible own roles without retired queries", async ({ page }) => {
        await page.evaluate(async () => {
            const v = window.__noxa;
            Object.assign(v.state, { authorizationModel: "roles-v1", myClientID: "self", clients: [{ client_id: "self", roles: [{ id: 1, name: "Member", position: 1 }] }] });
            await v.refreshPermissions();
        });
        expect(await page.evaluate(() => window.__calls.GetPermissions || 0)).toBe(0);
        await expect(page.locator("#perm-area")).toContainText("Member");
        await expect(page.locator("#perm-area .perm-grid")).toHaveCount(0);
    });
    test("own role overview follows current self snapshots", async ({ page }) => {
        await page.evaluate(async () => {
            const v = window.__noxa;
            Object.assign(v.state, { authorizationModel: "roles-v1", myClientID: "self", myChannelID: 0,
                clients: [{ client_id: "self", channel_id: 0, roles: [{ id: 1, name: "Member", position: 1 }] }] });
            await v.refreshPermissions();
            v.state.clients[0].roles = [{ id: 2, name: "Moderator", position: 2 }];
            v.syncOwnChannel({ audible: false });
        });
        await expect(page.locator("#perm-area")).toContainText("Moderator");
        await expect(page.locator("#perm-area")).not.toContainText("Member");
        expect(await page.evaluate(() => window.__calls.GetPermissions || 0)).toBe(0);
    });
});

test.describe("tray reconnect source ownership", () => {
    test("disconnect retires a late fallback guest connection", async ({ page }) => {
        await page.evaluate(() => {
            window.__noxa.state.lastSuccessfulConnect = null;
            window.__noxa.state.settings.bookmarks = [{ name: "Original", addr: "original.example:12333", nickname: "Alice" }];
            window.__guestConnectHandler = () => new Promise(resolve => { window.__finishGuestTray = resolve; });
            for (const cb of window.__events.tray_reconnect || []) cb();
        });
        await expect.poll(() => page.evaluate(() => typeof window.__finishGuestTray)).toBe("function");
        await page.evaluate(async () => {
            await window.__noxa.disconnect();
            window.__finishGuestTray({ tab_id: "obsolete-guest", error: "" });
        });
        await expect.poll(() => page.evaluate(() => window.__callArgs.CloseTab || [])).toEqual([["obsolete-guest"]]);
    });
    for (const automatic of [false, true]) test(`obsolete connection failure cannot disturb replacement with auto retry ${automatic}`, async ({ page }) => {
        await page.evaluate(automatic => {
            const v = window.__noxa;
            v.state.settings.reconnect_on_loss = automatic;
            v.state.lastSuccessfulConnect = { addr: "source.example:12333", nick: "Alice" };
            window.__connectBookmarkGate = new Promise(resolve => { window.__releaseTrayConnect = resolve; });
            window.__connectBookmarkResult = "connection failed";
            for (const cb of window.__events.tray_reconnect || []) cb();
        }, automatic);
        await expect.poll(() => page.evaluate(() => window.__calls.ConnectBookmarkTabWithID || 0)).toBe(1);
        await page.evaluate(() => {
            const v = window.__noxa;
            v.state.activeTabID = "replacement"; v.state.serverGeneration++;
            v.state.lastConnect = { addr: "replacement.example:12333", nick: "Bob" };
            v.showWorkspace(false);
            window.__releaseTrayConnect();
        });
        await expect.poll(() => page.evaluate(() => window.__noxa.state.reconnectInFlight)).toBe(false);
        expect(await page.evaluate(() => window.__noxa.state.reconnectAttempts)).toBe(0);
        await expect(page.locator("#login-overlay")).toBeHidden();
    });
    test("fallback bookmark is frozen before the status read", async ({ page }) => {
        await page.evaluate(() => {
            const v = window.__noxa;
            v.state.lastSuccessfulConnect = null;
            v.state.settings.bookmarks = [{ name: "Original", addr: "original.example:12333", nickname: "Alice" }];
            const app = window.go.main.App;
            window.go.main.App = new Proxy(app, { get(target, key) {
                if (key === "ListTabs") return async () => await new Promise(resolve => { window.__finishTrayLookup = resolve; });
                return target[key];
            } });
            for (const cb of window.__events.tray_reconnect || []) cb();
        });
        await expect.poll(() => page.evaluate(() => typeof window.__finishTrayLookup)).toBe("function");
        await page.evaluate(() => {
            window.__noxa.state.settings.bookmarks[0].addr = "changed.example:12333";
            window.__finishTrayLookup([]);
        });
        await expect.poll(() => page.evaluate(() => window.__calls.ConnectGuestBookmarkTabWithID || 0)).toBe(1);
        expect(await page.evaluate(() => window.__callArgs.ConnectGuestBookmarkTabWithID[0][1])).toBe("original.example:12333");
    });
    for (const scenario of ["native switch", "frontend switch", "disconnect", "read failure", "still offline"]) test(scenario, async ({ page }) => {
        await page.evaluate(scenario => {
            const v = window.__noxa;
            v.state.activeTabID = "source";
            v.state.settings.reconnect_on_loss = false;
            v.state.lastSuccessfulConnect = { addr: "source.example:12333", nick: "Alice", pw: "", spw: "", bookmark: "" };
            document.getElementById("conn-pill").classList.remove("up");
            const app = window.go.main.App;
            window.__trayCheck = { scenario, lookups: [] };
            window.go.main.App = new Proxy(app, { get(target, key) {
                if (!["Connected", "ListTabs"].includes(key)) return target[key];
                return async () => {
                    const f = window.__trayCheck;
                    f.lookups.push(key);
                    if (f.finished) return key === "Connected" ? false : [{ id: "source", active: true, connected: false }];
                    await new Promise(resolve => { f.release = resolve; });
                    f.finished = true;
                    if (scenario === "read failure") throw new Error("status unavailable");
                    return key === "Connected" ? false : [{ id: scenario === "native switch" ? "other" : "source", active: true, connected: false }];
                };
            } });
            for (const cb of window.__events.tray_reconnect || []) cb();
        }, scenario);
        await expect.poll(() => page.evaluate(() => typeof window.__trayCheck.release)).toBe("function");
        await page.evaluate(scenario => {
            const v = window.__noxa;
            if (scenario === "frontend switch") {
                v.state.activeTabID = "other";
                v.state.serverGeneration++;
                v.state.lastSuccessfulConnect = { addr: "other.example:12333", nick: "Bob", pw: "", spw: "", bookmark: "" };
            }
            if (scenario === "disconnect") void v.disconnect();
            window.__trayCheck.release();
        }, scenario);
        await expect.poll(() => page.evaluate(() => window.__trayCheck.finished)).toBe(true);
        if (scenario === "still offline") {
            await expect.poll(() => page.evaluate(() => window.__calls.ConnectBookmarkTabWithID || 0)).toBe(1);
            expect(await page.evaluate(() => window.__callArgs.ConnectBookmarkTabWithID[0][1])).toBe("source.example:12333");
        } else {
            expect(await page.evaluate(() => window.__calls.ConnectBookmarkTabWithID || 0)).toBe(0);
        }
        expect(await page.evaluate(() => window.__trayCheck.lookups[0])).toBe("ListTabs");
    });
});

test.describe("confirmed tab-bound media controls", () => {
    test.beforeEach(async ({ page }) => {
        await page.evaluate(() => {
            const v = window.__noxa;
            v.showWorkspace(false);
            Object.assign(v.state, { activeTabID: "server-a", myClientID: "self", myChannelID: 42, lastWhispererUID: "peer", myPriority: false, whisperArmed: false });
            const f = window.__controls = { nativeTab: "server-a", effects: [], errors: [], pending: [] };
            v.sysMsg = message => f.errors.push(message);
            const app = window.go.main.App;
            window.go.main.App = new Proxy(app, { get(target, key) {
                if (!["SetPrioritySpeaker", "WhisperSet", "SetVideoQuality"].includes(key.replace(/ForTab$/, ""))) return target[key];
                return async (...args) => {
                    const tab = key.endsWith("ForTab") ? args.shift() : f.nativeTab;
                    if (tab !== f.nativeTab) return "server changed";
                    f.effects.push([key.replace(/ForTab$/, ""), tab, ...args]);
                    return await new Promise(resolve => f.pending.push(resolve));
                };
            } });
            window.__runControl = action => {
                if (action === "priority") return document.getElementById("voice-prio").onclick();
                return Promise.all((window.__events.hotkey || []).map(cb => cb("whisper_reply")));
            };
        });
    });
    for (const action of ["priority", "whisper"]) {
        test(`${action} rejects native activation before frontend reset`, async ({ page }) => {
            await page.evaluate(action => { window.__controls.nativeTab = "server-b"; window.__controlAction = window.__runControl(action); }, action);
            await expect.poll(() => page.evaluate(() => window.__controls.effects.length)).toBe(0);
            expect(await page.evaluate(() => window.__noxa.state.myPriority || window.__noxa.state.whisperArmed)).toBe(false);
        });
        test(`${action} waits for confirmation and contains denial`, async ({ page }) => {
            await page.evaluate(action => { window.__controlAction = window.__runControl(action); }, action);
            await expect.poll(() => page.evaluate(() => window.__controls.pending.length)).toBe(1);
            expect(await page.evaluate(() => window.__noxa.state.myPriority || window.__noxa.state.whisperArmed)).toBe(false);
            await page.evaluate(async () => { window.__controls.pending.shift()("denied"); await window.__controlAction; });
            expect(await page.evaluate(() => window.__noxa.state.myPriority || window.__noxa.state.whisperArmed)).toBe(false);
        });
        test(`${action} ignores an old successful completion`, async ({ page }) => {
            await page.evaluate(action => { window.__controlAction = window.__runControl(action); }, action);
            await expect.poll(() => page.evaluate(() => window.__controls.pending.length)).toBe(1);
            await page.evaluate(async () => {
                const s = window.__noxa.state;
                s.activeTabID = "server-b"; s.serverGeneration++; s.myPriority = false; s.whisperArmed = false;
                window.__controls.pending.shift()(""); await window.__controlAction;
            });
            expect(await page.evaluate(() => window.__noxa.state.myPriority || window.__noxa.state.whisperArmed)).toBe(false);
        });
    }
    test("priority confirmation does not invert an earlier authoritative event", async ({ page }) => {
        await page.evaluate(() => { window.__controlAction = window.__runControl("priority"); });
        await expect.poll(() => page.evaluate(() => window.__controls.pending.length)).toBe(1);
        await page.evaluate(async () => {
            for (const cb of window.__events.event || []) cb(JSON.stringify({ type: "priority_speaker_changed", data: { client_id: "self", active: true } }));
            window.__controls.pending.shift()(""); await window.__controlAction;
        });
        expect(await page.evaluate(() => window.__noxa.state.myPriority)).toBe(true);
    });
    test("whisper reply preserves its confirmed target and failed restore remains armed", async ({ page }) => {
        await page.evaluate(() => { window.__controlAction = window.__runControl("whisper"); });
        await expect.poll(() => page.evaluate(() => window.__controls.pending.length)).toBe(1);
        await page.evaluate(async () => {
            window.__noxa.state.lastWhispererUID = "new-incoming-peer";
            window.__controls.pending.shift()(""); await window.__controlAction;
        });
        expect(await page.evaluate(() => window.__noxa.state.whisperTargetUID)).toBe("peer");
        await expect(page.locator("#voice-status")).toContainText("Whisper to peer");
        await page.evaluate(() => { window.__controlAction = window.__runControl("whisper"); });
        await expect.poll(() => page.evaluate(() => window.__controls.pending.length)).toBe(1);
        await page.evaluate(async () => { window.__controls.pending.shift()("restore denied"); await window.__controlAction; });
        expect(await page.evaluate(() => window.__noxa.state.whisperArmed)).toBe(true);
        expect(await page.evaluate(() => window.__noxa.state.whisperTargetUID)).toBe("peer");
    });
    test("voice teardown declares stop sharing only for the original tab", async ({ page }) => {
        await page.evaluate(async () => {
            const s = window.__noxa.state;
            s.voiceTabID = "server-a";
            s.pc = { close() {} };
            s.shareStream = document.createElement("canvas").captureStream(1);
            s.screenSharing = true;
            const calls = window.__publicationCalls = [];
            const app = window.go.main.App;
            const control = async (tab, msg) => {
                calls.push([tab, msg]);
                return { ...msg, generation: "7", streams: [] };
            };
            window.go.main.App = new Proxy(app, { get: (target, key) => key === "VideoStreamControlForTab" ? control : target[key] });
            await (await import("/src/stream-publication.js")).startPublication("screen", s.shareStream.getVideoTracks()[0]);
            s.activeTabID = "server-b";
            window.__noxa.resetVoiceSession();
        });
        expect(await page.evaluate(() => window.__publicationCalls.filter(([, msg]) => msg.action === "publish" && !msg.active))).toEqual([
            ["server-a", expect.objectContaining({ slot: "screen", generation: "7", active: false })],
        ]);
    });
    for (const scenario of ["retry", "reopen retry", "server switch"]) test(`whisper settings preserve ${scenario} ownership`, async ({ page }) => {
        await installSaveScenario(page, { whisper_active: false, whisper_clients: [], whisper_channels: [] });
        await page.evaluate(() => {
            window.__saveMode = "success";
            window.__noxa.state.channels = [{ ChannelID: 42, Name: "Original room" }];
            window.__noxa.openSettings("whisper");
        });
        await page.getByRole("checkbox", { name: "Activate whisper", exact: true }).check();
        await page.getByRole("checkbox", { name: "Original room", exact: true }).check();
        if (scenario === "server switch") {
            await page.evaluate(() => {
                window.__noxa.state.activeTabID = "server-b";
                window.__noxa.state.serverGeneration++;
                window.__controls.nativeTab = "server-b";
            });
            await page.locator("#set-apply").click();
            await expect(page.locator("#settings-overlay")).not.toHaveAttribute("aria-busy", "true");
            expect(await page.evaluate(() => window.__controls.effects)).toEqual([]);
            return;
        }
        await page.locator("#set-apply").click();
        await expect.poll(() => page.evaluate(() => window.__controls.pending.length)).toBe(1);
        await page.evaluate(() => window.__controls.pending.shift()("whisper denied"));
        await expect(page.locator(".settings-save-status")).toContainText("whisper denied");
        if (scenario === "reopen retry") {
            await page.locator("#set-cancel").click();
            await page.evaluate(() => window.__noxa.openSettings("whisper"));
        }
        await page.locator("#set-apply").click();
        await expect.poll(() => page.evaluate(() => window.__controls.effects.length)).toBe(2);
        await page.evaluate(() => window.__controls.pending.shift()(""));
        await expect(page.locator("#settings-overlay")).not.toHaveAttribute("aria-busy", "true");
    });
});

test.describe("tab-bound voice signaling", () => {
    test.beforeEach(async ({ page }) => {
        await page.evaluate(() => {
            const v = window.__noxa;
            v.showWorkspace(false);
            Object.assign(v.state, { activeTabID: "server-a", myChannelID: 42, channels: [{ ChannelID: 42, Name: "Voice" }] });
            const f = window.__voiceScope = { nativeTab: "server-b", calls: [], effects: [] };
            const app = window.go.main.App;
            window.go.main.App = new Proxy(app, { get(target, key) {
                const method = key.replace(/ForTab$/, "");
                if (!["GetICEServers", "GetMediaLimits", "WebRTCOffer", "WebRTCAnswer", "SendICECandidate"].includes(method)) return target[key];
                return async (...args) => {
                    const tab = key.endsWith("ForTab") ? args.shift() : f.nativeTab;
                    f.calls.push([method, tab, ...args]);
                    if (tab !== f.nativeTab) throw new Error("server changed");
                    f.effects.push([method, tab, ...args]);
                    if (method === "GetICEServers") return [];
                    if (method === "GetMediaLimits") return {};
                    if (method === "WebRTCOffer") {
                        if (f.holdOffer) return await new Promise(resolve => { f.resolveOffer = resolve; });
                        return "answer";
                    }
                };
            } });
        });
    });
    test("voice metadata cannot come from the newly active native tab", async ({ page }) => {
        await page.evaluate(async () => {
            window.__voiceScope.capture = document.createElement("canvas").captureStream(1);
            navigator.mediaDevices.getUserMedia = async () => window.__voiceScope.capture;
            await window.__noxa.ensureVoiceForChannel();
        });
        expect(await page.evaluate(() => window.__voiceScope.effects)).toEqual([]);
        expect(await page.evaluate(() => window.__voiceScope.calls.map(call => call.slice(0, 2)))).toEqual([["GetICEServers", "server-a"], ["GetMediaLimits", "server-a"]]);
        expect(await page.evaluate(() => window.__voiceScope.capture.getTracks()[0].readyState)).toBe("ended");
    });
    for (const action of ["offer", "answer"]) test(`${action} remains bound across native activation`, async ({ page }) => {
        await page.evaluate(async action => {
            const video = await import("/src/video.js");
            const identity = "a=ice-ufrag:original\r\na=ice-pwd:original-secret";
            const pc = window.__noxa.state.pc = {
                remoteDescription: { sdp: identity },
                createOffer: async () => ({ type: "offer", sdp: "offer" }),
                createAnswer: async () => ({ type: "answer", sdp: "answer" }),
                setLocalDescription: async () => {},
                setRemoteDescription: async () => {},
                getSenders: () => [],
            };
            try {
                if (action === "offer") await video.renegotiate(pc);
                else await video.answerRemoteOffer(pc, window.__noxa.state.serverGeneration, identity);
            } catch { /* stale native tab is expected */ }
        }, action);
        expect(await page.evaluate(() => window.__voiceScope.effects)).toEqual([]);
        expect(await page.evaluate(() => window.__voiceScope.calls.map(call => call.slice(0, 2)))).toEqual([[action === "offer" ? "WebRTCOffer" : "WebRTCAnswer", "server-a"]]);
    });
    for (const slot of ["mic", "cam", "screenaudio"]) test(`ending a replaced ${slot} track preserves its successor`, async ({ page }) => {
        await page.evaluate(() => {
            window.__voiceScope.nativeTab = "server-a";
            window.__voiceScope.holdOffer = true;
            navigator.mediaDevices.getUserMedia = async () => new MediaStream();
            window.__voiceStart = window.__noxa.ensureVoiceForChannel();
        });
        await expect.poll(() => page.evaluate(() => typeof window.__voiceScope.resolveOffer)).toBe("function");
        const result = await page.evaluate(async slot => {
            const v = window.__noxa;
            const pc = v.state.pc;
            const context = new AudioContext();
            const capture = () => slot === "cam" ? document.createElement("canvas").captureStream(1) : context.createMediaStreamDestination().stream;
            const first = capture();
            const second = capture();
            const id = slot === "mic" ? "publisher" : `publisher|${slot}`;
            const playbacks = [];
            const NativeAudio = window.Audio;
            window.Audio = function (...args) {
                const playback = new NativeAudio(...args);
                playbacks.push(playback);
                return playback;
            };
            for (const stream of [first, second]) {
                Object.defineProperty(stream.getTracks()[0], "id", { value: id });
                pc.ontrack({ track: stream.getTracks()[0], streams: [stream] });
            }
            first.getTracks()[0].dispatchEvent(new Event("ended"));
            const result = { registered: v.state.trackUsers.has(id) };
            window.Audio = NativeAudio;
            if (slot !== "cam") result.playback = playbacks.at(-1)?.srcObject?.getTracks()[0] === second.getTracks()[0];
            if (slot === "cam") result.tile = !!document.querySelector('.vtile[data-clid="publisher"]');
            if (slot === "screenaudio") result.audio = v.shareAudioCtl.get("publisher");
            v.state.myChannelID = 0;
            v.resetVoiceSession();
            for (const stream of [first, second]) stream.getTracks().forEach(track => track.stop());
            await context.close();
            window.__voiceScope.resolveOffer("obsolete answer");
            await window.__voiceStart;
            return result;
        }, slot);
        expect(result.registered).toBe(true);
        if (slot !== "cam") expect(result.playback).toBe(true);
        if (slot === "cam") expect(result.tile).toBe(false); // Viewing requires an explicit watch.
        if (slot === "screenaudio") expect(result.audio).toEqual({ muted: false, volume: 100 });
    });
    for (const callback of ["ICE", "track"]) test(`replaced peers cannot deliver late ${callback} callbacks`, async ({ page }) => {
        await page.evaluate(() => {
            window.__voiceScope.nativeTab = "server-a";
            window.__voiceScope.holdOffer = true;
            navigator.mediaDevices.getUserMedia = async () => new MediaStream();
            window.__voiceStart = window.__noxa.ensureVoiceForChannel();
        });
        await expect.poll(() => page.evaluate(() => typeof window.__voiceScope.resolveOffer)).toBe("function");
        await page.evaluate(async callback => {
            const v = window.__noxa;
            const pc = v.state.pc;
            if (!pc) throw new Error("voice fixture did not create a peer");
            window.__voiceScope.calls = [];
            v.state.pc = {};
            pc.onicecandidate({ candidate: { candidate: "old-candidate", sdpMid: "0", sdpMLineIndex: 0 } });
            if (callback === "track") {
                const stream = document.createElement("canvas").captureStream(1);
                const track = stream.getVideoTracks()[0];
                pc.ontrack({ track, streams: [stream] });
                window.__voiceScope.lateTrack = { readyState: track.readyState, registered: v.state.trackUsers.has(track.id) };
            }
            v.state.pc = pc;
            v.state.myChannelID = 0;
            v.resetVoiceSession();
            window.__voiceScope.resolveOffer("obsolete answer");
            await window.__voiceStart;
        }, callback);
        expect(await page.evaluate(() => window.__voiceScope.calls.filter(call => call[0] === "SendICECandidate"))).toEqual([]);
        if (callback === "track") expect(await page.evaluate(() => window.__voiceScope.lateTrack)).toEqual({ readyState: "ended", registered: false });
    });
});

test.describe("confirmed tab-bound presence", () => {
    test.beforeEach(async ({ page }) => {
        await page.evaluate(() => {
            const v = window.__noxa;
            v.showWorkspace(false);
            Object.assign(v.state, { activeTabID: "server-a", myClientID: "self", authorizationModel: "roles-v1", myStatus: "", canSetInvisible: true, isAdmin: false });
            const f = window.__presence = { nativeTab: "server-a", calls: [], pending: [], messages: [], errors: [], timers: [] };
            v.sysMsg = msg => f.messages.push(msg);
            v.toast = (...args) => f.errors.push(args);
            const app = window.go.main.App;
            window.go.main.App = new Proxy(app, { get(target, key) {
                if (key !== "SetStatus" && key !== "SetStatusForTab") return target[key];
                return async (...args) => {
                    const tab = key === "SetStatusForTab" ? args.shift() : f.nativeTab;
                    f.calls.push([tab, ...args]);
                    if (tab !== f.nativeTab) return "server changed";
                    return await new Promise((resolve, reject) => f.pending.push({ resolve, reject }));
                };
            } });
            const timer = window.setTimeout;
            window.setTimeout = (callback, ms, ...args) => {
                if (ms === 60000) { const id = timer(() => {}, 3600000); f.timers.push({ id, callback }); return id; }
                return timer(callback, ms, ...args);
            };
        });
    });
    test("status picker uses displayed tab during native activation", async ({ page }) => {
        await page.evaluate(() => { window.__noxaSocial.openStatusPicker(); window.__presence.nativeTab = "server-b"; });
        await page.locator('.st-sel').selectOption("busy");
        await page.getByRole("button", { name: "Set", exact: true }).click();
        await expect.poll(() => page.evaluate(() => window.__presence.calls)).toEqual([["server-a", "busy", ""]]);
        expect(await page.evaluate(() => window.__noxa.state.myStatus)).toBe("");
    });
    test("role authority supplies invisible option and confirmed feedback", async ({ page }) => {
        await page.evaluate(() => window.__noxaSocial.openStatusPicker());
        await expect(page.locator('.st-sel option[value="invisible"]')).toHaveCount(1);
        await page.locator('.st-sel').selectOption("invisible");
        await page.getByRole("button", { name: "Set", exact: true }).click();
        await expect.poll(() => page.evaluate(() => window.__presence.pending.length)).toBe(1);
        expect(await page.evaluate(() => window.__noxa.state.myStatus)).toBe("");
        await page.evaluate(() => window.__presence.pending[0].resolve(""));
        await expect.poll(() => page.evaluate(() => window.__noxa.state.myStatus)).toBe("invisible");
        expect(await page.evaluate(() => window.__presence.messages)).toEqual(["status: invisible"]);
    });
    test("legacy admin flag cannot enable invisible in role mode", async ({ page }) => {
        await page.evaluate(() => { window.__noxa.state.canSetInvisible = false; window.__noxa.state.isAdmin = true; window.__noxaSocial.openStatusPicker(); });
        await expect(page.locator('.st-sel option[value="invisible"]')).toHaveCount(0);
    });
    test("auto-away does not report success before confirmation", async ({ page }) => {
        await page.evaluate(() => {
            window.__noxa.state.settings.auto_away_minutes = 1;
            document.dispatchEvent(new MouseEvent("mousemove"));
            window.__presence.timers.at(-1).callback();
        });
        await expect.poll(() => page.evaluate(() => window.__presence.pending.length)).toBe(1);
        expect(await page.evaluate(() => window.__noxa.state.myStatus)).toBe("");
        await page.evaluate(() => window.__presence.pending[0].resolve("status denied"));
        await expect.poll(() => page.evaluate(() => window.__presence.errors.length)).toBe(1);
        expect(await page.evaluate(() => window.__noxa.state.myStatus)).toBe("");
    });
    test("old idle timer cannot set the replacement server away", async ({ page }) => {
        await page.evaluate(() => {
            window.__noxa.state.settings.auto_away_minutes = 1;
            document.dispatchEvent(new MouseEvent("mousemove"));
            const state = window.__noxa.state;
            state.activeTabID = "server-b";
            state.serverGeneration++;
            window.__presence.nativeTab = "server-b";
            window.__presence.timers.at(-1).callback();
        });
        expect(await page.evaluate(() => window.__presence.calls)).toEqual([]);
    });
    test("tab activation starts its own idle timer after session restoration", async ({ page }) => {
        await page.evaluate(() => {
            window.__noxa.state.settings.auto_away_minutes = 1;
            const app = window.go.main.App;
            window.go.main.App = new Proxy(app, { get(target, key) {
                if (key === "SessionInfoForTab") return async () => ({ client_id: "self-b", connected: true, is_admin: false, is_guest: false });
                return target[key];
            } });
            window.__presence.nativeTab = "server-b";
            for (const callback of window.__events.tab_reset || []) callback("server-b");
        });
        await expect.poll(() => page.evaluate(() => window.__noxa.state.myClientID)).toBe("self-b");
        await expect.poll(() => page.evaluate(() => window.__presence.timers.length)).toBe(1);
        await page.evaluate(() => { void window.__presence.timers[0].callback(); });
        await expect.poll(() => page.evaluate(() => window.__presence.calls)).toEqual([["server-b", "away", "auto-away"]]);
    });
    test("activity during pending auto-away restores online once", async ({ page }) => {
        await page.evaluate(() => {
            window.__noxa.state.settings.auto_away_minutes = 1;
            document.dispatchEvent(new MouseEvent("mousemove"));
            window.__presence.timers.at(-1).callback();
        });
        await expect.poll(() => page.evaluate(() => window.__presence.pending.length)).toBe(1);
        await page.evaluate(() => {
            for (let i = 0; i < 5; i++) document.dispatchEvent(new MouseEvent("mousemove"));
        });
        await expect.poll(() => page.evaluate(() => window.__presence.calls)).toEqual([["server-a", "away", "auto-away"], ["server-a", "online", ""]]);
        await page.evaluate(() => window.__presence.pending[0].resolve(""));
        expect(await page.evaluate(() => window.__noxa.state.myStatus)).toBe("");
        await page.evaluate(() => window.__presence.pending[1].resolve(""));
        expect(await page.evaluate(() => window.__noxa.state.myStatus)).toBe("");
        await expect(page.locator('#chat-log')).not.toContainText("auto-away after");
    });
    test("a newer manual status owns completion and feedback", async ({ page }) => {
        for (const status of ["busy", "invisible"]) {
            await page.evaluate(() => window.__noxaSocial.openStatusPicker());
            await page.locator('.st-sel').selectOption(status);
            await page.getByRole("button", { name: "Set", exact: true }).click();
        }
        await expect.poll(() => page.evaluate(() => window.__presence.pending.length)).toBe(2);
        await page.evaluate(() => window.__presence.pending[1].resolve(""));
        await expect.poll(() => page.evaluate(() => window.__noxa.state.myStatus)).toBe("invisible");
        await page.evaluate(() => window.__presence.pending[0].resolve(""));
        expect(await page.evaluate(() => window.__noxa.state.myStatus)).toBe("invisible");
        expect(await page.evaluate(() => window.__presence.messages)).toEqual(["status: invisible"]);
    });
    test("failed automatic restoration does not retry on every activity", async ({ page }) => {
        await page.evaluate(() => {
            window.__noxa.state.myStatus = "away";
            document.dispatchEvent(new MouseEvent("mousemove"));
        });
        await expect.poll(() => page.evaluate(() => window.__presence.pending.length)).toBe(1);
        await page.evaluate(() => window.__presence.pending[0].resolve("bridge unavailable"));
        await expect.poll(() => page.evaluate(() => window.__presence.errors.length)).toBe(1);
        await page.evaluate(() => {
            for (let i = 0; i < 10; i++) document.dispatchEvent(new MouseEvent("mousemove"));
        });
        expect(await page.evaluate(() => window.__presence.calls)).toEqual([["server-a", "online", ""]]);
        // A deliberate manual change can still recover immediately.
        await page.evaluate(() => window.__noxaSocial.openStatusPicker());
        await page.locator('.st-sel').selectOption("online");
        await page.getByRole("button", { name: "Set", exact: true }).click();
        await expect.poll(() => page.evaluate(() => window.__presence.pending.length)).toBe(2);
        await page.evaluate(() => window.__presence.pending[1].resolve(""));
        await expect.poll(() => page.evaluate(() => window.__noxa.state.myStatus)).toBe("");
    });
    for (const replaced of [true, false]) test(`bridge failure ${replaced ? "after replacement stays silent" : "is reported without changing status"}`, async ({ page }) => {
        await page.evaluate(() => window.__noxaSocial.openStatusPicker());
        await page.locator('.st-sel').selectOption("busy");
        await page.getByRole("button", { name: "Set", exact: true }).click();
        await expect.poll(() => page.evaluate(() => window.__presence.pending.length)).toBe(1);
        await page.evaluate(replaced => {
            if (replaced) { window.__noxa.state.serverGeneration++; window.__noxa.state.activeTabID = "server-b"; }
            window.__presence.pending[0].reject(new Error("bridge unavailable"));
        }, replaced);
        expect(await page.evaluate(() => window.__noxa.state.myStatus)).toBe("");
        expect(await page.evaluate(() => window.__presence.messages)).toEqual([]);
        await expect.poll(() => page.evaluate(() => window.__presence.errors.length)).toBe(replaced ? 0 : 1);
    });
    test("snapshot and event restore authoritative self presence and eligibility", async ({ page }) => {
        await page.evaluate(() => {
            for (const callback of window.__events.snapshot || []) callback(JSON.stringify({ can_set_invisible: true, root_channels: [], unassigned_clients: [{ client_id: "self", unique_id: "me", status: "busy", channel_id: 0 }] }));
        });
        expect(await page.evaluate(() => [window.__noxa.state.myStatus, window.__noxa.state.canSetInvisible])).toEqual(["busy", true]);
        await page.evaluate(() => {
            for (const callback of window.__events.snapshot || []) callback(JSON.stringify({ root_channels: [], unassigned_clients: [{ client_id: "self", unique_id: "me", status: "", channel_id: 0 }] }));
        });
        expect(await page.evaluate(() => [window.__noxa.state.myStatus, window.__noxa.state.canSetInvisible])).toEqual(["", false]);
        await page.evaluate(() => {
            for (const callback of window.__events.event || []) callback(JSON.stringify({ type: "status_changed", data: { client_id: "self", status: "away", message: "back soon" } }));
        });
        expect(await page.evaluate(() => window.__noxa.state.myStatus)).toBe("away");
    });
});

test.describe("tab-bound rules subscriptions and avatars", () => {
    test.beforeEach(async ({ page }) => {
        await page.evaluate(() => {
            const v = window.__noxa;
            v.showWorkspace(false);
            Object.assign(v.state, { activeTabID: "server-a", myChannelID: 1 });
            const f = window.__sessionActions = { nativeTab: "server-b", calls: [], effects: [], toasts: [] };
            v.toast = (...args) => f.toasts.push(args);
            const app = window.go.main.App;
            window.go.main.App = new Proxy(app, { get(target, key) {
                const method = key.replace(/ForTab$/, "");
                if (!["AcceptServerRules", "SubscribeChannels", "GetAvatar", "Disconnect", "DisconnectTab"].includes(method)) return target[key];
                return async (...args) => {
                    const tab = key.endsWith("ForTab") || key === "DisconnectTab" ? args.shift() : f.nativeTab;
                    f.calls.push([method, tab, ...args]);
                    if (key !== "DisconnectTab" && tab !== f.nativeTab) {
                        if (method === "GetAvatar") throw new Error("server changed");
                        return "server changed";
                    }
                    f.effects.push([method, tab, ...args]);
                    if (f.hold && method === "SubscribeChannels") return await new Promise(resolve => { f.resolve = resolve; });
                    if (method === "GetAvatar") return {};
                    return "";
                };
            } });
        });
    });
    for (const action of ["accept", "decline", "subscribe", "avatar", "disconnect"]) {
        test(`${action} stays on the displayed server before tab reset`, async ({ page }) => {
            if (action === "accept" || action === "decline") {
                await page.evaluate(async () => (await import("/src/notifications.js")).showServerRules({ text: "Be kind", hash: "revision-a" }));
                await page.getByRole("button", { name: action === "accept" ? "Accept" : "Decline and disconnect", exact: true }).click();
            } else if (action === "subscribe") {
                await page.evaluate(async () => (await import("/src/chat-ui.js")).setChannelSubscription(3, true));
            } else if (action === "disconnect") {
                await page.evaluate(() => window.__noxa.disconnect());
            } else {
                await page.evaluate(() => window.__noxa.fetchAvatar("peer"));
            }
            const calls = await page.evaluate(() => window.__sessionActions.calls);
            expect(calls).toHaveLength(1);
            expect(calls[0][1]).toBe("server-a");
            expect(await page.evaluate(() => window.__sessionActions.effects)).toEqual(["decline", "disconnect"].includes(action) ? [["DisconnectTab", "server-a"]] : []);
        });
    }
    for (const replacement of [true, false]) test(`late subscription rejection preserves the ${replacement ? "replacement server" : "newer request"} pending tab`, async ({ page }) => {
        await page.evaluate(async () => {
            const chat = await import("/src/chat-ui.js");
            const f = window.__sessionActions;
            f.nativeTab = "server-a";
            f.hold = true;
            window.__oldSubscription = chat.openChannelTab(3);
        });
        await expect.poll(() => page.evaluate(() => typeof window.__sessionActions.resolve)).toBe("function");
        await page.evaluate(async replacement => {
            const chat = await import("/src/chat-ui.js");
            const f = window.__sessionActions;
            if (replacement) {
                Object.assign(window.__noxa.state, { activeTabID: "server-b", serverGeneration: window.__noxa.state.serverGeneration + 1 });
                chat.resetView();
                f.nativeTab = "server-b";
            }
            f.hold = false;
            await chat.openChannelTab(3);
            f.resolve("old server denied");
            await window.__oldSubscription;
            chat.onSubscriptions({ channel_ids: [1, 3] });
        }, replacement);
        expect(await page.evaluate(() => window.__sessionActions.toasts)).toEqual(replacement ? [] : [["subscription update failed: old server denied", "warn"]]);
        await expect(page.locator('.channel-tab.active')).toContainText("3");
    });
    test("rules success waits for the server event and preserves the displayed hash", async ({ page }) => {
        await page.evaluate(async () => {
            window.__sessionActions.nativeTab = "server-a";
            (await import("/src/notifications.js")).showServerRules({ text: "Be kind", hash: "revision-a" });
        });
        await page.getByRole("button", { name: "Accept", exact: true }).click();
        expect(await page.evaluate(() => window.__sessionActions.calls)).toEqual([["AcceptServerRules", "server-a", "revision-a"]]);
        await expect(page.locator('.server-rules-gate')).toContainText("recording acceptance");
        await page.evaluate(() => {
            for (const callback of window.__events.server_rules || []) callback(JSON.stringify({ text: "", hash: "" }));
        });
        await expect(page.locator('.server-rules-gate')).toHaveCount(0);
    });
    for (const action of ["accept", "decline"]) test(`rules ${action} bridge rejection restores controls`, async ({ page }) => {
        await page.evaluate(async action => {
            const app = window.go.main.App;
            window.go.main.App = new Proxy(app, { get(target, key) {
                if (key === (action === "accept" ? "AcceptServerRulesForTab" : "DisconnectTab")) return async () => { throw new Error("bridge unavailable"); };
                return target[key];
            } });
            (await import("/src/notifications.js")).showServerRules({ text: "Be kind", hash: "revision-a" });
        }, action);
        await page.getByRole("button", { name: action === "accept" ? "Accept" : "Decline and disconnect", exact: true }).click();
        await expect(page.locator('.server-rules-gate')).toContainText("bridge unavailable");
        await expect(page.getByRole("button", { name: "Accept", exact: true })).toBeEnabled();
        await expect(page.getByRole("button", { name: "Decline and disconnect" })).toBeEnabled();
    });
});

test.describe("DM identity context lifecycle", () => {
    test.beforeEach(async ({ page }) => {
        await page.evaluate(() => {
            const v = window.__noxa;
            v.showWorkspace(false);
            Object.assign(v.state, { activeTabID: "server-a", myUniqueID: "display-identity", myChannelID: 1 });
            const f = window.__dmIdentity = {
                native: { tab_id: "server-a", identity_uid: "storage-identity", activation: "4", identity_revision: "0" },
                calls: [], effects: [], acquisitions: [], pending: [], toasts: [], unhandled: [], hold: false,
            };
            v.toast = (...args) => f.toasts.push(args);
            window.addEventListener("unhandledrejection", event => f.unhandled.push(String(event.reason)));
            const app = window.go.main.App;
            window.go.main.App = new Proxy(app, { get(target, key) {
                if (key === "DMHistoryContextForTab") return async tab => {
                    f.acquisitions.push(tab);
                    if (tab !== f.native.tab_id) throw new Error("server changed");
                    const value = structuredClone(f.native);
                    if (f.hold) return await new Promise((resolve, reject) => f.pending.push({ value, resolve, reject }));
                    return value;
                };
                const method = key.replace(/ForContext$/, "");
                if (!["DMHistoryLoad", "DMHistoryAppend", "DMHistoryClear", "DMHistoryPeers", "DMSearch", "DMExportHistory"].includes(method)) return target[key];
                return async (...args) => {
                    const context = key.endsWith("ForContext") ? args.shift() : structuredClone(f.native);
                    f.calls.push([method, context, ...args]);
                    if (JSON.stringify(context) !== JSON.stringify(f.native)) {
                        if (["DMHistoryAppend", "DMHistoryClear"].includes(method)) return "DM history owner changed";
                        throw new Error("DM history owner changed");
                    }
                    f.effects.push([method, context, ...args]);
                    if (method === "DMHistoryLoad") return [{ body: "stored for " + context.identity_uid, sent_at: 1 }];
                    if (method === "DMHistoryPeers") return [];
                    if (method === "DMSearch") return { messages: [], scanned: 0 };
                    if (method === "DMExportHistory") return { text: "private transcript", messages: 1, complete: true };
                    return "";
                };
            } });
            window.__dmIdentityChanged = revision => {
                f.native = { ...f.native, identity_uid: "replacement-identity", identity_revision: revision };
                for (const callback of window.__events.dm_history_identity_changed || []) callback(revision);
            };
            window.__noxaChat.resetView();
        });
        await page.evaluate(() => new Promise(resolve => setTimeout(resolve, 0)));
        await page.evaluate(() => { window.__dmIdentity.calls.length = 0; window.__dmIdentity.effects.length = 0; });
    });
    test("native activation before reset cannot redirect a DM history load", async ({ page }) => {
        await page.evaluate(() => {
            window.__dmIdentity.native.tab_id = "server-b";
            window.__noxaChat.openPM("peer", "Peer");
        });
        await expect.poll(() => page.evaluate(() => window.__dmIdentity.calls.length)).toBe(1);
        expect(await page.evaluate(() => window.__dmIdentity.effects)).toEqual([]);
        expect(await page.evaluate(() => window.__dmIdentity.calls[0][1].identity_uid)).toBe("storage-identity");
    });
    test("identity change before notification cannot redirect a pending clear", async ({ page }) => {
        await page.evaluate(() => window.__noxaChat.openPM("peer", "Peer"));
        await expect(page.locator("#chat-log")).toContainText("stored for storage-identity");
        await page.locator(".pm-tab").click({ button: "right" });
        await page.evaluate(() => { window.__dmIdentity.native.identity_uid = "replacement-identity"; window.__dmIdentity.native.identity_revision = "2"; });
        await page.getByRole("button", { name: "Delete history", exact: true }).click();
        await expect.poll(() => page.evaluate(() => window.__dmIdentity.calls.filter(call => call[0] === "DMHistoryClear").length)).toBe(1);
        expect(await page.evaluate(() => window.__dmIdentity.effects.filter(call => call[0] === "DMHistoryClear"))).toEqual([]);
        await expect(page.locator("#chat-log")).toContainText("stored for storage-identity");
        expect(await page.evaluate(() => window.__dmIdentity.acquisitions.length)).toBe(1);
    });
    test("same-tab identity event replaces DM state and ignores duplicate or older revisions", async ({ page }) => {
        await page.evaluate(() => window.__noxaChat.openPM("peer", "Peer"));
        await expect(page.locator("#chat-log")).toContainText("stored for storage-identity");
        await page.evaluate(() => window.__dmIdentityChanged("2"));
        await expect(page.locator("#chat-log")).not.toContainText("stored for storage-identity");
        await page.evaluate(() => window.__noxaChat.openPM("peer", "Peer"));
        await expect(page.locator("#chat-log")).toContainText("stored for replacement-identity");
        await page.evaluate(() => {
            for (const revision of ["1", "2"]) for (const callback of window.__events.dm_history_identity_changed || []) callback(revision);
        });
        await expect(page.locator("#chat-log")).toContainText("stored for replacement-identity");
        expect(await page.evaluate(() => window.__dmIdentity.acquisitions.length)).toBe(2);
    });
    test("late context acquisition cannot authorize actions from an obsolete owner", async ({ page }) => {
        await page.evaluate(() => {
            window.__dmIdentity.hold = true;
            window.__noxaChat.resetView();
            window.__noxaChat.openPM("peer", "Peer");
        });
        await expect.poll(() => page.evaluate(() => window.__dmIdentity.pending.length)).toBe(1);
        await page.evaluate(() => window.__dmIdentityChanged("2"));
        await expect.poll(() => page.evaluate(() => window.__dmIdentity.pending.length)).toBe(2);
        await page.evaluate(() => { const p = window.__dmIdentity.pending[0]; p.resolve(p.value); });
        await page.evaluate(() => new Promise(resolve => requestAnimationFrame(resolve)));
        expect(await page.evaluate(() => window.__dmIdentity.effects)).toEqual([]);
        expect(await page.evaluate(() => window.__dmIdentity.toasts)).toEqual([]);
        await page.evaluate(() => { const p = window.__dmIdentity.pending[1]; p.resolve(p.value); });
        await page.evaluate(() => window.__noxaChat.openPM("peer", "Peer"));
        await expect(page.locator("#chat-log")).toContainText("stored for replacement-identity");
    });
    test("offline history uses a frozen offline context", async ({ page }) => {
        await page.evaluate(() => {
            window.__dmIdentity.native = { ...window.__dmIdentity.native, tab_id: "", activation: "5" };
            window.__noxa.state.activeTabID = "";
            window.__noxa.state.serverGeneration++;
            window.__noxaChat.resetView();
            window.__noxaChat.openPM("peer", "Peer");
        });
        await expect(page.locator("#chat-log")).toContainText("stored for storage-identity");
        expect(await page.evaluate(() => window.__dmIdentity.acquisitions.at(-1))).toBe("");
        expect(await page.evaluate(() => window.__dmIdentity.calls.at(-1)[1].tab_id)).toBe("");
    });
    test("newer getter response before notification invalidates its waiting actions", async ({ page }) => {
        await page.evaluate(() => {
            window.__dmIdentity.hold = true;
            window.__noxaChat.resetView();
            window.__noxaChat.openPM("peer", "Peer");
        });
        await expect.poll(() => page.evaluate(() => window.__dmIdentity.pending.length)).toBe(1);
        await page.evaluate(() => {
            const f = window.__dmIdentity;
            f.native = { ...f.native, identity_uid: "replacement-identity", identity_revision: "2" };
            f.pending[0].resolve(structuredClone(f.native));
        });
        await expect.poll(() => page.evaluate(() => window.__dmIdentity.pending.length)).toBe(2);
        expect(await page.evaluate(() => window.__dmIdentity.effects)).toEqual([]);
        await page.evaluate(() => { const p = window.__dmIdentity.pending[1]; p.resolve(p.value); });
        await page.evaluate(() => window.__noxaChat.openPM("peer", "Peer"));
        await expect(page.locator("#chat-log")).toContainText("stored for replacement-identity");
        await page.evaluate(() => window.__dmIdentityChanged("2"));
        await expect(page.locator("#chat-log")).toContainText("stored for replacement-identity");
        expect(await page.evaluate(() => window.__dmIdentity.pending.length)).toBe(2);
    });
    test("reopening recovers failed acquisition without replaying old descendants", async ({ page }) => {
        await page.evaluate(() => { window.__dmIdentity.hold = true; window.__noxaChat.resetView(); window.__noxaChat.openPM("peer", "Peer"); });
        await expect.poll(() => page.evaluate(() => window.__dmIdentity.pending.length)).toBe(1);
        await page.evaluate(() => window.__dmIdentity.pending[0].reject(new Error("temporary context failure")));
        await expect.poll(() => page.evaluate(() => window.__dmIdentity.toasts.length)).toBe(1);
        await page.locator(".pm-tab .pm-close").click();
        await page.evaluate(() => { window.__dmIdentity.hold = false; window.__noxaChat.openPM("peer", "Peer"); });
        await expect(page.locator("#chat-log")).toContainText("stored for storage-identity");
        expect(await page.evaluate(() => window.__dmIdentity.effects.filter(call => call[0] === "DMHistoryLoad").length)).toBe(1);
        expect(await page.evaluate(() => window.__dmIdentity.unhandled)).toEqual([]);
    });
    test("replayed outgoing DM uses the connection identity before context acquisition finishes", async ({ page }) => {
        await page.evaluate(() => {
            const f = window.__dmIdentity;
            f.hold = true;
            window.__noxaChat.resetView();
            window.__noxa.state.myUniqueID = "other-tab-identity";
            for (const callback of window.__events.tab_identity || []) callback({ tab_id: "server-a", identity_uid: "storage-identity" });
            window.__noxaChat.addChat({ direct: true, enc_verified: true, from_unique_id: "storage-identity", to_unique_id: "recipient", from: "Self", client_msg_id: "replayed", text: "outgoing replay" });
        });
        await expect.poll(() => page.evaluate(() => window.__dmIdentity.pending.length)).toBe(1);
        await page.evaluate(() => { const p = window.__dmIdentity.pending[0]; p.resolve(p.value); });
        await expect.poll(() => page.evaluate(() => window.__dmIdentity.effects.filter(call => call[0] === "DMHistoryAppend").length)).toBe(1);
        const append = await page.evaluate(() => window.__dmIdentity.effects.find(call => call[0] === "DMHistoryAppend"));
        expect(append[2]).toBe("recipient");
        expect(append[4].self).toBe(true);
    });
    for (const operation of ["append", "search", "export"]) {
        test(`native identity change cannot redirect DM ${operation}`, async ({ page }) => {
            await page.evaluate(() => window.__noxaChat.openPM("peer", "Peer"));
            await expect(page.locator("#chat-log")).toContainText("stored for storage-identity");
            await page.evaluate(() => { window.__dmIdentity.native.identity_uid = "replacement-identity"; window.__dmIdentity.native.identity_revision = "2"; });
            if (operation === "append") {
                await page.evaluate(() => window.__noxaChat.addChat({ direct: true, enc_verified: true, from_unique_id: "peer", from: "Peer", client_msg_id: "incoming", text: "incoming" }));
            } else if (operation === "search") {
                await page.evaluate(() => {
                    document.getElementById("chat-search-btn").click();
                    document.getElementById("chat-search").value = "needle";
                    document.getElementById("chat-search-server").click();
                });
            } else {
                await page.evaluate(() => document.getElementById("chat-export-btn").click());
                await page.locator('input[placeholder^="passphrase"]').fill("export-password");
                await page.getByRole("button", { name: "Export", exact: true }).click();
            }
            const method = { append: "DMHistoryAppend", search: "DMSearch", export: "DMExportHistory" }[operation];
            await expect.poll(() => page.evaluate(method => window.__dmIdentity.calls.filter(call => call[0] === method).length, method)).toBe(1);
            expect(await page.evaluate(method => window.__dmIdentity.effects.filter(call => call[0] === method), method)).toEqual([]);
            expect(await page.evaluate(() => window.__dmIdentity.acquisitions.length)).toBe(1);
            expect(await page.evaluate(() => window.__calls.ExportChatEncrypted || 0)).toBe(0);
        });
    }
    test("identity reset closes old quick-switcher actions and cancels offline summaries", async ({ page }) => {
        await page.evaluate(() => {
            window.__noxaChat.openPM("peer", "Peer");
            window.__noxaChat.addChat({ direct: true, offline: true, from_unique_id: "peer", from: "Peer", client_msg_id: "offline", text: "offline batch" });
        });
        await page.keyboard.press("Control+k");
        await expect(page.locator(".qs-overlay")).toBeVisible();
        await page.evaluate(() => {
            window.__oldDMQuickRow = [...document.querySelectorAll(".qs-row")].find(row => row.textContent.includes("Peer"));
            window.__dmIdentityChanged("2");
        });
        await expect(page.locator(".qs-overlay")).toHaveCount(0);
        await page.evaluate(() => window.__oldDMQuickRow.click());
        await page.waitForTimeout(800);
        expect(await page.evaluate(() => window.__dmIdentity.effects.filter(call => call[0] === "DMHistoryLoad" && call[1].identity_revision === "2"))).toEqual([]);
        expect(await page.evaluate(() => window.__dmIdentity.toasts)).toEqual([]);
    });
    test("malformed identity notifications cannot invalidate a valid owner", async ({ page }) => {
        await page.evaluate(() => window.__noxaChat.openPM("peer", "Peer"));
        await expect(page.locator("#chat-log")).toContainText("stored for storage-identity");
        await page.evaluate(() => {
            for (const revision of [null, {}, "-1", "NaN", "18446744073709551616"]) {
                for (const callback of window.__events.dm_history_identity_changed || []) callback(revision);
            }
        });
        await expect(page.locator("#chat-log")).toContainText("stored for storage-identity");
        expect(await page.evaluate(() => window.__dmIdentity.acquisitions.length)).toBe(1);
    });
});

test.describe("DM history callback ownership", () => {
    test.beforeEach(async ({ page }) => {
        await page.evaluate(() => {
            const v = window.__noxa;
            v.showWorkspace(false);
            Object.assign(v.state, { activeTabID: "server-a", myUniqueID: "self", myChannelID: 1 });
            const f = window.__dmScope = { loads: [], peers: [], clears: [], appends: [], toasts: [], unhandled: [] };
            v.toast = (...args) => f.toasts.push(args);
            window.addEventListener("unhandledrejection", event => f.unhandled.push(String(event.reason)));
            const app = window.go.main.App;
            window.go.main.App = new Proxy(app, { get(target, key) {
                const list = { DMHistoryLoad: "loads", DMHistoryPeers: "peers", DMHistoryClear: "clears", DMHistoryAppend: "appends" }[key.replace(/ForContext$/, "")];
                if (!list) return target[key];
                return (...args) => new Promise((resolve, reject) => f[list].push({ args: key.endsWith("ForContext") ? args.slice(1) : args, resolve, reject }));
            } });
            window.__dmIncoming = body => window.__noxaChat.addChat({ direct: true, enc_verified: true, from_unique_id: "peer", from: "Peer", client_msg_id: body, text: body });
        });
        await page.evaluate(() => window.__noxaChat.openPM("bootstrap", "Bootstrap"));
        await expect.poll(() => page.evaluate(() => window.__dmScope.loads.length)).toBe(1);
        await page.evaluate(() => window.__dmScope.loads[0].resolve([]));
        await page.locator(".pm-tab .pm-close").click();
        await page.evaluate(() => { window.__dmScope.loads.length = 0; });
    });
    for (const reject of [false, true]) {
        test(`obsolete history ${reject ? "failure" : "completion"} cannot repaint a reopened peer`, async ({ page }) => {
            await page.evaluate(() => window.__noxaChat.openPM("peer", "Peer"));
            await expect.poll(() => page.evaluate(() => window.__dmScope.loads.length)).toBe(1);
            await page.locator(".pm-tab .pm-close").click();
            await page.evaluate(() => window.__noxaChat.openPM("peer", "Peer"));
            await expect.poll(() => page.evaluate(() => window.__dmScope.loads.length)).toBe(2);
            await page.evaluate(() => window.__dmScope.loads[1].resolve([{ body: "current history", sent_at: 1 }]));
            await expect(page.locator("#chat-log")).toContainText("current history");
            await page.evaluate(reject => {
                window.__currentDMRow = document.querySelector("#chat-log .msg");
                const pending = window.__dmScope.loads[0];
                if (reject) pending.reject(new Error("old history failed"));
                else pending.resolve([{ body: "obsolete history", sent_at: 1 }]);
            }, reject);
            await page.evaluate(() => new Promise(resolve => requestAnimationFrame(resolve)));
            expect(await page.evaluate(() => window.__currentDMRow.isConnected)).toBe(true);
            expect(await page.evaluate(() => window.__dmScope.toasts)).toEqual([]);
            await expect(page.locator("#chat-log")).not.toContainText("obsolete history");
        });
    }
    test("old peer enumeration cannot restore tabs after a server reset", async ({ page }) => {
        await page.evaluate(() => window.__noxaChat.resetView());
        await expect.poll(() => page.evaluate(() => window.__dmScope.peers.length)).toBe(1);
        await page.evaluate(() => { window.__noxa.state.serverGeneration++; window.__noxaChat.resetView(); });
        await expect.poll(() => page.evaluate(() => window.__dmScope.peers.length)).toBe(2);
        await page.evaluate(() => {
            window.__dmScope.peers[1].resolve([]);
            window.__dmScope.peers[0].resolve([{ unique_id: "old-peer", nickname: "Obsolete peer" }]);
        });
        await page.evaluate(() => new Promise(resolve => requestAnimationFrame(resolve)));
        await expect(page.locator("#pm-tabs")).not.toContainText("Obsolete peer");
    });
    test("old persistence failure cannot consume the new session warning", async ({ page }) => {
        await page.evaluate(() => {
            window.__dmIncoming("first");
            window.__noxa.state.serverGeneration++;
            window.__noxaChat.resetView();
            window.__dmIncoming("second");
            window.__dmScope.toasts.length = 0;
            window.__dmScope.appends[0].resolve("old storage failure");
        });
        await page.evaluate(() => new Promise(resolve => requestAnimationFrame(resolve)));
        expect(await page.evaluate(() => window.__dmScope.toasts)).toEqual([]);
        await page.evaluate(() => window.__dmScope.appends[1].resolve("current storage failure"));
        await expect.poll(() => page.evaluate(() => window.__dmScope.toasts.length)).toBe(1);
        expect(await page.evaluate(() => window.__dmScope.toasts[0][0])).toContain("current storage failure");
    });
    test("old persistence failure cannot warn in a reopened peer", async ({ page }) => {
        await page.evaluate(() => { window.__noxaChat.openPM("peer", "Peer"); window.__dmIncoming("first"); });
        await page.locator(".pm-tab .pm-close").click();
        await page.evaluate(() => {
            window.__noxaChat.openPM("peer", "Peer");
            window.__dmIncoming("second");
            window.__dmScope.toasts.length = 0;
            window.__dmScope.appends[0].resolve("obsolete failure");
        });
        await page.evaluate(() => new Promise(resolve => requestAnimationFrame(resolve)));
        expect(await page.evaluate(() => window.__dmScope.toasts)).toEqual([]);
        await page.evaluate(() => window.__dmScope.appends[1].resolve("current failure"));
        await expect.poll(() => page.evaluate(() => window.__dmScope.toasts.length)).toBe(1);
        expect(await page.evaluate(() => window.__dmScope.toasts[0][0])).toContain("current failure");
    });
    async function requestClear(page) {
        await page.locator(".pm-tab").first().click({ button: "right" });
        await page.getByRole("button", { name: "Delete history", exact: true }).click();
        await expect.poll(() => page.evaluate(() => window.__dmScope.clears.length)).toBe(1);
    }
    test("clear completion preserves messages that arrived after deletion was requested", async ({ page }) => {
        await page.evaluate(() => window.__noxaChat.openPM("peer", "Peer"));
        await page.evaluate(() => window.__dmScope.loads[0].resolve([{ body: "old history", sent_at: 1 }]));
        await expect(page.locator("#chat-log")).toContainText("old history");
        await requestClear(page);
        await page.evaluate(() => { window.__dmIncoming("new arrival"); window.__dmScope.clears[0].resolve(""); });
        await expect(page.locator("#chat-log")).toContainText("new arrival");
        await expect(page.locator("#chat-log")).not.toContainText("old history");
    });
    test("late history cannot undo a successful clear", async ({ page }) => {
        await page.evaluate(() => window.__noxaChat.openPM("peer", "Peer"));
        await requestClear(page);
        await page.evaluate(() => window.__dmScope.clears[0].resolve(""));
        await expect.poll(() => page.evaluate(() => window.__dmScope.toasts.length)).toBe(1);
        await page.evaluate(() => window.__dmScope.loads[0].resolve([{ body: "deleted history", sent_at: 1 }]));
        await page.evaluate(() => new Promise(resolve => requestAnimationFrame(resolve)));
        await expect(page.locator("#chat-log")).not.toContainText("deleted history");
    });
    for (const fail of [false, true]) {
        test(`history finishing during clear is ${fail ? "retained on failure" : "discarded on success"}`, async ({ page }) => {
            await page.evaluate(() => window.__noxaChat.openPM("peer", "Peer"));
            await requestClear(page);
            await page.evaluate(() => window.__dmScope.loads[0].resolve([{ body: "loaded during clear", sent_at: 1 }]));
            await page.evaluate(() => new Promise(resolve => requestAnimationFrame(resolve)));
            await page.evaluate(fail => window.__dmScope.clears[0].resolve(fail ? "storage unavailable" : ""), fail);
            await expect.poll(() => page.evaluate(() => window.__dmScope.toasts.length)).toBe(1);
            if (fail) await expect(page.locator("#chat-log")).toContainText("loaded during clear");
            else await expect(page.locator("#chat-log")).not.toContainText("loaded during clear");
        });
    }
    test("closing a peer while enumeration is pending keeps it closed", async ({ page }) => {
        await page.evaluate(() => { window.__noxaChat.resetView(); window.__noxaChat.openPM("peer", "Peer"); });
        await page.locator(".pm-tab .pm-close").click();
        await page.evaluate(() => window.__dmScope.peers[0].resolve([{ unique_id: "peer", nickname: "Peer" }]));
        await page.evaluate(() => new Promise(resolve => requestAnimationFrame(resolve)));
        await expect(page.locator(".pm-tab")).toHaveCount(0);
        expect(await page.evaluate(() => window.__dmScope.clears.length)).toBe(0);
    });
    test("detached peer controls cannot act on a replacement peer", async ({ page }) => {
        await page.evaluate(() => {
            window.__noxaChat.openPM("peer", "Peer");
            window.__oldDMTab = document.querySelector(".pm-tab");
            window.__noxaChat.resetView();
            window.__noxaChat.openPM("peer", "Replacement peer");
            window.__oldDMTab.querySelector(".pm-close").click();
            window.__oldDMTab.click();
            window.__oldDMTab.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true }));
        });
        await expect(page.locator(".pm-tab")).toContainText("Replacement peer");
        await expect(page.getByRole("button", { name: "Delete history", exact: true })).toHaveCount(0);
        expect(await page.evaluate(() => window.__dmScope.clears.length)).toBe(0);
    });
    test("clear bridge failure is contained and leaves history available", async ({ page }) => {
        await page.evaluate(() => window.__noxaChat.openPM("peer", "Peer"));
        await page.evaluate(() => window.__dmScope.loads[0].resolve([{ body: "retained history", sent_at: 1 }]));
        await expect(page.locator("#chat-log")).toContainText("retained history");
        await requestClear(page);
        await page.evaluate(() => window.__dmScope.clears[0].reject(new Error("storage unavailable")));
        await page.evaluate(() => new Promise(resolve => requestAnimationFrame(resolve)));
        expect(await page.evaluate(() => window.__dmScope.unhandled)).toEqual([]);
        await expect(page.locator("#chat-log")).toContainText("retained history");
        await expect.poll(() => page.evaluate(() => window.__dmScope.toasts.length)).toBe(1);
    });
    test("clear confirmation cannot delete a reopened peer", async ({ page }) => {
        await page.evaluate(() => window.__noxaChat.openPM("peer", "Peer"));
        await page.locator(".pm-tab").click({ button: "right" });
        await page.evaluate(() => {
            document.querySelector(".pm-tab .pm-close").click();
            window.__noxaChat.openPM("peer", "Peer");
        });
        await page.getByRole("button", { name: "Delete history", exact: true }).click();
        await page.evaluate(() => new Promise(resolve => requestAnimationFrame(resolve)));
        expect(await page.evaluate(() => window.__dmScope.clears.length)).toBe(0);
    });
    for (const reject of [false, true]) {
        test(`old clear ${reject ? "failure" : "completion"} cannot change a reopened peer`, async ({ page }) => {
            await page.evaluate(() => window.__noxaChat.openPM("peer", "Peer"));
            await requestClear(page);
            await page.locator(".pm-tab .pm-close").click();
            await page.evaluate(() => window.__noxaChat.openPM("peer", "Peer"));
            await page.evaluate(() => window.__dmScope.loads[1].resolve([{ body: "replacement history", sent_at: 1 }]));
            await expect(page.locator("#chat-log")).toContainText("replacement history");
            await page.evaluate(reject => {
                const pending = window.__dmScope.clears[0];
                if (reject) pending.reject(new Error("obsolete clear failure"));
                else pending.resolve("");
            }, reject);
            await page.evaluate(() => new Promise(resolve => requestAnimationFrame(resolve)));
            await expect(page.locator("#chat-log")).toContainText("replacement history");
            expect(await page.evaluate(() => window.__dmScope.toasts)).toEqual([]);
            expect(await page.evaluate(() => window.__dmScope.unhandled)).toEqual([]);
        });
    }
});

test.describe("tab-bound chat signals", () => {
    test.beforeEach(async ({ page }) => {
        await page.evaluate(() => {
            const v = window.__noxa;
            v.showWorkspace(false);
            Object.assign(v.state, { activeTabID: "server-a", myUniqueID: "self", myChannelID: 1, channels: [{ ChannelID: 1, Name: "Original" }, { ChannelID: 2, Name: "Other" }] });
            const f = window.__signalScope = { nativeTab: "server-a", calls: [], effects: [], pending: [], focused: true, visible: true, hold: false, reject: false, unhandled: [] };
            document.hasFocus = () => f.focused;
            Object.defineProperty(document, "visibilityState", { configurable: true, get: () => f.visible ? "visible" : "hidden" });
            window.addEventListener("unhandledrejection", event => f.unhandled.push(String(event.reason)));
            const app = window.go.main.App;
            window.go.main.App = new Proxy(app, { get(target, key) {
                const method = key.replace(/ForTab$/, "");
                if (!["SendTyping", "SendChatRead", "SendChatDelivered"].includes(method)) return target[key];
                return async (...args) => {
                    const tab = key.endsWith("ForTab") ? args.shift() : f.nativeTab;
                    f.calls.push([method, tab, ...args]);
                    if (tab !== f.nativeTab) return "server changed";
                    f.effects.push([method, tab, ...args]);
                    if (f.reject) throw new Error("signal unavailable");
                    if (method === "SendChatRead" && f.hold) return await new Promise(resolve => f.pending.push(resolve));
                    return "";
                };
            } });
            window.__incomingDM = id => window.__noxaChat.addChat({ direct: true, enc_verified: true, from_unique_id: "peer", from: "Peer", client_msg_id: id, text: "hello " + id });
        });
    });
    test("native activation cannot redirect typing", async ({ page }) => {
        await page.evaluate(() => { window.__signalScope.nativeTab = "server-b"; });
        await page.locator("#chat-text").fill("typing on original");
        await expect.poll(() => page.evaluate(() => window.__signalScope.calls)).toEqual([["SendTyping", "server-a", 1, ""]]);
        expect(await page.evaluate(() => window.__signalScope.effects)).toEqual([]);
    });
    test("native activation cannot redirect delivery or read receipts", async ({ page }) => {
        await page.evaluate(() => {
            window.__noxaChat.openPM("peer", "Peer");
            window.__signalScope.nativeTab = "server-b";
            window.__incomingDM("message-1");
        });
        await expect.poll(() => page.evaluate(() => window.__signalScope.calls)).toEqual([
            ["SendChatDelivered", "server-a", "peer", "message-1"], ["SendChatRead", "server-a", "peer", "message-1"],
        ]);
        expect(await page.evaluate(() => window.__signalScope.effects)).toEqual([]);
    });
    test("typing scheduled in the previous channel cannot announce typing in the new one", async ({ page }) => {
        await page.locator("#chat-text").fill("original draft");
        await page.evaluate(() => { window.__noxa.state.myChannelID = 2; window.__noxaChat.onMyChannelChanged(); });
        await page.waitForTimeout(400);
        expect(await page.evaluate(() => window.__signalScope.effects)).toEqual([]);
        await page.locator("#chat-text").fill("new draft");
        await expect.poll(() => page.evaluate(() => window.__signalScope.effects)).toEqual([["SendTyping", "server-a", 2, ""]]);
    });
    test("failed queued read is retained for a later focus retry", async ({ page }) => {
        await page.evaluate(() => {
            window.__signalScope.focused = false;
            window.__incomingDM("message-1");
            window.__noxaChat.openPM("peer", "Peer");
            window.__signalScope.hold = true;
            window.__signalScope.focused = true;
            window.dispatchEvent(new Event("focus"));
        });
        await expect.poll(() => page.evaluate(() => window.__signalScope.pending.length)).toBe(1);
        await page.evaluate(() => window.__signalScope.pending[0]("write failed"));
        await page.evaluate(() => new Promise(resolve => requestAnimationFrame(resolve)));
        await page.evaluate(() => window.dispatchEvent(new Event("focus")));
        await expect.poll(() => page.evaluate(() => window.__signalScope.pending.length)).toBe(2);
        await page.evaluate(() => window.__signalScope.pending[1](""));
    });
    test("new pending read survives an older read completion without duplicate sends", async ({ page }) => {
        await page.evaluate(() => {
            window.__noxaChat.openPM("peer", "Peer");
            window.__signalScope.hold = true;
            window.__incomingDM("message-1");
        });
        await expect.poll(() => page.evaluate(() => window.__signalScope.pending.length)).toBe(1);
        await page.evaluate(() => {
            window.__incomingDM("message-2");
            window.dispatchEvent(new Event("focus"));
            window.dispatchEvent(new Event("focus"));
        });
        expect(await page.evaluate(() => window.__signalScope.pending.length)).toBe(1);
        await page.evaluate(() => window.__signalScope.pending[0](""));
        await expect.poll(() => page.evaluate(() => window.__signalScope.pending.length)).toBe(2);
        expect(await page.evaluate(() => window.__signalScope.calls.filter(call => call[0] === "SendChatRead"))).toEqual([
            ["SendChatRead", "server-a", "peer", "message-1"], ["SendChatRead", "server-a", "peer", "message-2"],
        ]);
        await page.evaluate(() => window.__signalScope.pending[1](""));
    });
    test("signal bridge failures never become unhandled rejections", async ({ page }) => {
        await page.evaluate(() => {
            window.__signalScope.reject = true;
            window.__noxaChat.openPM("peer", "Peer");
            window.__incomingDM("message-1");
        });
        await page.locator("#chat-text").fill("typing");
        await expect.poll(() => page.evaluate(() => window.__signalScope.calls.length)).toBe(3);
        await page.evaluate(() => new Promise(resolve => requestAnimationFrame(resolve)));
        expect(await page.evaluate(() => window.__signalScope.unhandled)).toEqual([]);
    });
    test("visible bursts retain every read ID while one write is pending", async ({ page }) => {
        await page.evaluate(() => {
            window.__noxaChat.openPM("peer", "Peer");
            window.__signalScope.hold = true;
            for (const id of ["one", "two", "three"]) window.__incomingDM(id);
        });
        for (let i = 0; i < 3; i++) {
            await expect.poll(() => page.evaluate(() => window.__signalScope.pending.length)).toBe(i + 1);
            await page.evaluate(i => window.__signalScope.pending[i](""), i);
        }
        expect(await page.evaluate(() => window.__signalScope.calls.filter(call => call[0] === "SendChatRead").map(call => call[3]))).toEqual(["one", "two", "three"]);
    });
    for (const hidden of ["files", "document"]) {
        test(`${hidden} visibility delays receipts until chat becomes visible`, async ({ page }) => {
            await page.evaluate(() => window.__noxaChat.openPM("peer", "Peer"));
            if (hidden === "files") await page.locator("#tab-files").click();
            else await page.evaluate(() => { window.__signalScope.visible = false; document.dispatchEvent(new Event("visibilitychange")); });
            await page.evaluate(() => window.__incomingDM("hidden-message"));
            expect(await page.evaluate(() => window.__signalScope.calls.filter(call => call[0] === "SendChatRead"))).toEqual([]);
            if (hidden === "files") await page.locator("#tab-chat").click();
            else await page.evaluate(() => { window.__signalScope.visible = true; document.dispatchEvent(new Event("visibilitychange")); });
            await expect.poll(() => page.evaluate(() => window.__signalScope.calls.filter(call => call[0] === "SendChatRead"))).toEqual([["SendChatRead", "server-a", "peer", "hidden-message"]]);
        });
    }
    test("a rejected read retries only on a later trigger", async ({ page }) => {
        await page.evaluate(() => {
            window.__noxaChat.openPM("peer", "Peer");
            window.__signalScope.reject = true;
            window.__incomingDM("message-1");
        });
        await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
        expect(await page.evaluate(() => window.__signalScope.calls.filter(call => call[0] === "SendChatRead").length)).toBe(1);
        await page.evaluate(() => { window.__signalScope.reject = false; window.dispatchEvent(new Event("focus")); });
        await expect.poll(() => page.evaluate(() => window.__signalScope.calls.filter(call => call[0] === "SendChatRead").length)).toBe(2);
        expect(await page.evaluate(() => window.__signalScope.unhandled)).toEqual([]);
    });
    test("old read completion cannot release a recreated peer's pending write", async ({ page }) => {
        await page.evaluate(() => {
            window.__noxaChat.openPM("peer", "Peer");
            window.__signalScope.hold = true;
            window.__incomingDM("old");
        });
        await expect.poll(() => page.evaluate(() => window.__signalScope.pending.length)).toBe(1);
        await page.evaluate(() => {
            window.__noxaChat.resetView();
            window.__noxa.state.activeTabID = "server-b";
            window.__noxa.state.serverGeneration++;
            window.__signalScope.nativeTab = "server-b";
            window.__noxaChat.openPM("peer", "Peer");
            window.__incomingDM("new");
        });
        await expect.poll(() => page.evaluate(() => window.__signalScope.pending.length)).toBe(2);
        await page.evaluate(() => window.__signalScope.pending[0](""));
        await page.evaluate(() => new Promise(resolve => requestAnimationFrame(resolve)));
        await page.evaluate(() => window.dispatchEvent(new Event("focus")));
        expect(await page.evaluate(() => window.__signalScope.pending.length)).toBe(2);
        await page.evaluate(() => window.__signalScope.pending[1](""));
    });
});

test.describe("tab-bound chat sending and attachments", () => {
    test.beforeEach(async ({ page }) => {
        await page.evaluate(() => {
            const v = window.__noxa;
            v.showWorkspace(false);
            Object.assign(v.state, { activeTabID: "server-a", myChannelID: 1, channels: [{ ChannelID: 1, Name: "Original" }, { ChannelID: 2, Name: "Other" }] });
            const f = window.__sendScope = { nativeTab: "server-a", calls: [], effects: [], pending: [], messages: [], hold: "" };
            v.sysMsg = (...args) => f.messages.push(args);
            v.toast = (...args) => f.messages.push(args);
            const app = window.go.main.App;
            window.go.main.App = new Proxy(app, { get(target, key) {
                const method = key.replace(/ForTab$/, "");
                if (!["SendChat", "SendChatReply", "UploadChatAttachment", "DownloadChatAttachment", "SaveChatAttachment"].includes(method)) return target[key];
                return async (...args) => {
                    const tab = key.endsWith("ForTab") ? args.shift() : f.nativeTab;
                    f.calls.push([method, tab, ...args]);
                    if (tab !== f.nativeTab) throw new Error("server changed");
                    f.effects.push([method, tab, ...args]);
                    if (f.hold === method) return await new Promise((resolve, reject) => f.pending.push({ resolve, reject }));
                    if (method === "UploadChatAttachment") return "[file:original.vcx#dGVzdA==#original.txt]";
                    if (method === "DownloadChatAttachment") return "aGVsbG8=";
                    return "";
                };
            } });
        });
    });
    async function attach(page) {
        await page.locator("#chat-file").setInputFiles({ name: "original.txt", mimeType: "text/plain", buffer: Buffer.from("original") });
        await expect(page.locator(".file-preview")).toHaveCount(1);
    }
    for (const kind of ["text", "reply", "upload", "preview", "save"]) {
        test(`native activation cannot redirect ${kind}`, async ({ page }) => {
            if (kind === "upload") await attach(page);
            if (["reply", "save"].includes(kind)) {
                await page.evaluate(kind => window.__noxaChat.addChat({ id: 71, channel_id: 1, from: "Me", text: kind === "save" ? "[file:original.vcx#dGVzdA==#original.txt]" : "parent" }), kind);
                if (kind === "reply") await page.locator('#chat-log button[title="reply"]').evaluate(button => button.click());
            }
            await page.evaluate(() => { window.__sendScope.nativeTab = "server-b"; });
            if (kind === "preview") {
                await page.evaluate(() => window.__noxaChat.addChat({ id: 72, channel_id: 1, from: "Me", text: "[file:photo.vcx#dGVzdA==#photo.png]" }));
            } else if (kind === "save") {
                await page.locator(".msg-file .file-chip").click();
            } else {
                if (kind !== "upload") await page.locator("#chat-text").fill("captured text");
                await page.locator("#chat-send").click();
            }
            await expect.poll(() => page.evaluate(() => window.__sendScope.calls.length)).toBe(1);
            expect(await page.evaluate(() => window.__sendScope.calls[0][1])).toBe("server-a");
            expect(await page.evaluate(() => window.__sendScope.effects)).toEqual([]);
        });
    }
    for (const result of ["success", "error"]) {
        test(`late send ${result} cannot clear or report into another conversation`, async ({ page }) => {
            await page.evaluate(() => { window.__sendScope.hold = "SendChat"; });
            await page.locator("#chat-text").fill("original draft");
            await page.locator("#chat-send").click();
            await expect.poll(() => page.evaluate(() => window.__sendScope.pending.length)).toBe(1);
            await page.evaluate(() => { window.__noxa.state.myChannelID = 2; window.__noxaChat.onMyChannelChanged(); });
            await page.locator("#chat-text").fill("new draft");
            await page.evaluate(result => window.__sendScope.pending[0].resolve(result === "error" ? "old private error" : ""), result);
            await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
            await expect(page.locator("#chat-text")).toHaveValue("new draft");
            expect(await page.evaluate(() => window.__sendScope.messages)).toEqual([]);
        });
    }
    test("upload completion after a channel round trip cannot send its token or stale text", async ({ page }) => {
        await attach(page);
        await page.evaluate(() => { window.__sendScope.hold = "UploadChatAttachment"; });
        await page.locator("#chat-text").fill("original draft");
        await page.locator("#chat-send").click();
        await expect.poll(() => page.evaluate(() => window.__sendScope.pending.length)).toBe(1);
        await page.evaluate(() => {
            for (const id of [2, 1]) { window.__noxa.state.myChannelID = id; window.__noxaChat.onMyChannelChanged(); }
            window.__sendScope.pending[0].resolve("[file:stale.vcx#dGVzdA==#stale.txt]");
        });
        await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
        expect(await page.evaluate(() => window.__sendScope.calls.map(call => call[0]))).toEqual(["UploadChatAttachment"]);
    });
    test("send success preserves a newer draft in the same conversation", async ({ page }) => {
        await page.evaluate(() => { window.__sendScope.hold = "SendChat"; });
        await page.locator("#chat-text").fill("first draft");
        await page.locator("#chat-send").click();
        await expect.poll(() => page.evaluate(() => window.__sendScope.pending.length)).toBe(1);
        await page.locator("#chat-text").fill("second draft");
        await page.evaluate(() => window.__sendScope.pending[0].resolve(""));
        await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
        await expect(page.locator("#chat-text")).toHaveValue("second draft");
    });
    test("editing away and back to the same draft still preserves it", async ({ page }) => {
        await page.evaluate(() => { window.__sendScope.hold = "SendChat"; });
        await page.locator("#chat-text").fill("same text");
        await page.locator("#chat-send").click();
        await expect.poll(() => page.evaluate(() => window.__sendScope.pending.length)).toBe(1);
        await page.locator("#chat-text").fill("different text");
        await page.locator("#chat-text").fill("same text");
        await page.evaluate(() => window.__sendScope.pending[0].resolve(""));
        await expect(page.locator("#chat-send")).toBeEnabled();
        await expect(page.locator("#chat-text")).toHaveValue("same text");
    });
    test("replacing a selected emoji with itself preserves the newer draft", async ({ page }) => {
        await page.locator("#chat-emoji").click();
        const emoji = await page.locator(".emoji-panel button").first().textContent();
        await page.locator("#chat-emoji").click();
        await page.evaluate(() => { window.__sendScope.hold = "SendChat"; });
        await page.locator("#chat-text").fill(emoji);
        await page.locator("#chat-send").click();
        await expect.poll(() => page.evaluate(() => window.__sendScope.pending.length)).toBe(1);
        await page.locator("#chat-emoji").click();
        await page.evaluate(() => {
            const input = document.getElementById("chat-text");
            input.setSelectionRange(0, input.value.length);
            document.querySelector(".emoji-panel button").click();
        });
        await page.evaluate(() => window.__sendScope.pending[0].resolve(""));
        await expect(page.locator("#chat-send")).toBeEnabled();
        await expect(page.locator("#chat-text")).toHaveValue(emoji);
    });
    test("old send completion cannot unlock a newer pending send", async ({ page }) => {
        await page.evaluate(() => { window.__sendScope.hold = "SendChat"; });
        await page.locator("#chat-text").fill("original");
        await page.locator("#chat-send").click();
        await expect.poll(() => page.evaluate(() => window.__sendScope.pending.length)).toBe(1);
        await page.evaluate(() => { window.__noxa.state.myChannelID = 2; window.__noxaChat.onMyChannelChanged(); });
        await page.locator("#chat-text").fill("replacement");
        await page.locator("#chat-send").click();
        await expect.poll(() => page.evaluate(() => window.__sendScope.pending.length)).toBe(2);
        await page.evaluate(async () => {
            window.__sendScope.pending[0].resolve("");
            await new Promise(resolve => requestAnimationFrame(resolve));
            void window.__noxaChat.sendMessage();
        });
        expect(await page.evaluate(() => window.__sendScope.pending.length)).toBe(2);
        await expect(page.locator("#chat-send")).toBeDisabled();
        await page.evaluate(() => window.__sendScope.pending[1].resolve(""));
        await expect(page.locator("#chat-send")).toBeEnabled();
        await expect(page.locator("#chat-text")).toHaveValue("");
    });
    for (const reject of [false, true]) {
        test(`stale attachment-token ${reject ? "rejection" : "error"} stops the remaining send batch`, async ({ page }) => {
            await attach(page);
            await page.locator("#chat-file").setInputFiles({ name: "second.txt", mimeType: "text/plain", buffer: Buffer.from("second") });
            await expect(page.locator(".file-preview")).toHaveCount(2);
            await page.evaluate(() => { window.__sendScope.hold = "SendChat"; });
            await page.locator("#chat-text").fill("text after files");
            await page.locator("#chat-send").click();
            await expect.poll(() => page.evaluate(() => window.__sendScope.pending.length)).toBe(1);
            await page.evaluate(reject => {
                window.__noxa.state.myChannelID = 2;
                window.__noxaChat.onMyChannelChanged();
                if (reject) window.__sendScope.pending[0].reject(new Error("old failure"));
                else window.__sendScope.pending[0].resolve("old failure");
            }, reject);
            await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
            expect(await page.evaluate(() => window.__sendScope.calls.map(call => call[0]))).toEqual(["UploadChatAttachment", "SendChat"]);
            expect(await page.evaluate(() => window.__sendScope.messages)).toEqual([]);
            await expect(page.locator(".file-preview")).toHaveCount(0);
        });
    }
    test("a reply chosen during upload does not replace the captured parent or get cleared", async ({ page }) => {
        await page.evaluate(() => {
            for (const id of [71, 72]) window.__noxaChat.addChat({ id, channel_id: 1, from: "Me", text: "parent " + id });
        });
        await page.locator('.msg[data-msg-id="71"] button[title="reply"]').evaluate(button => button.click());
        await attach(page);
        await page.locator("#chat-text").fill("original reply");
        await page.evaluate(() => { window.__sendScope.hold = "UploadChatAttachment"; });
        await page.locator("#chat-send").click();
        await expect.poll(() => page.evaluate(() => window.__sendScope.pending.length)).toBe(1);
        await page.locator('.msg[data-msg-id="72"] button[title="reply"]').evaluate(button => button.click());
        await page.evaluate(() => window.__sendScope.pending[0].resolve("[file:original.vcx#dGVzdA==#original.txt]"));
        await expect(page.locator("#chat-send")).toBeEnabled();
        expect(await page.evaluate(() => window.__sendScope.calls.filter(call => call[0] === "SendChatReply"))).toEqual([["SendChatReply", "server-a", "channel", "1", "original reply", 71]]);
        await expect(page.locator("#reply-bar")).toContainText("parent 72");
        await expect(page.locator("#reply-bar")).not.toHaveClass(/hidden/);
    });
    for (const error of [false, true]) {
        test(`late FileReader ${error ? "error" : "success"} cannot revive after a channel round trip`, async ({ page }) => {
            await page.evaluate(() => {
                window.FileReader = class { readAsDataURL() { window.__pendingReader = this; } };
            });
            await page.locator("#chat-file").setInputFiles({ name: "late.txt", mimeType: "text/plain", buffer: Buffer.from("late") });
            await expect.poll(() => page.evaluate(() => !!window.__pendingReader)).toBe(true);
            await page.evaluate(error => {
                for (const id of [2, 1]) { window.__noxa.state.myChannelID = id; window.__noxaChat.onMyChannelChanged(); }
                const reader = window.__pendingReader;
                reader.result = "data:text/plain;base64,bGF0ZQ==";
                if (error) reader.onerror();
                else reader.onload();
            }, error);
            await expect(page.locator(".file-preview")).toHaveCount(0);
            expect(await page.evaluate(() => window.__sendScope.messages)).toEqual([]);
        });
    }
    test("detached attachment save controls cannot invoke the native dialog", async ({ page }) => {
        await page.evaluate(() => {
            window.__noxaChat.addChat({ id: 71, channel_id: 1, from: "Me", text: "[file:original.vcx#dGVzdA==#original.txt]" });
            const button = document.querySelector(".msg-file .file-chip");
            button.remove();
            button.click();
        });
        expect(await page.evaluate(() => window.__sendScope.calls)).toEqual([]);
    });
    test("current attachment previews and save controls retain their exact source", async ({ page }) => {
        await page.evaluate(() => window.__noxaChat.addChat({ id: 71, channel_id: 1, from: "Me", text: "[file:photo.vcx#dGVzdA==#photo.png] [file:doc.vcx#dGVzdA==#doc.txt]" }));
        await expect(page.locator(".msg-img")).toHaveAttribute("src", "data:image/png;base64,aGVsbG8=");
        await page.locator(".msg-file .file-chip").click();
        expect(await page.evaluate(() => window.__sendScope.effects)).toEqual([
            ["DownloadChatAttachment", "server-a", 1, "photo.vcx", "dGVzdA=="],
            ["SaveChatAttachment", "server-a", 1, "doc.vcx", "dGVzdA==", "doc.txt"],
        ]);
    });
    test("clearing a direct target during upload stops follow-up delivery", async ({ page }) => {
        await page.evaluate(() => window.__noxaChat.openPM("peer", "Peer"));
        await attach(page);
        await page.evaluate(() => { window.__sendScope.hold = "UploadChatAttachment"; });
        await page.locator("#chat-send").click();
        await expect.poll(() => page.evaluate(() => window.__sendScope.pending.length)).toBe(1);
        await page.evaluate(() => {
            const target = document.getElementById("chat-target");
            target.value = "";
            target.dispatchEvent(new Event("change", { bubbles: true }));
            window.__sendScope.pending[0].resolve("[file:original.vcx#dGVzdA==#original.txt]");
        });
        await expect(page.locator("#chat-send")).toBeEnabled();
        await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
        expect(await page.evaluate(() => window.__sendScope.calls.map(call => call[0]))).toEqual(["UploadChatAttachment"]);
    });
});

test.describe("tab-bound chat mutations", () => {
    test.beforeEach(async ({ page }) => {
        await page.evaluate(() => {
            const v = window.__noxa;
            v.showWorkspace(false);
            Object.assign(v.state, { activeTabID: "server-a", myUniqueID: "self", myChannelID: 1, channels: [{ ChannelID: 1, Name: "Original" }, { ChannelID: 2, Name: "Other" }] });
            const f = window.__mutationScope = { nativeTab: "server-a", calls: [], effects: [], pending: [], toasts: [], hold: false };
            v.toast = (...args) => f.toasts.push(args);
            window.__unhandled = [];
            window.addEventListener("unhandledrejection", event => window.__unhandled.push(String(event.reason)));
            const app = window.go.main.App;
            window.go.main.App = new Proxy(app, { get(target, key) {
                const method = key.replace(/ForTab$/, "");
                if (method === "ChatPins") return async () => ({ pins: [{ message_id: 71, message: { body: "original message", from_nickname: "Me" } }] });
                if (!["ChatEditMessage", "ChatDeleteMessage", "ChatPinMessage", "ChatReact"].includes(method)) return target[key];
                return async (...args) => {
                    const tab = key.endsWith("ForTab") ? args.shift() : f.nativeTab;
                    f.calls.push([method, tab, ...args]);
                    if (tab !== f.nativeTab) return "server changed";
                    f.effects.push([method, tab, ...args]);
                    if (f.hold) return await new Promise((resolve, reject) => f.pending.push({ resolve, reject }));
                    return "";
                };
            } });
            window.__noxaChat.addChat({ id: 71, channel_id: 1, from_unique_id: "self", from: "Me", text: "original message" });
        });
    });
    async function mutate(page, action) {
        await page.locator("#chat-log .msg").hover();
        await page.locator(`#chat-log button[title="${action}"]`).click();
        if (action === "edit") {
            await page.locator(".msg-edit-input").fill("revised message");
            await page.locator(".msg-edit-input").press("Enter");
        } else if (action === "delete") {
            await page.getByRole("button", { name: "Delete message", exact: true }).click();
        } else if (action === "react") {
            await page.locator(".react-strip button").first().click();
        }
    }
    for (const action of ["edit", "delete", "pin", "react"]) {
        test(`native activation cannot redirect ${action}`, async ({ page }) => {
            await page.evaluate(() => { window.__mutationScope.nativeTab = "server-b"; });
            await mutate(page, action);
            await expect.poll(() => page.evaluate(() => window.__mutationScope.calls.length)).toBe(1);
            expect(await page.evaluate(() => window.__mutationScope.calls[0][1])).toBe("server-a");
            expect(await page.evaluate(() => window.__mutationScope.effects)).toEqual([]);
        });
        test(`obsolete ${action} failure cannot affect a channel round trip`, async ({ page }) => {
            await page.evaluate(() => { window.__mutationScope.hold = true; });
            await mutate(page, action);
            await expect.poll(() => page.evaluate(() => window.__mutationScope.pending.length)).toBe(1);
            await page.evaluate(() => {
                for (const id of [2, 1]) { window.__noxa.state.myChannelID = id; window.__noxaChat.onMyChannelChanged(); }
                window.__mutationScope.pending[0].resolve("old private failure");
            });
            await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
            expect(await page.evaluate(() => window.__mutationScope.toasts)).toEqual([]);
        });
    }
    test("delete confirmation cannot survive a channel round trip", async ({ page }) => {
        await page.locator("#chat-log .msg").hover();
        await page.locator('#chat-log button[title="delete"]').click();
        await page.evaluate(() => { for (const id of [2, 1]) { window.__noxa.state.myChannelID = id; window.__noxaChat.onMyChannelChanged(); } });
        await page.getByRole("button", { name: "Delete message", exact: true }).click();
        expect(await page.evaluate(() => window.__mutationScope.calls)).toEqual([]);
    });
    test("current mutations preserve exact payloads and edit submission has no blur error", async ({ page }) => {
        for (const action of ["edit", "delete", "pin", "react"]) await mutate(page, action);
        expect(await page.evaluate(() => window.__mutationScope.effects)).toEqual([
            ["ChatEditMessage", "server-a", 1, 71, "revised message", 1],
            ["ChatDeleteMessage", "server-a", 71],
            ["ChatPinMessage", "server-a", 1, 71, true],
            ["ChatReact", "server-a", 71, "👍"],
        ]);
        expect(await page.evaluate(() => window.__unhandled)).toEqual([]);
        await page.evaluate(() => window.__noxaChat.onChatReaction({ message_id: 71, reactions: { "👍": 1 }, by: "self", added: true, emoji: "👍" }));
        await expect(page.locator(".react-chip.own")).toContainText("👍 1");
    });
    test("rejected mutation promises produce current feedback without an unhandled rejection", async ({ page }) => {
        await page.evaluate(() => { window.__mutationScope.hold = true; });
        await mutate(page, "react");
        await expect.poll(() => page.evaluate(() => window.__mutationScope.pending.length)).toBe(1);
        await page.evaluate(() => window.__mutationScope.pending[0].reject(new Error("write failed")));
        await expect.poll(() => page.evaluate(() => window.__mutationScope.toasts)).toEqual([["reaction failed: Error: write failed", "warn"]]);
        expect(await page.evaluate(() => window.__unhandled)).toEqual([]);
    });
    for (const ordering of ["before", "after"]) test(`role reaction broadcast ${ordering} acknowledgement applies once`, async ({ page }) => {
        await page.evaluate(() => {
            window.__noxa.state.authorizationModel = "roles-v1";
            window.__mutationScope.hold = true;
        });
        await mutate(page, "react");
        await expect.poll(() => page.evaluate(() => window.__mutationScope.pending.length)).toBe(1);
        await expect(page.locator(".react-chip.own")).toHaveCount(0);
        if (ordering === "before") await page.evaluate(() => window.__noxaChat.onChatReaction({ message_id: 71, reactions: { "👍": 1 }, by: "self", added: true, emoji: "👍" }));
        await page.evaluate(() => window.__mutationScope.pending[0].resolve(""));
        await page.evaluate(() => new Promise(resolve => requestAnimationFrame(resolve)));
        if (ordering === "after") {
            await expect(page.locator(".react-chip.own")).toHaveCount(0);
            await page.evaluate(() => window.__noxaChat.onChatReaction({ message_id: 71, reactions: { "👍": 1 }, by: "self", added: true, emoji: "👍" }));
        }
        await expect(page.locator(".react-chip.own")).toHaveText("👍 1");
        await page.locator(".react-chip.own").click();
        await expect.poll(() => page.evaluate(() => window.__mutationScope.pending.length)).toBe(2);
        await page.evaluate(() => {
            window.__noxaChat.onChatReaction({ message_id: 71, reactions: {}, by: "self", added: false, emoji: "👍" });
            window.__mutationScope.pending[1].resolve("");
        });
        await expect(page.locator(".react-chip")).toHaveCount(0);
    });
    test("stale reaction success cannot apply an optimistic toggle after navigation", async ({ page }) => {
        await page.evaluate(() => { window.__mutationScope.hold = true; });
        await mutate(page, "react");
        await expect.poll(() => page.evaluate(() => window.__mutationScope.pending.length)).toBe(1);
        await page.evaluate(() => {
            for (const id of [2, 1]) { window.__noxa.state.myChannelID = id; window.__noxaChat.onMyChannelChanged(); }
            window.__mutationScope.pending[0].resolve("");
        });
        await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
        await expect(page.locator(".react-chip.own")).toHaveCount(0);
    });
    test("pin-panel unpin retains its server", async ({ page }) => {
        await page.locator("#chat-pins-btn").evaluate(button => button.click());
        await expect(page.locator(".pin-row")).toHaveCount(1);
        await page.evaluate(() => { window.__mutationScope.nativeTab = "server-b"; });
        await page.locator('.pin-row button[title="unpin"]').click();
        await expect.poll(() => page.evaluate(() => window.__mutationScope.calls)).toEqual([["ChatPinMessage", "server-a", 1, 71, false]]);
        expect(await page.evaluate(() => window.__mutationScope.effects)).toEqual([]);
    });
    test("reaction rechecks scope after the mutation helper resolves", async ({ page }) => {
        await page.evaluate(() => {
            const app = window.go.main.App;
            window.go.main.App = new Proxy(app, { get(target, key) {
                if (key !== "ChatReactForTab") return target[key];
                return () => Promise.resolve("");
            } });
            document.querySelector('#chat-log button[title="react"]').click();
            document.querySelector(".react-strip button").click();
            queueMicrotask(() => {
                window.__noxa.state.serverGeneration++;
            });
        });
        await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
        await expect(page.locator(".react-chip.own")).toHaveCount(0);
    });
    test("an old pins close control cannot close a replacement panel", async ({ page }) => {
        await page.locator("#chat-pins-btn").evaluate(button => button.click());
        await expect(page.locator(".pin-row")).toHaveCount(1);
        await page.evaluate(() => {
            const oldClose = document.querySelector(".pins-panel .chat-pop-head button");
            document.getElementById("chat-pins-btn").click();
            document.getElementById("chat-pins-btn").click();
            oldClose.click();
        });
        await expect(page.locator(".pins-panel")).toBeVisible();
    });
    test("closed pin-panel writes cannot show late errors", async ({ page }) => {
        await page.locator("#chat-pins-btn").evaluate(button => button.click());
        await expect(page.locator(".pin-row")).toHaveCount(1);
        await page.evaluate(() => { window.__mutationScope.hold = true; });
        await page.locator('.pin-row button[title="unpin"]').click();
        await expect.poll(() => page.evaluate(() => window.__mutationScope.pending.length)).toBe(1);
        await page.locator(".pins-panel .chat-pop-head button").click();
        await page.evaluate(() => window.__mutationScope.pending[0].resolve("old panel failure"));
        await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
        expect(await page.evaluate(() => window.__mutationScope.toasts)).toEqual([]);
        await expect(page.locator(".pins-panel")).toHaveCount(0);
    });
    test("conversation changes dismiss old pins and reaction controls", async ({ page }) => {
        await page.locator("#chat-pins-btn").evaluate(button => button.click());
        await expect(page.locator(".pin-row")).toHaveCount(1);
        await page.locator('#chat-log button[title="react"]').evaluate(button => button.click());
        await expect(page.locator(".react-strip")).toBeVisible();
        await page.evaluate(() => { window.__noxa.state.myChannelID = 2; window.__noxaChat.onMyChannelChanged(); });
        await expect(page.locator(".pins-panel")).toHaveCount(0);
        await expect(page.locator(".react-strip")).toHaveCount(0);
    });
});

test.describe("tab-bound chat search and export", () => {
    test.beforeEach(async ({ page }) => {
        await page.evaluate(() => {
            const v = window.__noxa;
            v.showWorkspace(false);
            Object.assign(v.state, { activeTabID: "server-a", myClientID: "member-a", myChannelID: 1, channels: [{ ChannelID: 1, Name: "Original" }, { ChannelID: 2, Name: "Other" }] });
            const f = window.__scanScope = { nativeTab: "server-a", calls: [], effects: [], pending: [], toasts: [], hold: false };
            v.toast = (...args) => f.toasts.push(args);
            const app = window.go.main.App;
            window.go.main.App = new Proxy(app, { get(target, key) {
                const method = key.replace(/ForTab$|ForContext$/, "");
                if (method === "ExportChatEncrypted" && f.holdSave) return async (...args) => {
                    f.holdSave = false;
                    f.saveArgs = args;
                    return await new Promise((resolve, reject) => { f.resolveSave = resolve; f.rejectSave = reject; });
                };
                if (!["ChatSearch", "ChatExportHistory", "DMSearch"].includes(method)) return target[key];
                return async (...args) => {
                    if (key.endsWith("ForContext")) args.shift();
                    const scoped = key.endsWith("ForTab");
                    const tab = scoped ? args.shift() : f.nativeTab;
                    const requestID = scoped ? args.shift() : "";
                    f.calls.push([method, tab, requestID, ...args]);
                    if (tab !== f.nativeTab) throw new Error("server changed");
                    f.effects.push([method, tab, requestID, ...args]);
                    if (f.hold) return await new Promise((resolve, reject) => f.pending.push({ resolve, reject, requestID }));
                    return method === "ChatExportHistory" ? { text: "captured transcript", messages: 1, complete: true } : { messages: [], scanned: 0 };
                };
            } });
        });
    });
    async function search(page, query = "needle") {
        await page.evaluate(query => {
            document.getElementById("chat-search-btn").click();
            const input = document.getElementById("chat-search");
            input.value = query;
            input.dispatchEvent(new Event("input", { bubbles: true }));
            document.getElementById("chat-search-server").click();
        }, query);
    }
    async function exportHistory(page) {
        await page.evaluate(() => document.getElementById("chat-export-btn").click());
        await page.locator('input[placeholder^="passphrase"]').fill("export-password");
        await page.getByRole("button", { name: "Export", exact: true }).click();
    }
    test("native activation cannot redirect search", async ({ page }) => {
        await page.evaluate(() => { window.__scanScope.nativeTab = "server-b"; });
        await search(page);
        await expect.poll(() => page.evaluate(() => window.__scanScope.calls)).toEqual([["ChatSearch", "server-a", expect.any(String), 1, "needle", 2000]]);
        expect(await page.evaluate(() => window.__scanScope.effects)).toEqual([]);
    });
    test("native activation cannot redirect export after confirmation", async ({ page }) => {
        await page.evaluate(() => { window.__scanScope.nativeTab = "server-b"; });
        await exportHistory(page);
        await expect.poll(() => page.evaluate(() => window.__scanScope.calls)).toEqual([["ChatExportHistory", "server-a", expect.any(String), 1, 0]]);
        expect(await page.evaluate(() => window.__scanScope.effects)).toEqual([]);
        expect(await page.evaluate(() => window.__calls.ExportChatEncrypted || 0)).toBe(0);
    });
    for (const reject of [false, true]) {
        test(`returning to a channel cannot revive a stale search ${reject ? "failure" : "result"}`, async ({ page }) => {
            await page.evaluate(() => { window.__scanScope.hold = true; });
            await search(page);
            await expect.poll(() => page.evaluate(() => window.__scanScope.pending.length)).toBe(1);
            await page.evaluate(() => {
                for (const id of [2, 1]) { window.__noxa.state.myChannelID = id; window.__noxaChat.onMyChannelChanged(); }
            });
            await expect(page.locator("#chat-search-server")).toBeEnabled();
            await page.evaluate(reject => {
                const pending = window.__scanScope.pending[0];
                if (reject) pending.reject(new Error("old private failure"));
                else pending.resolve({ messages: [{ id: 77, body: "old private result", from_nickname: "member" }], scanned: 1 });
            }, reject);
            await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
            await expect(page.locator(".search-results")).toHaveCount(0);
            expect(await page.evaluate(() => window.__scanScope.toasts)).toEqual([]);
        });
    }
    test("superseded search progress and completion cannot disturb a newer request", async ({ page }) => {
        await page.evaluate(() => { window.__scanScope.hold = true; });
        await search(page, "old");
        await expect.poll(() => page.evaluate(() => window.__scanScope.pending.length)).toBe(1);
        await search(page, "new");
        await expect.poll(() => page.evaluate(() => window.__scanScope.pending.length)).toBe(2);
        await page.evaluate(() => {
            const [old, current] = window.__scanScope.pending;
            for (const cb of window.__events["chatsearch:progress"] || []) {
                cb({ request_id: old.requestID, scanned: 999 });
                cb({ request_id: current.requestID, scanned: 12 });
            }
            old.resolve({ messages: [], scanned: 999 });
        });
        await expect(page.locator("#chat-search-server")).toHaveText("Searching… 12");
        await expect(page.locator("#chat-search-server")).toBeDisabled();
        await expect(page.locator(".search-results")).toHaveCount(0);
        await page.evaluate(() => window.__scanScope.pending[1].resolve({ messages: [], scanned: 12 }));
        await expect(page.locator("#chat-search-server")).toBeEnabled();
        await expect(page.locator(".search-results")).toBeVisible();
    });
    test("rendered search results close when their conversation changes", async ({ page }) => {
        await search(page);
        await expect(page.locator(".search-results")).toBeVisible();
        await page.evaluate(() => { window.__noxa.state.myChannelID = 2; window.__noxaChat.onMyChannelChanged(); });
        await expect(page.locator(".search-results")).toHaveCount(0);
    });
    test("a local DM search cannot reopen after switching peers and returning", async ({ page }) => {
        await page.evaluate(() => { window.__scanScope.hold = true; window.__noxa.openPM("peer-a", "Peer A"); });
        await search(page);
        await expect.poll(() => page.evaluate(() => window.__scanScope.pending.length)).toBe(1);
        await page.evaluate(() => { window.__noxa.openPM("peer-b", "Peer B"); window.__noxa.openPM("peer-a", "Peer A"); });
        await expect(page.locator("#chat-search-server")).toBeEnabled();
        await page.evaluate(() => window.__scanScope.pending[0].resolve({ messages: [{ id: 5, body: "old private DM search" }], scanned: 1 }));
        await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
        await expect(page.locator(".search-results")).toHaveCount(0);
    });
    test("export progress accepts only its request and saves the captured transcript", async ({ page }) => {
        await page.evaluate(() => { window.__scanScope.hold = true; });
        await exportHistory(page);
        await expect.poll(() => page.evaluate(() => window.__scanScope.pending.length)).toBe(1);
        await page.evaluate(() => {
            const request = window.__scanScope.pending[0];
            for (const cb of window.__events["chatexport:progress"]) {
                cb({ request_id: request.requestID, scanned: 12 });
                cb({ request_id: "other-request", scanned: 999 });
                cb(999);
                cb({ request_id: request.requestID, scanned: -1 });
            }
        });
        await expect(page.getByRole("dialog", { name: "Exporting chat" })).toContainText("decrypted 12 messages…");
        await page.evaluate(() => window.__scanScope.pending[0].resolve({ text: "confirmed original transcript", messages: 12, complete: true }));
        await expect.poll(() => page.evaluate(() => window.__callArgs.ExportChatEncrypted)).toEqual([["noxa-Original.noxachat", "confirmed original transcript", "export-password"]]);
        expect(await page.evaluate(() => (window.__events["chatexport:progress"] || []).length)).toBe(0);
    });
    test("plain export still requires its explicit confirmation", async ({ page }) => {
        await page.evaluate(() => document.getElementById("chat-export-btn").click());
        await page.getByRole("button", { name: "Export", exact: true }).click();
        await expect(page.getByRole("dialog", { name: "Export unencrypted chat?" })).toBeVisible();
        expect(await page.evaluate(() => window.__scanScope.calls)).toEqual([]);
        await page.getByRole("button", { name: "Export unencrypted", exact: true }).click();
        await expect.poll(() => page.evaluate(() => window.__callArgs.ExportChat)).toEqual([["noxa-Original.txt", "captured transcript"]]);
    });
    test("superseded native save errors cannot disturb a newer export", async ({ page }) => {
        await page.evaluate(() => { window.__scanScope.holdSave = true; });
        await exportHistory(page);
        await expect.poll(() => page.evaluate(() => window.__scanScope.saveArgs)).toEqual(["noxa-Original.noxachat", "captured transcript", "export-password"]);
        await page.evaluate(() => { window.__scanScope.hold = true; });
        await exportHistory(page);
        await expect.poll(() => page.evaluate(() => window.__scanScope.pending.length)).toBe(1);
        await page.evaluate(() => window.__scanScope.rejectSave(new Error("old save failure")));
        await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
        expect(await page.evaluate(() => window.__scanScope.toasts)).toEqual([]);
        await expect(page.getByRole("dialog", { name: "Exporting chat" })).toBeVisible();
    });
    for (const reason of ["channel", "close"]) {
        test(`${reason} invalidation prevents an export scan from opening a save dialog`, async ({ page }) => {
            await page.evaluate(() => { window.__scanScope.hold = true; });
            await exportHistory(page);
            await expect.poll(() => page.evaluate(() => window.__scanScope.pending.length)).toBe(1);
            if (reason === "channel") {
                await page.evaluate(() => { for (const id of [2, 1]) { window.__noxa.state.myChannelID = id; window.__noxaChat.onMyChannelChanged(); } });
            } else {
                await page.keyboard.press("Escape");
            }
            await page.evaluate(() => window.__scanScope.pending[0].resolve({ text: "stale transcript", messages: 1, complete: true }));
            await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
            expect(await page.evaluate(() => window.__calls.ExportChatEncrypted || 0)).toBe(0);
            expect(await page.evaluate(() => (window.__events["chatexport:progress"] || []).length)).toBe(0);
        });
    }
});

test.describe("tab-bound chat history and pins", () => {
    test.beforeEach(async ({ page }) => {
        await page.evaluate(() => {
            const v = window.__noxa;
            v.showWorkspace(false);
            Object.assign(v.state, { activeTabID: "server-a", myClientID: "member-a", myChannelID: 1, channels: [{ ChannelID: 1, Name: "Room" }] });
            const f = window.__chatReadScope = { nativeTab: "server-a", calls: [], effects: [], pins: { pins: [] } };
            const app = window.go.main.App;
            window.go.main.App = new Proxy(app, { get(target, key) {
                const method = key.replace(/ForTab$/, "");
                if (!["ChatHistory", "ChatPins"].includes(method)) return target[key];
                return async (...args) => {
                    const tab = key.endsWith("ForTab") ? args.shift() : f.nativeTab;
                    f.calls.push([method, tab, ...args]);
                    if (tab !== f.nativeTab) throw new Error("server changed");
                    f.effects.push([method, tab, ...args]);
                    const result = structuredClone(method === "ChatPins" ? f.pins : (f.history || { messages: [] }));
                    if (method === "ChatPins" && f.gate) { const gate = f.gate; f.gate = null; await gate; }
                    if (method === "ChatHistory" && f.historyGate) { const gate = f.historyGate; f.historyGate = null; await gate; }
                    if (f.reject) throw new Error("old private error");
                    return result;
                };
            } });
        });
    });
    test("native activation cannot redirect history or pin preload", async ({ page }) => {
        await page.evaluate(() => {
            window.__chatReadScope.nativeTab = "server-b";
            window.__noxaChat.onMyChannelChanged();
        });
        await expect.poll(() => page.evaluate(() => window.__chatReadScope.calls)).toEqual([
            ["ChatPins", "server-a", 1], ["ChatHistory", "server-a", 1, 0, 50],
        ]);
        expect(await page.evaluate(() => window.__chatReadScope.effects)).toEqual([]);
    });
    test("native activation cannot redirect the pins panel", async ({ page }) => {
        await page.evaluate(() => { window.__chatReadScope.nativeTab = "server-b"; document.getElementById("chat-pins-btn").click(); });
        await expect.poll(() => page.evaluate(() => window.__chatReadScope.calls)).toEqual([["ChatPins", "server-a", 1]]);
        expect(await page.evaluate(() => window.__chatReadScope.effects)).toEqual([]);
    });
    test("late history cannot rerender the replacement server", async ({ page }) => {
        await page.evaluate(() => {
            const f = window.__chatReadScope;
            f.history = { messages: [{ id: 77, body: "old private history", from_nickname: "old member" }] };
            f.historyGate = new Promise(resolve => { f.releaseHistory = resolve; });
            window.__noxaChat.onMyChannelChanged();
        });
        await expect.poll(() => page.evaluate(() => window.__chatReadScope.calls.length)).toBe(2);
        await page.evaluate(() => {
            window.__noxaChat.resetView();
            window.__noxa.state.activeTabID = "server-b";
            window.__noxa.state.serverGeneration++;
            window.__chatReadScope.nativeTab = "server-b";
            window.__chatReadScope.history = { messages: [{ id: 10, body: "replacement history", from_nickname: "new member" }] };
            window.__noxaChat.onMyChannelChanged();
        });
        await expect(page.locator("#chat-log")).toContainText("replacement history");
        await page.evaluate(() => {
            const f = window.__chatReadScope;
            f.mutations = 0;
            f.observer = new MutationObserver(records => { f.mutations += records.length; });
            f.observer.observe(document.getElementById("chat-log"), { childList: true, subtree: true });
            f.releaseHistory();
        });
        await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
        expect(await page.evaluate(() => window.__chatReadScope.mutations)).toBe(0);
        await expect(page.locator("#chat-log")).not.toContainText("old private");
    });
    for (const newer of ["panel", "event"]) {
        test(`pin preload cannot undo a newer ${newer} result`, async ({ page }) => {
            await page.evaluate(() => {
                const f = window.__chatReadScope;
                f.history = { messages: [{ id: 88, body: "unrelated pin", from_nickname: "member" }, { id: 77, body: "message to pin", from_nickname: "member" }] };
                f.pins = { pins: [{ message_id: 77 }, { message_id: 88 }] };
                f.gate = new Promise(resolve => { f.release = resolve; });
                window.__noxaChat.onMyChannelChanged();
            });
            await expect(page.locator('#chat-log .msg[data-msg-id="77"]')).toBeVisible();
            await page.evaluate(newer => {
                window.__chatReadScope.pins = { pins: [] };
                if (newer === "panel") document.getElementById("chat-pins-btn").click();
                else window.__noxaChat.onChatPinned({ channel_id: 1, message_id: 77 }, false);
            }, newer);
            if (newer === "panel") await expect(page.locator(".pins-panel")).toContainText("Nothing pinned yet");
            await page.evaluate(() => { window.__chatReadScope.release(); });
            await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
            await page.locator('#chat-log .msg[data-msg-id="77"] button[title="pin"]').evaluate(button => button.click());
            await expect.poll(() => page.evaluate(() => window.__callArgs.ChatPinMessageForTab)).toEqual([["server-a", 1, 77, true]]);
            if (newer === "event") {
                await page.locator('#chat-log .msg[data-msg-id="88"] button[title="pin"]').evaluate(button => button.click());
                await expect.poll(() => page.evaluate(() => window.__callArgs.ChatPinMessageForTab)).toEqual([["server-a", 1, 77, true], ["server-a", 1, 88, false]]);
            }
        });
    }
    for (const reject of [false, true]) {
        test(`late pin ${reject ? "failure" : "content"} cannot reach a replacement panel`, async ({ page }) => {
            await page.evaluate(() => {
                const f = window.__chatReadScope;
                f.pins = { pins: [{ message_id: 77, message: { body: "old private pin", from_nickname: "old member" } }] };
                f.gate = new Promise(resolve => { f.release = resolve; });
                document.getElementById("chat-pins-btn").click();
            });
            await expect.poll(() => page.evaluate(() => window.__chatReadScope.calls.length)).toBe(1);
            await page.evaluate(() => {
                window.__noxaChat.resetView();
                window.__noxa.state.activeTabID = "server-b";
                window.__noxa.state.serverGeneration++;
                window.__chatReadScope.nativeTab = "server-b";
                window.__chatReadScope.pins = { pins: [] };
                document.getElementById("chat-pins-btn").click();
            });
            await expect(page.locator(".pins-panel")).toContainText("Nothing pinned yet");
            await page.evaluate(reject => { window.__chatReadScope.reject = reject; window.__chatReadScope.release(); }, reject);
            // Drain the released async callback before inspecting the replacement.
            await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
            await expect(page.locator(".pins-panel")).not.toContainText("old private");
        });
    }
});

test.describe("acknowledged role ban removal", () => {
    test.beforeEach(async ({ page }) => {
        await page.evaluate(() => {
            const v = window.__noxa;
            v.showWorkspace(false);
            v.state.activeTabID = "server-a";
            v.state.authorizationModel = "roles-v1";
            const f = window.__roleBan = { nativeTab: "server-a", lists: 0, calls: [], effects: 0, completed: 0, rows: [{ id: 17, value: "banned-account", reason: "reason", banned_by: "moderator", expires_at: 0 }] };
            const app = window.go.main.App;
            window.go.main.App = new Proxy(app, { get(target, key) {
                if (key === "BanRemoveForTab" || key === "BanRemove") return () => { throw new Error("unacknowledged role removal"); };
                if (key === "BanListForTab") return async tab => {
                    if (tab !== f.nativeTab) throw new Error("server changed");
                    f.lists++;
                    if (f.failReload) throw new Error("list unavailable");
                    return { bans: structuredClone(f.rows) };
                };
                if (key === "RemoveRoleBanForTab") return async (tab, id) => {
                    f.calls.push([tab, id]);
                    if (tab !== f.nativeTab) throw new Error("server changed");
                    if (f.gate) await f.gate;
                    f.completed++;
                    if (f.reject) throw new Error("private error");
                    f.effects++; f.rows = []; return { ban_id: id };
                };
                return target[key];
            } });
            window.__noxaPerms.openBanList();
        });
        await expect(page.locator(".ban-lift")).toHaveCount(1);
    });
    async function lift(page) {
        await page.locator(".ban-lift").click();
        await page.getByRole("dialog", { name: "Lift ban", exact: true }).getByRole("button", { name: "Lift", exact: true }).click();
    }
    test("waits for committed deletion before refreshing the list", async ({ page }) => {
        await page.evaluate(() => { const f = window.__roleBan; f.gate = new Promise(resolve => { f.release = resolve; }); });
        await lift(page);
        await expect.poll(() => page.evaluate(() => window.__roleBan.calls)).toEqual([["server-a", 17]]);
        await expect(page.locator(".ban-lift")).toBeDisabled();
        await expect(page.getByRole("button", { name: "Reload bans", exact: true })).toBeDisabled();
        expect(await page.evaluate(() => window.__roleBan.lists)).toBe(1);
        await page.evaluate(() => window.__roleBan.release());
        await expect(page.locator(".ban-lift")).toHaveCount(0);
        expect(await page.evaluate(() => window.__roleBan.lists)).toBe(2);
    });
    test("uncertain deletion requires an explicit reload", async ({ page }) => {
        await page.evaluate(() => { window.__roleBan.reject = true; });
        await lift(page);
        await expect(page.getByRole("status").filter({ hasText: "Removal could not be confirmed" })).toBeVisible();
        await expect(page.locator(".ban-lift")).toBeDisabled();
        expect(await page.evaluate(() => window.__roleBan.lists)).toBe(1);
        await page.getByRole("button", { name: "Reload bans", exact: true }).click();
        await expect(page.locator(".ban-lift")).toBeEnabled();
        expect(await page.evaluate(() => window.__roleBan.lists)).toBe(2);
    });
    test("failed refresh after removal clears pending feedback and permits reload", async ({ page }) => {
        await page.evaluate(() => { window.__roleBan.failReload = true; });
        await lift(page);
        await expect(page.getByText("ban list failed: Error: list unavailable", { exact: true })).toBeVisible();
        await expect(page.getByRole("status").filter({ hasText: "Updating bans" })).toHaveCount(0);
        await expect(page.getByRole("button", { name: "Reload bans", exact: true })).toBeEnabled();
        expect(await page.evaluate(() => window.__roleBan.effects)).toBe(1);
        await page.evaluate(() => { window.__roleBan.failReload = false; });
        await page.getByRole("button", { name: "Reload bans", exact: true }).click();
        await expect(page.getByText("no bans", { exact: true })).toBeVisible();
    });
    test("native activation cannot redirect removal before frontend reset", async ({ page }) => {
        await page.evaluate(() => { window.__roleBan.nativeTab = "server-b"; });
        await lift(page);
        await expect.poll(() => page.evaluate(() => window.__roleBan.calls)).toEqual([["server-a", 17]]);
        expect(await page.evaluate(() => window.__roleBan.effects)).toBe(0);
        await expect(page.locator(".ban-lift")).toBeDisabled();
    });
    test("late rejection cannot revive the previous server dialog or reload it", async ({ page }) => {
        await page.evaluate(() => { const f = window.__roleBan; f.reject = true; f.gate = new Promise(resolve => { f.release = resolve; }); });
        await lift(page);
        await expect.poll(() => page.evaluate(() => window.__roleBan.calls.length)).toBe(1);
        await page.evaluate(() => {
            for (const callback of window.__events.tab_reset || []) callback("server-b");
            window.__roleBan.release();
        });
        await expect.poll(() => page.evaluate(() => window.__roleBan.completed)).toBe(1);
        await expect(page.getByRole("dialog")).toHaveCount(0);
        expect(await page.evaluate(() => window.__roleBan.lists)).toBe(1);
    });
});

test.describe("tab-bound channel placement", () => {
    test.beforeEach(async ({ page }) => {
        await page.evaluate(() => {
            const v = window.__noxa;
            v.showWorkspace(false);
            v.state.activeTabID = "server-a";
            v.state.myClientID = "self";
            v.state.authorizationModel = "roles-v1";
            v.state.channels = [{ ChannelID: 1, ParentID: 0, Name: "First", OrderIndex: 0 },
                { ChannelID: 2, ParentID: 0, Name: "Second", OrderIndex: 10 },
                { ChannelID: 4, ParentID: 0, Name: "Parent", OrderIndex: 20 },
                { ChannelID: 3, ParentID: 4, Name: "Child", OrderIndex: 30 }];
            const f = window.__placement = { nativeTab: "server-a", calls: [], changes: [], channels: structuredClone(v.state.channels) };
            const app = window.go.main.App;
            window.go.main.App = new Proxy(app, { get(target, key) {
                if (["ChannelEditTree", "RoleChannelState", "ChangeRoleChannel"].includes(key)) return () => { throw new Error("unscoped placement"); };
                if (!["ChannelEditTreeForTab", "RoleChannelStateForTab", "ChangeRoleChannelForTab"].includes(key)) return target[key];
                return async (tabID, ...args) => {
                    f.calls.push({ key, tabID, args });
                    if (tabID !== f.nativeTab) throw new Error("server changed");
                    if (key === "RoleChannelStateForTab") return {
                        revision: 8, channel_id: 1, name: "Current name", can_create_permanent: true,
                        destinations: f.destinations || [{ id: 0, name: "Root", can_sync: true }, { id: 4, name: "Parent", can_sync: true }],
                        settings: { name: "Current name", topic: "Fresh topic", description: "", order_index: f.order ?? 0, max_clients: 0, slow_mode_seconds: 0, opus_bitrate: 0, opus_fec: false, opus_dtx: false, opus_stereo: false },
                    };
                    f.changes.push(args);
                    if (f.gate) await f.gate;
                    return key === "ChangeRoleChannelForTab" ? { revision: 9, channel_id: 1 } : "";
                };
            } });
            v.renderTree();
        });
    });

    const drag = (page, target) => page.locator('.channel[data-chid="1"]').dragTo(page.locator(`.channel[data-chid="${target}"]`));

    test("same-parent role drag reviews a fresh settings edit and waits for acknowledgement", async ({ page }) => {
        await drag(page, 2);
        const dialog = page.locator(".channel-lifecycle-dialog");
        await expect(dialog.getByLabel("Topic", { exact: true })).toHaveValue("Fresh topic");
        await expect(dialog.getByLabel("Sort order", { exact: true })).toHaveValue("11");
        await expect(dialog.getByLabel("Sort order", { exact: true })).toBeVisible();
        expect(await page.evaluate(() => window.__placement.changes)).toEqual([]);
        await page.evaluate(() => { window.__placement.gate = new Promise(resolve => { window.__placement.release = resolve; }); });
        await dialog.getByRole("button", { name: "Save changes", exact: true }).click();
        await expect.poll(() => page.evaluate(() => window.__placement.changes.length)).toBe(1);
        await expect(dialog.getByRole("button", { name: "Save changes", exact: true })).toBeDisabled();
        const request = await page.evaluate(() => window.__placement.changes[0][0]);
        expect(request).toMatchObject({ kind: "channel_edit", channel_id: 1, expected_revision: 8, settings: { order_index: 11, topic: "Fresh topic" } });
        expect(request).not.toHaveProperty("parent_id");
        await page.evaluate(() => window.__placement.release());
        await expect(dialog).toHaveCount(0);
    });

    test("cross-parent role drag sends order and destination together while keeping access", async ({ page }) => {
        await drag(page, 3);
        const dialog = page.locator(".channel-lifecycle-dialog");
        await expect(dialog.getByRole("combobox", { name: "Destination", exact: true })).toHaveValue("4");
        await expect(dialog.getByRole("combobox", { name: "Channel access", exact: true })).toHaveValue("keep");
        await expect(dialog.getByLabel("Sort order", { exact: true })).toHaveValue("31");
        await dialog.getByRole("button", { name: "Move", exact: true }).click();
        await expect.poll(() => page.evaluate(() => window.__placement.changes.length)).toBe(1);
        expect(await page.evaluate(() => window.__placement.changes[0][0])).toEqual({ kind: "channel_move", expected_revision: 8, channel_id: 1, parent_id: 4, sync_to_parent: false, order_index: 31 });
    });

    test("cross-parent move preserves an explicit zero order", async ({ page }) => {
        await page.evaluate(() => { window.__placement.order = 9; });
        await drag(page, 3);
        const dialog = page.locator(".channel-lifecycle-dialog");
        await dialog.getByLabel("Sort order", { exact: true }).fill("0");
        await dialog.getByRole("button", { name: "Move", exact: true }).click();
        await expect.poll(() => page.evaluate(() => window.__placement.changes.length)).toBe(1);
        expect(await page.evaluate(() => window.__placement.changes[0][0])).toEqual({ kind: "channel_move", expected_revision: 8, channel_id: 1, parent_id: 4, sync_to_parent: false, order_index: 0 });
    });

    test("an unavailable drop destination is never replaced by the first allowed destination", async ({ page }) => {
        await page.evaluate(() => { window.__placement.destinations = [{ id: 0, name: "Root" }]; });
        await drag(page, 3);
        const dialog = page.locator(".channel-lifecycle-dialog");
        await expect(dialog.getByRole("combobox", { name: "Destination", exact: true })).toHaveValue("");
        await expect(dialog).toContainText("That destination is no longer available. Choose another destination.");
        await expect(dialog.getByRole("button", { name: "Move", exact: true })).toBeDisabled();
        await dialog.getByRole("combobox", { name: "Destination", exact: true }).selectOption("0");
        await expect(dialog.getByRole("button", { name: "Move", exact: true })).toBeEnabled();
    });

    test("overflowing drop order has no native effects", async ({ page }) => {
        await page.evaluate(() => { window.__noxa.state.channels[1].OrderIndex = 2147483647; window.__noxa.renderTree(); });
        await drag(page, 2);
        expect(await page.evaluate(() => window.__placement.calls)).toEqual([]);
        await expect(page.getByText("This channel is at the sort-order limit. Adjust its sort order in channel settings first.", { exact: true })).toBeVisible();
    });

    for (const mode of ["roles-v1"]) {
        test(`${mode} channel drop rejects native activation before frontend reset`, async ({ page }) => {
            await page.evaluate(model => { window.__noxa.state.authorizationModel = model; window.__placement.nativeTab = "server-b"; }, mode);
            await drag(page, 2);
            await expect.poll(() => page.evaluate(() => window.__placement.calls.length)).toBe(1);
            expect(await page.evaluate(() => window.__placement.calls[0].tabID)).toBe("server-a");
            expect(await page.evaluate(() => window.__placement.changes)).toEqual([]);
        });
    }

    test("returning to the source tab cannot revive a channel drag", async ({ page }) => {
        await page.evaluate(() => {
            const transfer = new DataTransfer();
            document.querySelector('.channel[data-chid="1"]').dispatchEvent(new DragEvent("dragstart", { dataTransfer: transfer, bubbles: true }));
            for (const tab of ["server-b", "server-a"]) for (const callback of window.__events.tab_reset || []) callback(tab);
            window.__noxa.state.channels = window.__placement.channels;
            window.__noxa.renderTree();
            document.querySelector('.channel[data-chid="2"]').dispatchEvent(new DragEvent("drop", { dataTransfer: transfer, bubbles: true }));
        });
        expect(await page.evaluate(() => window.__placement.calls)).toEqual([]);
    });
});

test.describe("tab-bound custom emoji", () => {
    test.beforeEach(async ({ page }) => {
        await page.evaluate(async () => {
            const v = window.__noxa;
            v.showWorkspace(false);
            v.state.activeTabID = "server-a";
            v.state.myClientID = "self";
            const f = window.__emoji = { nativeTab: "server-a", calls: [], effects: [], toasts: [], names: ["wave", "hello"], completed: [] };
            v.toast = text => f.toasts.push(text);
            const app = window.go.main.App;
            const methods = ["EmojiList", "EmojiGet", "EmojiUpload", "EmojiRename", "EmojiDelete"];
            window.go.main.App = new Proxy(app, { get(target, key) {
                if (methods.includes(key)) return () => { throw new Error("unscoped emoji call"); };
                if (!methods.some(name => key === name + "ForTab")) return target[key];
                return async (tabID, ...args) => {
                    const name = key.slice(0, -"ForTab".length);
                    f.calls.push({ name, tabID, args });
                    if (tabID !== f.nativeTab) throw new Error("server changed");
                    const names = [...f.names];
                    if (!["EmojiList", "EmojiGet"].includes(name)) f.effects.push({ name, args });
                    if (f.gated === name && (!f.gatedArg || f.gatedArg === args[0])) await f.gate;
                    f.completed.push(name);
                    if (f.reject === name) throw new Error("old rejection");
                    if (name === "EmojiList") return { emojis: names.map(name => ({ name })) };
                    if (name === "EmojiGet") return { content_type: "image/png", data_base64: "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=" };
                    return f.error || "";
                };
            } });
            const chat = await import("/src/chat-ui.js");
            chat.onEmojiAdded();
        });
    });

    const openManager = async page => {
        await page.locator("#tab-files").click();
        await page.locator(".fb-emoji").click();
    };

    test("manager lists and previews only its opening server", async ({ page }) => {
        await openManager(page);
        await expect(page.locator(".em-row img").first()).toHaveAttribute("src", /^data:image\/png;base64,/);
        expect(await page.evaluate(() => window.__emoji.calls.every(c => c.tabID === "server-a"))).toBe(true);
        await page.locator(".em-close").click();
        await page.evaluate(() => { window.__emoji.nativeTab = "server-b"; });
        await page.locator(".fb-emoji").click();
        await expect(page.locator(".em-list")).toContainText("server changed");
        expect(await page.evaluate(() => window.__emoji.calls.filter(c => c.name === "EmojiList").map(c => c.tabID))).toEqual(["server-a", "server-a"]);
    });

    for (const action of ["rename", "delete"]) {
        test(`${action} waits for storage and does not refresh after rejection`, async ({ page }) => {
            await openManager(page);
            await page.evaluate(action => {
                const f = window.__emoji;
                f.gated = action === "rename" ? "EmojiRename" : "EmojiDelete";
                f.error = "asset storage failed";
                f.gate = new Promise(resolve => { f.release = resolve; });
            }, action);
            const name = action === "rename" ? "Rename custom emoji" : "Delete emoji";
            await page.locator(".em-row").first().getByRole("button", { name, exact: true }).click();
            if (action === "rename") await page.getByRole("dialog", { name }).getByRole("textbox").fill("renamed");
            await page.getByRole("dialog").last().getByRole("button", { name: action === "rename" ? "Rename" : "Delete emoji", exact: true }).click();
            await expect.poll(() => page.evaluate(() => window.__emoji.effects.length)).toBe(1);
            expect(await page.evaluate(() => window.__emoji.completed.includes(window.__emoji.gated))).toBe(false);
            expect(await page.evaluate(() => window.__emoji.calls.filter(c => c.name === "EmojiList").length)).toBe(1);
            expect(await page.evaluate(() => window.__emoji.toasts)).toEqual([]);
            await page.evaluate(() => window.__emoji.release());
            await expect.poll(() => page.evaluate(() => window.__emoji.toasts.join(" "))).toContain("asset storage failed");
            await expect(page.locator(".em-row")).toHaveCount(2);
            expect(await page.evaluate(() => window.__emoji.calls.filter(c => c.name === "EmojiList").length)).toBe(1);
        });

        test(`${action} prompt cannot mutate another native tab`, async ({ page }) => {
            await openManager(page);
            const name = action === "rename" ? "Rename custom emoji" : "Delete emoji";
            await page.locator(".em-row").first().getByRole("button", { name, exact: true }).click();
            if (action === "rename") await page.getByRole("dialog", { name }).getByRole("textbox").fill("renamed");
            await page.evaluate(() => { window.__emoji.nativeTab = "server-b"; });
            await page.getByRole("dialog").last().getByRole("button", { name: action === "rename" ? "Rename" : "Delete emoji", exact: true }).click();
            await expect.poll(() => page.evaluate(() => window.__emoji.toasts.length)).toBe(1);
            expect(await page.evaluate(() => window.__emoji.effects)).toEqual([]);
            expect(await page.evaluate(() => window.__emoji.calls.at(-1).tabID)).toBe("server-a");
        });
    }

    for (const surface of ["manager", "picker"]) {
        test(`${surface} upload retains its tab through the image and name prompts`, async ({ page }) => {
            if (surface === "manager") {
                await openManager(page);
                await page.locator(".em-add").click();
                await page.getByRole("dialog", { name: "Upload custom emoji", exact: true }).getByRole("textbox").fill("new_emoji");
            } else await page.locator("#chat-emoji").click();
            const chooserPromise = page.waitForEvent("filechooser");
            if (surface === "manager") await page.getByRole("button", { name: "Continue", exact: true }).click();
            else await page.locator(".emoji-upload").click();
            const chooser = await chooserPromise;
            await page.evaluate(() => { window.__emoji.nativeTab = "server-b"; });
            await chooser.setFiles({ name: "pixel.png", mimeType: "image/png", buffer: Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=", "base64") });
            if (surface === "picker") {
                await page.getByRole("dialog", { name: "Upload custom emoji", exact: true }).getByRole("textbox").fill("new_emoji");
                await page.getByRole("button", { name: "Upload", exact: true }).click();
            }
            await expect.poll(() => page.evaluate(() => window.__emoji.calls.filter(c => c.name === "EmojiUpload").length)).toBe(1);
            expect(await page.evaluate(() => window.__emoji.effects)).toEqual([]);
            expect(await page.evaluate(() => window.__emoji.calls.find(c => c.name === "EmojiUpload").tabID)).toBe("server-a");
            await expect.poll(() => page.evaluate(() => window.__emoji.toasts.length)).toBe(1);
        });
    }

    test("closing the manager cancels a scheduled mutation refresh", async ({ page }) => {
        await page.clock.install();
        await openManager(page);
        await page.locator(".em-row").first().getByRole("button", { name: "Delete emoji", exact: true }).click();
        await page.getByRole("dialog").last().getByRole("button", { name: "Delete emoji", exact: true }).click();
        await expect.poll(() => page.evaluate(() => window.__emoji.effects.length)).toBe(1);
        await page.locator(".em-close").click();
        await page.evaluate(() => { window.__emoji.nativeTab = "server-b"; });
        await page.clock.runFor(500);
        expect(await page.evaluate(() => window.__emoji.calls.filter(c => c.name === "EmojiList").length)).toBe(1);
    });

    test("late mutation rejection cannot toast or refresh after reset", async ({ page }) => {
        await openManager(page);
        await page.evaluate(() => {
            const f = window.__emoji;
            f.gated = f.reject = "EmojiDelete";
            f.gate = new Promise(resolve => { f.release = resolve; });
        });
        await page.locator(".em-row").first().getByRole("button", { name: "Delete emoji", exact: true }).click();
        await page.getByRole("dialog").last().getByRole("button", { name: "Delete emoji", exact: true }).click();
        await expect.poll(() => page.evaluate(() => window.__emoji.effects.length)).toBe(1);
        await page.evaluate(() => {
            for (const callback of window.__events.tab_reset || []) callback("server-b");
            window.__emoji.release();
        });
        await expect.poll(() => page.evaluate(() => window.__emoji.completed.includes("EmojiDelete"))).toBe(true);
        expect(await page.evaluate(() => window.__emoji.toasts)).toEqual([]);
        expect(await page.evaluate(() => window.__emoji.calls.filter(c => c.name === "EmojiList").length)).toBe(1);
    });

    test("late emoji replies cannot attach the previous message to a replacement reaction strip", async ({ page }) => {
        await page.evaluate(() => {
            const f = window.__emoji;
            f.gated = "EmojiGet";
            f.gatedArg = "wave";
            f.gate = new Promise(resolve => { f.release = resolve; });
            window.__noxa.state.myChannelID = 42;
            window.__noxa.state.channels = [{ ChannelID: 42, Name: "Chat" }];
            for (const id of [1, 2]) {
                for (const callback of window.__events.event || []) callback(JSON.stringify({ type: "chat", data: { id, channel_id: 42, from: "Bob", from_unique_id: "peer", text: "Message " + id } }));
            }
        });
        await expect.poll(() => page.evaluate(() => window.__emoji.completed.includes("EmojiList"))).toBe(true);
        await page.locator('.msg[data-msg-id="1"]').hover();
        await page.locator('.msg[data-msg-id="1"]').getByRole("button", { name: "react", exact: true }).click();
        await expect.poll(() => page.evaluate(() => window.__emoji.calls.some(c => c.name === "EmojiGet" && c.args[0] === "wave"))).toBe(true);
        await page.locator('.msg[data-msg-id="2"]').hover();
        await page.locator('.msg[data-msg-id="2"]').getByRole("button", { name: "react", exact: true }).click();
        await expect(page.locator('.react-strip img[alt=":hello:"]')).toBeVisible();
        await page.evaluate(() => window.__emoji.release());
        await expect.poll(() => page.evaluate(() => window.__emoji.completed.filter(n => n === "EmojiGet").length)).toBe(2);
        // If the late first image is present, its action must belong to the
        // current message too; the previous loop used a mutable global strip.
        const wave = page.locator('.react-strip img[alt=":wave:"]');
        if (await wave.count()) await wave.click();
        else await page.locator('.react-strip img[alt=":hello:"]').click();
        await expect.poll(() => page.evaluate(() => window.__callArgs.ChatReactForTab?.at(-1)?.[1])).toBe(2);
    });

    test("a pending picker loop cannot fetch remaining old emoji on a new server", async ({ page }) => {
        await page.evaluate(() => {
            const f = window.__emoji;
            f.gated = "EmojiGet";
            f.gatedArg = "wave";
            f.gate = new Promise(resolve => { f.release = resolve; });
        });
        await page.locator("#chat-emoji").click();
        await expect.poll(() => page.evaluate(() => window.__emoji.calls.some(c => c.name === "EmojiGet" && c.args[0] === "wave"))).toBe(true);
        await page.evaluate(() => {
            for (const callback of window.__events.tab_reset || []) callback("server-b");
            window.__emoji.nativeTab = "server-b";
            window.__emoji.names = ["new"];
            window.__noxa.showWorkspace(false);
        });
        await page.locator("#chat-emoji").click();
        await expect(page.locator('.emoji-panel img[alt=":new:"]')).toBeVisible();
        await page.evaluate(() => window.__emoji.release());
        await expect.poll(() => page.evaluate(() => window.__emoji.completed.filter(n => n === "EmojiGet").length)).toBe(2);
        expect(await page.evaluate(() => window.__emoji.calls.some(c => c.name === "EmojiGet" && c.tabID === "server-b" && c.args[0] === "hello"))).toBe(false);
        await expect(page.locator('.emoji-panel img[alt=":wave:"]')).toHaveCount(0);
    });
});

test.describe("tab-bound metadata and channel icons", () => {
    test.beforeEach(async ({ page }) => {
        await page.evaluate(() => {
            const v = window.__noxa;
            v.showWorkspace(false);
            v.state.activeTabID = "server-a";
            v.state.myClientID = "self";
            v.state.myChannelID = 1;
            v.state.channels = [{ ChannelID: 1, Name: "Lobby", ParentID: 0 }, { ChannelID: 2, Name: "Other", ParentID: 0, HasIcon: true }];
            v.recentChannels = () => [2];
            const f = window.__metadata = { nativeTab: "server-a", calls: [], effects: [], toasts: [], notices: [] };
            v.toast = text => f.toasts.push(text);
            v.sysMsg = text => f.notices.push(text);
            const app = window.go.main.App;
            const names = ["GetClientInfo", "ServerInfo", "MOTD", "Subscriptions", "ChannelIconGet", "ChannelIconSet", "JoinChannel"];
            window.go.main.App = new Proxy(app, { get(target, key) {
                if (names.includes(key)) return () => { throw new Error("unscoped metadata call"); };
                if (key === "DMHistoryContextForTab") return async (tabID) => {
                    if (tabID !== "server-a") return target[key](tabID);
                    f.calls.push({ name: "DMHistoryContext", tabID, args: [] });
                    if (f.gated === "DMHistoryContext") await f.gate;
                    return { tab_id: tabID, identity_uid: "old-identity", activation: "0", identity_revision: "0" };
                };
                if (!names.some(name => key === name + "ForTab")) return target[key];
                return async (tabID, ...args) => {
                    const name = key.slice(0, -"ForTab".length);
                    f.calls.push({ name, tabID, args });
                    if (tabID !== f.nativeTab) throw new Error("server changed");
                    if (["ChannelIconSet", "JoinChannel"].includes(name)) f.effects.push({ name, args });
                    if (f.gated === name) await f.gate;
                    if (f.reject === name) throw new Error("old rejection");
                    if (name === "GetClientInfo") return { nickname: "Old Alice", unique_id: "old", ping_ms: 12 };
                    if (name === "ServerInfo") return { name: "Old server", uptime_seconds: 60 };
                    if (name === "MOTD") return "old notice";
                    if (name === "Subscriptions") return [2];
                    if (name === "ChannelIconGet") return f.icon || {};
                    return "";
                };
            } });
            v.renderTree();
        });
    });

    test("metadata dialogs and news reject the native activation gap", async ({ page }) => {
        await page.evaluate(() => {
            window.__metadata.nativeTab = "server-b";
            window.__noxa.openClientInfo({ client_id: "alice", nickname: "Alice" });
            window.__noxaMeta.openServerInfo();
            return window.__noxaSocial.refreshNews();
        });
        await expect(page.locator(".server-info-error")).toContainText("Server details unavailable");
        await expect(page.locator("#news-area")).toHaveText("server info unavailable");
        const calls = await page.evaluate(() => window.__metadata.calls.filter(c => ["GetClientInfo", "ServerInfo", "MOTD"].includes(c.name)));
        expect(calls.map(c => c.name).sort()).toEqual(["GetClientInfo", "GetClientInfo", "MOTD", "ServerInfo", "ServerInfo"]);
        expect(calls.every(c => c.tabID === "server-a")).toBe(true);
        expect(calls.filter(c => c.name === "GetClientInfo").map(c => c.args)).toEqual([["alice"], ["self"]]);
    });

    test("late news and channel icons cannot populate a replacement server", async ({ page }) => {
        await page.evaluate(() => {
            const f = window.__metadata;
            const pending = [];
            const app = window.go.main.App;
            window.go.main.App = new Proxy(app, { get(target, key) {
                if (!["ServerInfoForTab", "ChannelIconGetForTab"].includes(key)) return target[key];
                return async (...args) => {
                    const response = key === "ServerInfoForTab" ? { name: "Stale server", uptime_seconds: 60 } :
                        { data_base64: "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=", content_type: "image/png" };
                    if (args[0] !== "server-a") return {};
                    f.calls.push({ name: key, tabID: args[0], args: args.slice(1) });
                    await new Promise(resolve => pending.push(resolve));
                    return response;
                };
            } });
            f.release = () => pending.forEach(resolve => resolve());
            window.__noxa.state.channels = [{ ChannelID: 33, Name: "Old channel", ParentID: 0, HasIcon: true }];
            window.__noxa.renderTree();
            f.news = window.__noxaSocial.refreshNews();
        });
        await expect.poll(() => page.evaluate(() => window.__metadata.calls.some(c => c.name === "ChannelIconGetForTab" && c.args[0] === 33))).toBe(true);
        await page.evaluate(async () => {
            for (const callback of window.__events.tab_reset || []) callback("server-b");
            window.__noxa.state.channels = [{ ChannelID: 33, Name: "New channel", ParentID: 0, HasIcon: true }];
            window.__noxa.renderTree();
            document.getElementById("news-area").textContent = "New server news";
            window.__metadata.release();
            await window.__metadata.news;
        });
        await expect(page.locator("#news-area")).not.toContainText("Stale server");
        await expect(page.locator('.channel[data-chid="33"] .ch-icon img')).toHaveCount(0);
    });

    test("server information captures the opening client before queued queries", async ({ page }) => {
        await page.evaluate(() => {
            window.__noxaMeta.openServerInfo();
            window.__noxa.state.myClientID = "replacement";
        });
        await expect.poll(() => page.evaluate(() => window.__metadata.calls.filter(c => c.name === "GetClientInfo"))).toEqual([
            { name: "GetClientInfo", tabID: "server-a", args: ["self"] },
        ]);
    });

    test("recent channel navigation cannot join another native server", async ({ page }) => {
        await page.locator('.channel[data-chid="1"]').click({ button: "right" });
        await page.evaluate(() => { window.__metadata.nativeTab = "server-b"; });
        await page.locator('[data-act="recent-2"]').click();
        await expect.poll(() => page.evaluate(() => window.__metadata.calls.filter(c => c.name === "JoinChannel"))).toEqual([
            { name: "JoinChannel", tabID: "server-a", args: [2] },
        ]);
        expect(await page.evaluate(() => window.__metadata.effects)).toEqual([]);
    });

    test("quick switch and leave channel reject the native activation gap", async ({ page }) => {
        await page.keyboard.press("Control+k");
        await page.evaluate(() => { window.__metadata.nativeTab = "server-b"; });
        await page.locator(".qs-row").filter({ hasText: "# Other" }).click();
        await page.locator("#voice-leave-channel").click();
        await expect.poll(() => page.evaluate(() => window.__metadata.calls.filter(c => c.name === "JoinChannel"))).toEqual([
            { name: "JoinChannel", tabID: "server-a", args: [2] },
            { name: "JoinChannel", tabID: "server-a", args: [0] },
        ]);
        expect(await page.evaluate(() => window.__metadata.effects)).toEqual([]);
        await expect(page.locator("#voice-leave-channel")).toBeEnabled();
    });

    test("channel notifications retain their originating server across tab changes", async ({ page }) => {
        await page.evaluate(() => {
            window.__noxaPolish.recordNotification("mention", "Old channel mention", { channelID: 2 });
            for (const callback of window.__events.tab_reset || []) callback("server-b");
            window.__metadata.nativeTab = "server-b";
            window.__noxaPolish.openNotifCenter();
        });
        await page.locator(".nc-row").filter({ hasText: "Old channel mention" }).click();
        await expect.poll(() => page.evaluate(() => window.__metadata.calls.filter(c => c.name === "JoinChannel"))).toEqual([
            { name: "JoinChannel", tabID: "server-a", args: [2] },
        ]);
        expect(await page.evaluate(() => window.__metadata.effects)).toEqual([]);
    });

    for (const stage of ["DMHistoryContext", "Subscriptions", "MOTD"]) {
        test(`connection setup discards late ${stage} after reset`, async ({ page }) => {
            await page.evaluate(async gated => {
                const f = window.__metadata;
                f.gated = gated;
                f.gate = new Promise(resolve => { f.release = resolve; });
                const chat = await import("/src/chat-ui.js");
                f.connect = chat.onConnect();
            }, stage);
            await expect.poll(() => page.evaluate(name => window.__metadata.calls.some(c => c.name === name), stage)).toBe(true);
            await page.evaluate(async () => {
                window.__dmStorageIdentity = "new-identity";
                for (const callback of window.__events.tab_reset || []) callback("server-b");
                window.__noxa.state.myUniqueID = "new-identity";
                window.__metadata.release();
                await window.__metadata.connect;
            });
            expect(await page.evaluate(() => window.__noxa.state.myUniqueID)).toBe("new-identity");
            expect(await page.evaluate(() => window.__noxaChat.isSubscribed(2))).toBe(false);
            expect(await page.evaluate(() => window.__metadata.notices)).toEqual([]);
            if (stage !== "MOTD") expect(await page.evaluate(() => window.__metadata.calls.some(c => c.name === "MOTD" && c.tabID === "server-a"))).toBe(false);
        });
    }

    test("connection setup restores the current tab subscriptions and notice", async ({ page }) => {
        await page.evaluate(async () => { const chat = await import("/src/chat-ui.js"); await chat.onConnect(); });
        expect(await page.evaluate(() => window.__noxaChat.isSubscribed(2))).toBe(true);
        expect(await page.evaluate(() => window.__metadata.notices)).toEqual(["server notice — old notice"]);
        expect(await page.evaluate(() => window.__metadata.calls.filter(c => ["MOTD", "Subscriptions"].includes(c.name)).map(c => c.tabID))).toEqual(["server-a", "server-a"]);
    });

});

test.describe("tab-bound member actions and branding", () => {
    test.beforeEach(async ({ page }) => {
        await page.evaluate(() => {
            const v = window.__noxa;
            v.showWorkspace(false);
            v.state.activeTabID = "server-a";
            v.state.authorizationModel = "roles-v1";
            v.state.isAdmin = false;
            v.state.myPerms = new Map();
            v.state.myClientID = "self";
            v.state.clients = [
                { client_id: "alice", unique_id: "alice", nickname: "Alice", channel_id: 1 },
                { client_id: "bob", unique_id: "bob", nickname: "Bob", channel_id: 1 },
            ];
            v.state.channels = [{ ChannelID: 1, Name: "Lobby", ParentID: 0 }, { ChannelID: 2, Name: "Other", ParentID: 0 }];
            v.renderTree();
            const fixture = window.__actions = { nativeTab: "server-a", calls: [], effects: [], toasts: [] };
            v.toast = text => fixture.toasts.push(text);
            const app = window.go.main.App;
            const names = ["JoinChannel", "KickClient", "DisconnectMember", "MoveClient", "Poke", "SetAvatar", "ServerIconSet", "ServerBannerSet", "ServerIconGet", "ServerBannerGet"];
            window.go.main.App = new Proxy(app, { get(target, key) {
                if (names.includes(key)) return () => { throw new Error("unscoped native call"); };
                if (!names.some(name => key === name + "ForTab")) return target[key];
                return async (tabID, ...args) => {
                    const name = key.slice(0, -"ForTab".length);
                    fixture.calls.push({ name, tabID, args });
                    if (name.endsWith("Get")) {
                        if (tabID !== fixture.nativeTab) throw new Error("server changed");
                        return {};
                    }
                    if (tabID !== fixture.nativeTab) return "server changed";
                    if (name === "DisconnectMember" && args[1] !== v.state.clients.find(c => c.client_id === args[0])?.channel_id) return "target channel changed";
                    fixture.effects.push({ name, args });
                    if (fixture.gate) await fixture.gate;
                    return fixture.error || "";
                };
            } });
        });
    });

    test("join rejection preserves membership until the result arrives", async ({ page }) => {
        await page.evaluate(() => {
            const f = window.__actions;
            f.error = "channel access denied";
            f.gate = new Promise(resolve => { f.release = resolve; });
            window.__noxa.state.myChannelID = 1;
            window.__noxa.renderTree();
        });
        await page.locator('.channel[data-chid="2"]').click();
        await expect.poll(() => page.evaluate(() => window.__actions.calls.filter(c => c.name === "JoinChannel"))).toEqual([
            { name: "JoinChannel", tabID: "server-a", args: [2] },
        ]);
        expect(await page.evaluate(() => window.__noxa.state.myChannelID)).toBe(1);
        await expect(page.locator("#toasts .toast")).toHaveCount(0);
        await page.evaluate(() => window.__actions.release());
        await expect(page.locator("#toasts .toast")).toHaveText("join failed: channel access denied");
        expect(await page.evaluate(() => window.__noxa.state.myChannelID)).toBe(1);
    });

    test("leave waits for confirmation and restores controls on rejection", async ({ page }) => {
        await page.evaluate(() => {
            const f = window.__actions;
            f.error = "membership backend unavailable";
            f.gate = new Promise(resolve => { f.release = resolve; });
            window.__noxa.state.myChannelID = 1;
            window.__noxa.renderTree();
        });
        await page.locator("#voice-leave-channel").click();
        await expect.poll(() => page.evaluate(() => window.__actions.calls.filter(c => c.name === "JoinChannel"))).toEqual([
            { name: "JoinChannel", tabID: "server-a", args: [0] },
        ]);
        await expect(page.locator("#voice-leave-channel")).toBeDisabled();
        expect(await page.evaluate(() => window.__noxa.state.myChannelID)).toBe(1);
        await page.evaluate(() => window.__actions.release());
        await expect(page.locator("#voice-leave-channel")).toBeEnabled();
        await expect.poll(() => page.evaluate(() => window.__actions.toasts.join(" "))).toContain("membership backend unavailable");
        expect(await page.evaluate(() => window.__noxa.state.myChannelID)).toBe(1);
    });

    test("old leave result cannot change a replacement server's controls", async ({ page }) => {
        await page.evaluate(() => {
            const f = window.__actions;
            f.error = "old leave denied";
            f.gate = new Promise(resolve => { f.release = resolve; });
            window.__noxa.state.myChannelID = 1;
            window.__noxa.renderTree();
        });
        await page.locator("#voice-leave-channel").click();
        await expect.poll(() => page.evaluate(() => window.__actions.calls.some(c => c.name === "JoinChannel"))).toBe(true);
        await page.evaluate(() => {
            for (const callback of window.__events.tab_reset || []) callback("server-b");
            window.__noxa.state.myChannelID = 2;
            window.__noxa.renderTree();
            window.__actions.release();
        });
        await expect(page.locator("#voice-leave-channel")).toBeEnabled();
        expect(await page.evaluate(() => window.__actions.toasts)).toEqual([]);
        expect(await page.evaluate(() => window.__noxa.state.myChannelID)).toBe(2);
    });

    test("role moderators can request a kick without legacy powers", async ({ page }) => {
        await page.locator('.client[data-clid="alice"]').click({ button: "right" });
        await page.locator('[data-act="kick-ch"]').click();
        await page.getByRole("dialog").locator(".reason").fill("Please rejoin");
        await page.getByRole("button", { name: "Kick", exact: true }).click();
        await expect.poll(() => page.evaluate(() => window.__actions.effects)).toEqual([
            { name: "DisconnectMember", args: ["alice", 1, "Please rejoin"] },
        ]);
    });

    test("kick and poke prompts cannot act on another native tab", async ({ page }) => {
        await page.locator('.client[data-clid="alice"]').click({ button: "right" });
        await page.locator('[data-act="kick-srv"]').click();
        await page.evaluate(() => { window.__actions.nativeTab = "server-b"; });
        await page.getByRole("button", { name: "Kick", exact: true }).click();
        await expect.poll(() => page.evaluate(() => window.__actions.calls.length)).toBe(1);
        await page.evaluate(() => window.__noxaSocial.openPoke(window.__noxa.state.clients[0]));
        await page.getByRole("button", { name: "Poke", exact: true }).click();
        await expect.poll(() => page.evaluate(() => window.__actions.calls.length)).toBe(2);
        expect(await page.evaluate(() => window.__actions.effects)).toEqual([]);
        expect(await page.evaluate(() => window.__actions.calls.every(c => c.tabID === "server-a"))).toBe(true);
    });

    test("channel disconnect retains the channel captured when the menu opened", async ({ page }) => {
        await page.locator('.client[data-clid="alice"]').click({ button: "right" });
        await page.evaluate(() => { window.__noxa.state.clients[0].channel_id = 2; });
        await page.locator('[data-act="kick-ch"]').click();
        await page.getByRole("button", { name: "Kick", exact: true }).click();
        await expect.poll(() => page.evaluate(() => window.__actions.calls)).toEqual([
            { name: "DisconnectMember", tabID: "server-a", args: ["alice", 1, ""] },
        ]);
        expect(await page.evaluate(() => window.__actions.effects)).toEqual([]);
        await expect.poll(() => page.evaluate(() => window.__actions.toasts)).toEqual(["target channel changed"]);
    });

    test("batch channel disconnects retain every member's opening channel", async ({ page }) => {
        await page.evaluate(() => {
            window.__noxa.state.multiSelect = new Set(["alice", "bob"]);
            const f = window.__actions;
            f.gate = new Promise(resolve => { f.release = resolve; });
        });
        await page.locator('.client[data-clid="alice"]').click({ button: "right" });
        await page.locator('[data-act="kick"]').click();
        await expect.poll(() => page.evaluate(() => window.__actions.effects.length)).toBe(1);
        await page.evaluate(() => { window.__noxa.state.clients[1].channel_id = 2; window.__actions.release(); });
        await expect.poll(() => page.evaluate(() => window.__actions.calls)).toEqual([
            { name: "DisconnectMember", tabID: "server-a", args: ["alice", 1, ""] },
            { name: "DisconnectMember", tabID: "server-a", args: ["bob", 1, ""] },
        ]);
        expect(await page.evaluate(() => window.__actions.effects.length)).toBe(1);
        await expect.poll(() => page.evaluate(() => window.__actions.toasts)).toEqual(["target channel changed"]);
    });

    test("a member who left before tree redraw does not offer a channel disconnect", async ({ page }) => {
        await page.evaluate(() => { window.__noxa.state.clients[0].channel_id = 0; });
        await page.locator('.client[data-clid="alice"]').click({ button: "right" });
        await expect(page.locator('[data-act="kick-ch"]')).toHaveCount(0);
        await expect(page.locator('[data-act="kick-srv"]')).toBeVisible();
    });

    test("batch kicks stop when native activation changes between requests", async ({ page }) => {
        await page.evaluate(() => {
            window.__noxa.state.multiSelect = new Set(["alice", "bob"]);
            const f = window.__actions;
            f.gate = new Promise(resolve => { f.release = resolve; });
        });
        await page.locator('.client[data-clid="alice"]').click({ button: "right" });
        await page.locator('[data-act="kick"]').click();
        await expect.poll(() => page.evaluate(() => window.__actions.effects.length)).toBe(1);
        await page.evaluate(() => { window.__actions.nativeTab = "server-b"; window.__actions.release(); });
        await expect.poll(() => page.evaluate(() => window.__actions.calls.length)).toBe(2);
        expect(await page.evaluate(() => window.__actions.effects.length)).toBe(1);
        expect(await page.evaluate(() => window.__actions.toasts.some(s => s.includes("kicked 2")))).toBe(false);
    });

    for (const switched of [false, true]) {
        test(`poke rejection ${switched ? "stays out of a replacement session" : "is reported after native acceptance fails"}`, async ({ page }) => {
            await page.evaluate(() => {
                const f = window.__actions;
                f.error = "poke permission denied";
                f.gate = new Promise(resolve => { f.release = resolve; });
                window.__noxaSocial.openPoke(window.__noxa.state.clients[0]);
            });
            await page.getByRole("button", { name: "Poke", exact: true }).click();
            await expect.poll(() => page.evaluate(() => window.__actions.effects.length)).toBe(1);
            expect(await page.evaluate(() => window.__actions.toasts)).toEqual([]);
            await page.evaluate(async changed => {
                if (changed) for (const callback of window.__events.tab_reset || []) callback("server-b");
                window.__actions.release();
                await window.__actions.gate;
            }, switched);
            if (switched) expect(await page.evaluate(() => window.__actions.toasts)).toEqual([]);
            else await expect.poll(() => page.evaluate(() => window.__actions.toasts)).toEqual(["poke failed: poke permission denied"]);
        });
    }

    test("batch channel disconnects wait for each result and stop on denial", async ({ page }) => {
        await page.evaluate(() => {
            window.__noxa.state.multiSelect = new Set(["alice", "bob"]);
            const f = window.__actions;
            f.error = "disconnect denied";
            f.gate = new Promise(resolve => { f.release = resolve; });
        });
        await page.locator('.client[data-clid="alice"]').click({ button: "right" });
        await page.locator('[data-act="kick"]').click();
        await expect.poll(() => page.evaluate(() => window.__actions.effects.length)).toBe(1);
        expect(await page.evaluate(() => window.__actions.toasts)).toEqual([]);
        await page.evaluate(() => window.__actions.release());
        await expect.poll(() => page.evaluate(() => window.__actions.toasts)).toEqual(["disconnect denied"]);
        expect(await page.evaluate(() => window.__actions.effects.length)).toBe(1);
    });

    for (const outcome of ["ban saved and sessions revoked; resource cleanup is pending", "sessions revoked; ban persistence is unconfirmed; refresh the ban list before retrying"]) {
        test(`ban reports ${outcome}`, async ({ page }) => {
            await page.locator('.client[data-clid="alice"]').click({ button: "right" });
            await page.locator('[data-act="ban"]').click();
            await page.evaluate(result => {
                const f = window.__actions;
                f.error = result;
                f.gate = new Promise(resolve => { f.release = resolve; });
            }, outcome);
            await page.getByRole("button", { name: "Ban", exact: true }).click();
            await expect.poll(() => page.evaluate(() => window.__actions.effects.length)).toBe(1);
            expect(await page.evaluate(() => window.__actions.toasts)).toEqual([]);
            await page.evaluate(() => window.__actions.release());
            await expect.poll(() => page.evaluate(() => window.__actions.toasts)).toEqual([outcome]);
            expect(await page.evaluate(() => window.__actions.effects.length)).toBe(1);
        });
    }

    test("late ban errors do not toast into a replacement session", async ({ page }) => {
        await page.locator('.client[data-clid="alice"]').click({ button: "right" });
        await page.locator('[data-act="ban"]').click();
        await page.evaluate(() => {
            const f = window.__actions;
            f.error = "old rejection";
            f.gate = new Promise(resolve => { f.release = resolve; });
        });
        await page.getByRole("button", { name: "Ban", exact: true }).click();
        await expect.poll(() => page.evaluate(() => window.__actions.effects.length)).toBe(1);
        await page.evaluate(() => {
            for (const callback of window.__events.tab_reset || []) callback("new-session");
            window.__actions.release();
        });
        expect(await page.evaluate(() => window.__actions.toasts)).toEqual([]);
    });

    test("member drag uses the opening native tab and rejects a switched server", async ({ page }) => {
        const member = page.locator('.client[data-clid="alice"]');
        const channel = page.locator('.channel[data-chid="2"]');
        await member.dragTo(channel);
        await expect.poll(() => page.evaluate(() => window.__actions.effects)).toEqual([
            { name: "MoveClient", args: ["alice", 2] },
        ]);
        await page.evaluate(() => { window.__actions.nativeTab = "server-b"; });
        await member.dragTo(channel);
        await expect.poll(() => page.evaluate(() => window.__actions.calls.filter(c => c.name === "MoveClient").length)).toBe(2);
        expect(await page.evaluate(() => window.__actions.effects.length)).toBe(1);
        expect(await page.evaluate(() => window.__actions.calls.every(c => c.tabID === "server-a"))).toBe(true);
    });

    test("self drag is rejected during native activation before frontend reset", async ({ page }) => {
        await page.evaluate(() => { window.__noxa.state.myClientID = "alice"; window.__actions.nativeTab = "server-b"; });
        await page.locator('.client[data-clid="alice"]').dragTo(page.locator('.channel[data-chid="2"]'));
        await expect.poll(() => page.evaluate(() => window.__actions.calls.filter(c => c.name === "JoinChannel"))).toEqual([
            { name: "JoinChannel", tabID: "server-a", args: [2] },
        ]);
        expect(await page.evaluate(() => window.__actions.effects)).toEqual([]);
    });

    test("drag targets the exact session when two sessions share an identity", async ({ page }) => {
        await page.evaluate(() => {
            window.__noxa.state.clients[1].unique_id = "alice";
            window.__noxa.renderTree();
        });
        await page.locator('.client[data-clid="bob"]').dragTo(page.locator('.channel[data-chid="2"]'));
        await expect.poll(() => page.evaluate(() => window.__actions.effects)).toEqual([
            { name: "MoveClient", args: ["bob", 2] },
        ]);
    });

    test("returning to the same tab does not revive an old member drag", async ({ page }) => {
        await page.evaluate(() => {
            const transfer = new DataTransfer();
            document.querySelector('.client[data-clid="alice"]').dispatchEvent(new DragEvent("dragstart", { dataTransfer: transfer, bubbles: true }));
            for (const id of ["server-b", "server-a"]) {
                for (const callback of window.__events.tab_reset || []) callback(id);
            }
            const v = window.__noxa;
            v.state.clients = [{ client_id: "alice", unique_id: "alice", nickname: "Alice", channel_id: 1 }];
            v.state.channels = [{ ChannelID: 1, Name: "Lobby", ParentID: 0 }, { ChannelID: 2, Name: "Other", ParentID: 0 }];
            v.renderTree();
            document.querySelector('.channel[data-chid="2"]').dispatchEvent(new DragEvent("drop", { dataTransfer: transfer, bubbles: true }));
        });
        expect(await page.evaluate(() => window.__actions.effects)).toEqual([]);
    });

    test("a drag started on another tab cannot target a matching user after reset", async ({ page }) => {
        await page.evaluate(() => {
            const transfer = new DataTransfer();
            document.querySelector('.client[data-clid="alice"]').dispatchEvent(new DragEvent("dragstart", { dataTransfer: transfer, bubbles: true }));
            window.__oldMemberDrag = transfer;
            for (const callback of window.__events.tab_reset || []) callback("server-b");
            const v = window.__noxa;
            v.state.clients = [{ client_id: "new-alice", unique_id: "alice", nickname: "Alice", channel_id: 1 }];
            v.state.channels = [{ ChannelID: 1, Name: "Lobby", ParentID: 0 }, { ChannelID: 2, Name: "Other", ParentID: 0 }];
            v.renderTree();
            document.querySelector('.channel[data-chid="2"]').dispatchEvent(new DragEvent("drop", { dataTransfer: transfer, bubbles: true }));
        });
        expect(await page.evaluate(() => window.__actions.effects)).toEqual([]);
        expect(await page.evaluate(() => window.__actions.calls.filter(c => c.name === "MoveClient"))).toEqual([]);
    });

    for (const kind of ["icon", "banner"]) {
        for (const rejected of [false, true]) {
            test(`${kind} waits for storage before ${rejected ? "rejection" : "success"} feedback`, async ({ page }) => {
                await page.evaluate(rejected => {
                    const f = window.__actions;
                    f.error = rejected ? "asset storage failed" : "";
                    f.gate = new Promise(resolve => { f.release = resolve; });
                }, rejected);
                const chooserPromise = page.waitForEvent("filechooser");
                if (kind === "icon") {
                    await page.locator("#menubar > .menu-item > span").filter({ hasText: /^Self$/ }).click();
                    await page.getByRole("menuitem", { name: /^Set server icon/ }).click();
                } else {
                    await page.locator("#tab-files").click();
                    await page.locator(".fb-banner").click();
                }
                const chooser = await chooserPromise;
                await chooser.setFiles({ name: "pixel.png", mimeType: "image/png",
                    buffer: Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=", "base64") });
                await expect.poll(() => page.evaluate(() => window.__actions.effects.length)).toBe(1);
                expect(await page.evaluate(() => window.__actions.toasts)).toEqual([]);
                const reads = await page.evaluate(() => window.__actions.calls.filter(c => c.name.endsWith("Get")).length);
                await page.evaluate(() => window.__actions.release());
                await expect.poll(() => page.evaluate(() => window.__actions.toasts.length)).toBe(1);
                if (rejected) {
                    expect(await page.evaluate(() => window.__actions.toasts[0])).toContain("asset storage failed");
                    expect(await page.evaluate(() => window.__actions.calls.filter(c => c.name.endsWith("Get")).length)).toBe(reads);
                } else {
                    expect(await page.evaluate(() => window.__actions.toasts[0])).toMatch(/updated/i);
                    // Icon refresh also loads the sidebar banner.
                    await expect.poll(() => page.evaluate(() => window.__actions.calls.filter(c => c.name.endsWith("Get")).length)).toBe(reads + (kind === "icon" ? 2 : 1));
                }
            });
        }

        test(`${kind} picker stays on its original native tab`, async ({ page }) => {
            const chooserPromise = page.waitForEvent("filechooser");
            if (kind === "icon") {
                await page.locator("#menubar > .menu-item > span").filter({ hasText: /^Self$/ }).click();
                await page.getByRole("menuitem", { name: /^Set server icon/ }).click();
            } else {
                await page.locator("#tab-files").click();
                await page.locator(".fb-banner").click();
            }
            const chooser = await chooserPromise;
            await page.evaluate(() => { window.__actions.nativeTab = "server-b"; });
            await chooser.setFiles({ name: "pixel.png", mimeType: "image/png",
                buffer: Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=", "base64") });
            await expect.poll(() => page.evaluate(() => window.__actions.calls.filter(c => c.name.endsWith("Set")).length)).toBe(1);
            expect(await page.evaluate(() => window.__actions.effects)).toEqual([]);
            expect(await page.evaluate(() => window.__actions.calls.filter(c => c.name.endsWith("Set"))[0].tabID)).toBe("server-a");
        });
    }
});

test.describe("tab-bound shared management", () => {
    test.beforeEach(async ({ page }) => {
        await page.evaluate(() => {
            window.__noxa.showWorkspace(false);
            window.__noxa.state.isAdmin = false;
            window.__noxa.state.activeTabID = "server-a";
            window.__noxa.state.myPerms = new Map();
            const fixture = window.__management = { nativeTab: "server-a", calls: [], effects: [], toasts: [],
                filters: { word_filter: "old", link_blacklist: "bad.test", link_whitelist: "", from_config: false },
                bans: [{ id: 17, value: "member", reason: "spam", banned_by: "owner", expires_at: 0 }],
                complaints: [{ target_unique_id: "member", from_unique_id: "reporter", reason: "spam", created_at: 1 }],
            };
            window.__noxa.toast = (text) => fixture.toasts.push(text);
            const app = window.go.main.App;
            const names = ["AuditLog", "ChatFilterGet", "ChatFilterSet", "BanList", "BanRemove", "ComplaintList", "ComplaintClear"];
            window.go.main.App = new Proxy(app, { get(target, key) {
                if (names.includes(key)) return () => { throw new Error("unscoped call"); };
                if (!names.some((name) => key === name + "ForTab")) return target[key];
                return async (tabID, ...args) => {
                    const name = key.slice(0, -"ForTab".length);
                    fixture.calls.push({ name, tabID, args });
                    if (tabID !== fixture.nativeTab || fixture.denied) {
                        if (name === "BanRemove") return "permission denied or server changed";
                        throw new Error("permission denied or server changed");
                    }
                    if (name === "ChatFilterGet") {
                        if (fixture.loadGate) await fixture.loadGate;
                        return structuredClone(fixture.filters);
                    }
                    if (name === "ChatFilterSet") {
                        fixture.effects.push({ name, args });
                        if (fixture.saveGate) await fixture.saveGate;
                        if (fixture.saveFailed) throw new Error("save outcome unknown");
                        fixture.filters = { word_filter: args[0], link_blacklist: args[1], link_whitelist: args[2], from_config: false };
                        return structuredClone(fixture.filters);
                    }
                    if (name === "BanList") return { bans: structuredClone(fixture.bans) };
                    if (name === "BanRemove") { fixture.effects.push({ name, args }); fixture.bans = []; return ""; }
                    if (name === "ComplaintList") return { entries: structuredClone(fixture.complaints) };
                    if (name === "ComplaintClear") { fixture.effects.push({ name, args }); fixture.complaints = []; return { entries: [] }; }
                    return { entries: args[0] ? [] : [{ id: 7, action: "ban_remove", actor: "owner", target: "17", detail: "", created_at: 1 }] };
                };
            } });
        });
    });

    test("delegated filter manager loads and saves without legacy flags", async ({ page }) => {
        await page.evaluate(() => window.__noxaPerms.openChatFilters());
        await expect(page.locator(".cf-words")).toHaveValue("old");
        await page.locator(".cf-words").fill("");
        await page.locator(".cf-save").click();
        await expect.poll(() => page.evaluate(() => window.__management.effects)).toEqual([
            { name: "ChatFilterSet", args: ["", "bad.test", ""] },
        ]);
        await expect(page.locator(".cf-save")).toBeEnabled();
    });

    test("filter save requires a loaded form and freezes edits until acknowledgement", async ({ page }) => {
        await page.evaluate(() => {
            const f = window.__management;
            f.loadGate = new Promise(resolve => { f.releaseLoad = resolve; });
            window.__noxaPerms.openChatFilters();
        });
        await expect(page.locator(".cf-save")).toBeDisabled();
        await expect(page.locator(".cf-words")).toBeDisabled();
        await page.evaluate(() => window.__management.releaseLoad());
        await expect(page.locator(".cf-words")).toHaveValue("old");
        await page.locator(".cf-words").fill("draft");
        await page.evaluate(() => {
            const f = window.__management;
            f.saveGate = new Promise(resolve => { f.releaseSave = resolve; });
        });
        await page.locator(".cf-save").click();
        await expect(page.locator(".cf-reload")).toBeDisabled();
        await expect(page.locator(".cf-words")).toBeDisabled();
        await page.evaluate(() => window.__management.releaseSave());
        await expect(page.locator(".cf-save")).toBeEnabled();
        await expect(page.locator(".cf-words")).toHaveValue("draft");
    });

    test("uncertain filter save keeps draft and requires reload", async ({ page }) => {
        await page.evaluate(() => window.__noxaPerms.openChatFilters());
        await expect(page.locator(".cf-words")).toHaveValue("old");
        await page.locator(".cf-words").fill("draft");
        await page.evaluate(() => { window.__management.saveFailed = true; });
        await page.locator(".cf-save").click();
        await expect.poll(() => page.evaluate(() => window.__management.toasts.length)).toBe(1);
        await expect(page.locator(".cf-save")).toBeDisabled();
        await expect(page.locator(".cf-words")).toHaveValue("draft");
        await page.locator(".cf-reload").click();
        await expect(page.locator(".cf-words")).toHaveValue("old");
        await expect(page.locator(".cf-save")).toBeEnabled();
    });

    test("old filter save is rejected before frontend reset", async ({ page }) => {
        await page.evaluate(() => window.__noxaPerms.openChatFilters());
        await expect(page.locator(".cf-words")).toHaveValue("old");
        await page.locator(".cf-words").fill("draft");
        await page.evaluate(() => { window.__management.nativeTab = "server-b"; });
        await page.locator(".cf-save").click();
        await expect.poll(() => page.evaluate(() => window.__management.toasts.length)).toBe(1);
        expect(await page.evaluate(() => window.__management.effects)).toEqual([]);
        expect(await page.evaluate(() => window.__management.calls.at(-1).tabID)).toBe("server-a");
    });

    test("late filter acknowledgement cannot restore a closed server dialog", async ({ page }) => {
        await page.evaluate(() => window.__noxaPerms.openChatFilters());
        await expect(page.locator(".cf-words")).toHaveValue("old");
        await page.locator(".cf-words").fill("draft");
        await page.evaluate(() => {
            const f = window.__management;
            f.saveGate = new Promise(resolve => { f.releaseSave = resolve; });
        });
        await page.locator(".cf-save").click();
        await expect.poll(() => page.evaluate(() => window.__management.effects.length)).toBe(1);
        await page.evaluate(() => {
            for (const callback of window.__events.tab_reset || []) callback("replacement-session");
            window.__management.releaseSave();
        });
        await expect(page.getByRole("dialog")).toHaveCount(0);
        expect(await page.evaluate(() => window.__management.toasts)).toEqual([]);
        expect(await page.evaluate(() => window.__management.calls.map(c => c.name))).toEqual(["ChatFilterGet", "ChatFilterSet"]);
    });

    for (const [open, control, effect] of [
        ["openComplaints", ".cp-one", "ComplaintClear"],
    ]) {
        test(`${effect} uses current server grants without legacy flags`, async ({ page }) => {
            await page.evaluate(name => window.__noxaPerms[name](), open);
            await page.locator(control).click();
            if (effect === "BanRemove") await page.getByRole("button", { name: "Lift", exact: true }).click();
            await expect.poll(() => page.evaluate(() => window.__management.effects.length)).toBe(1);
            expect(await page.evaluate(() => window.__management.effects[0].name)).toBe(effect);
        });
        test(`${effect} cannot reach a different native tab`, async ({ page }) => {
            await page.evaluate(name => window.__noxaPerms[name](), open);
            await expect(page.locator(control)).toBeVisible();
            await page.evaluate(() => { window.__management.nativeTab = "server-b"; });
            await page.locator(control).click();
            if (effect === "BanRemove") {
                await page.getByRole("button", { name: "Lift", exact: true }).click();
                await expect(page.getByRole("dialog").getByRole("status")).toHaveText("Removal could not be confirmed. Reload bans before trying again.");
                await expect(page.locator(control)).toBeDisabled();
                await expect(page.getByRole("button", { name: "Reload bans", exact: true })).toBeEnabled();
            } else await expect.poll(() => page.evaluate(() => window.__management.toasts.length)).toBe(1);
            expect(await page.evaluate(() => window.__management.effects)).toEqual([]);
            expect(await page.evaluate(() => window.__management.calls.every(c => c.tabID === "server-a"))).toBe(true);
        });
    }

    for (const [open, error] of [["openBanList", "ban list failed"], ["openComplaints", "complaint list failed"], ["openChatFilters", "loading filters failed"]]) {
        test(`${open} honors server denial despite stale administrator flag`, async ({ page }) => {
            await page.evaluate(name => {
                window.__noxa.state.isAdmin = true;
                window.__management.denied = true;
                window.__noxaPerms[name]();
            }, open);
            await expect(page.getByRole("dialog")).toContainText(error);
            expect(await page.evaluate(() => window.__management.effects)).toEqual([]);
            if (open === "openChatFilters") await expect(page.locator(".cf-save")).toBeDisabled();
        });
    }
});

test.describe("role-aware server configuration", () => {
    test.beforeEach(async ({ page }) => {
        await page.evaluate(() => {
            const v = window.__noxa;
            v.state.authorizationModel = "roles-v1";
            v.state.isAdmin = false;
            v.state.activeTabID = "server-a";
            window.__serverSettings = { loads: 0, saves: [], mediaLoads: 0, mediaSaves: [], toasts: [], config: {
                max_clients: 100, client_timeout_seconds: 90, opus_bitrate: 64000,
                opus_fec: true, opus_dtx: false, opus_stereo: true, media_limits_management: true,
            }, media: { video_max_bitrate: 800000, video_max_width: 1280, video_max_height: 720 } };
            v.toast = message => window.__serverSettings.toasts.push(message);
            const app = window.go.main.App;
            window.go.main.App = new Proxy(app, { get(target, key) {
                if (key === "GetServerConfigForTab") return async tabID => {
                    if (tabID !== "server-a") throw new Error("wrong server tab");
                    const fixture = window.__serverSettings;
                    fixture.loads++;
                    if (fixture.delayLoad) await new Promise(resolve => { fixture.finishLoad = resolve; });
                    if (fixture.denied) throw new Error("permission denied");
                    return structuredClone(fixture.config);
                };
                if (key === "SetServerConfigForTab") return async (tabID, config) => {
                    if (tabID !== "server-a") throw new Error("wrong server tab");
                    const fixture = window.__serverSettings;
                    fixture.saves.push(structuredClone(config));
                    if (fixture.delaySave) await new Promise(resolve => { fixture.finishSave = resolve; });
                    if (fixture.denied) throw new Error("permission denied");
                    fixture.config = structuredClone(config);
                    return structuredClone(config);
                };
                if (key === "GetMediaLimitsForTab") return async tabID => {
                    if (tabID !== "server-a") throw new Error("wrong server tab");
                    const fixture = window.__serverSettings;
                    fixture.mediaLoads++;
                    if (fixture.denied) throw new Error("permission denied");
                    return structuredClone(fixture.media);
                };
                if (key === "SetMediaLimitsForTab") return async (tabID, limits) => {
                    if (tabID !== "server-a") throw new Error("wrong server tab");
                    const fixture = window.__serverSettings;
                    fixture.mediaSaves.push(structuredClone(limits));
                    if (fixture.delaySave) await new Promise(resolve => { fixture.finishSave = resolve; });
                    if (fixture.denied) throw new Error("permission denied");
                    fixture.media = structuredClone(limits);
                    return { revision: "2", ...structuredClone(limits) };
                };
                return target[key];
            } });
        });
    });

    test("delegated managers can save without the legacy administrator flag", async ({ page }) => {
        await page.evaluate(() => window.__noxa.openSettings("server"));
        await expect(page.getByLabel("Maximum clients (0 = unlimited)")).toHaveValue("100");
        await page.getByLabel("Maximum clients (0 = unlimited)").fill("75");
        await page.getByRole("button", { name: "Apply server configuration", exact: true }).click();
        await expect.poll(() => page.evaluate(() => window.__serverSettings.saves)).toEqual([{
            max_clients: 75, client_timeout_seconds: 90, opus_bitrate: 64000,
            opus_fec: true, opus_dtx: false, opus_stereo: true,
        }]);
    });

    test("delegated managers can edit live video publishing limits", async ({ page }) => {
        await page.evaluate(() => window.__noxa.openSettings("server"));
        const rate = page.getByLabel("Video bitrate ceiling (bit/s, 0 = unlimited)");
        const width = page.getByLabel("Maximum encoded video width (0 = unlimited)");
        const height = page.getByLabel("Maximum encoded video height (0 = unlimited)");
        await expect(rate).toHaveValue("800000");
        await expect(width).toHaveValue("1280");
        await expect(height).toHaveValue("720");
        await rate.fill("600000");
        await width.fill("640");
        await height.fill("360");
        await page.getByRole("button", { name: "Apply video publishing limits", exact: true }).click();
        await expect.poll(() => page.evaluate(() => window.__serverSettings.mediaSaves)).toEqual([{
            video_max_bitrate: 600000, video_max_width: 640, video_max_height: 360,
        }]);
        await width.fill("640");
        await height.fill("0");
        await page.getByRole("button", { name: "Apply video publishing limits", exact: true }).click();
        expect(await page.evaluate(() => window.__serverSettings.mediaSaves.length)).toBe(1);
        await expect(height).toHaveJSProperty("validationMessage", "Set both dimensions to 0, or set both between 1 and 16383.");
    });

    test("older servers omit the unsupported media editor", async ({ page }) => {
        await page.evaluate(() => {
            delete window.__serverSettings.config.media_limits_management;
            window.__noxa.openSettings("server");
        });
        await expect(page.getByLabel("Maximum clients (0 = unlimited)")).toHaveValue("100");
        await expect(page.getByRole("button", { name: "Apply video publishing limits", exact: true })).toHaveCount(0);
        expect(await page.evaluate(() => window.__serverSettings.mediaLoads)).toBe(0);
    });

    test("server authorization works before model discovery and denies legacy non-admins", async ({ page }) => {
        await page.evaluate(() => { delete window.__noxa.state.authorizationModel; window.__noxa.openSettings("server"); });
        await expect(page.getByLabel("Maximum clients (0 = unlimited)")).toHaveValue("100");
        await page.evaluate(() => { window.__serverSettings.denied = true; window.__noxa.openSettings("server"); });
        await expect(page.locator("#settings-content")).toContainText("permission denied");
        await expect(page.getByRole("button", { name: "Apply server configuration", exact: true })).toBeHidden();
    });

    test("role-mode denials override a stale legacy administrator flag", async ({ page }) => {
        await page.evaluate(() => {
            window.__noxa.state.isAdmin = true;
            window.__serverSettings.denied = true;
            window.__noxa.openSettings("server");
        });
        await expect(page.locator("#settings-content")).toContainText("permission denied");
        await expect(page.getByRole("button", { name: "Apply server configuration", exact: true })).toBeHidden();
        expect(await page.evaluate(() => window.__serverSettings.loads)).toBe(1);
    });

    test("invalid numbers and duplicate submissions never reach the backend", async ({ page }) => {
        await page.evaluate(() => { window.__serverSettings.delaySave = true; window.__noxa.openSettings("server"); });
        const maximum = page.getByLabel("Maximum clients (0 = unlimited)");
        await maximum.fill("-1");
        const apply = page.getByRole("button", { name: "Apply server configuration", exact: true });
        await apply.click();
        expect(await page.evaluate(() => window.__serverSettings.saves)).toEqual([]);
        await maximum.fill("50");
        await apply.click();
        await expect(apply).toBeDisabled();
        await expect(maximum).toBeDisabled();
        await page.evaluate(() => window.__serverSettings.finishSave());
        await expect(apply).toBeEnabled();
        expect(await page.evaluate(() => window.__serverSettings.saves.length)).toBe(1);
    });

    test("failed saves require an explicit reload before retry", async ({ page }) => {
        await page.evaluate(() => window.__noxa.openSettings("server"));
        const apply = page.getByRole("button", { name: "Apply server configuration", exact: true });
        await expect(apply).toBeVisible();
        await page.evaluate(() => { window.__serverSettings.denied = true; });
        await apply.click();
        await expect(apply).toBeDisabled();
        await expect(page.locator("#settings-content")).toContainText("permission denied");
        await page.evaluate(() => { window.__serverSettings.denied = false; window.__serverSettings.config.max_clients = 12; });
        await page.getByRole("button", { name: "Reload server configuration", exact: true }).click();
        await expect(page.getByLabel("Maximum clients (0 = unlimited)")).toHaveValue("12");
        await expect(apply).toBeEnabled();
        expect(await page.evaluate(() => window.__serverSettings.saves.length)).toBe(1);
    });

    test("a server switch prevents stale saves and ignores late replies", async ({ page }) => {
        await page.evaluate(() => { window.__serverSettings.delayLoad = true; window.__noxa.openSettings("server"); });
        await expect.poll(() => page.evaluate(() => typeof window.__serverSettings.finishLoad)).toBe("function");
        await page.evaluate(() => { window.__noxa.state.serverGeneration++; window.__serverSettings.finishLoad(); });
        await expect(page.locator("#settings-content")).toContainText("The server changed");
        await expect(page.getByRole("button", { name: "Apply server configuration", exact: true })).toBeHidden();
        await page.evaluate(() => { window.__serverSettings.delayLoad = false; });
        await page.getByRole("button", { name: "Reload server configuration", exact: true }).click();
        await expect(page.getByLabel("Maximum clients (0 = unlimited)")).toHaveValue("100");
        await page.evaluate(() => { window.__noxa.state.serverGeneration++; });
        await page.getByRole("button", { name: "Apply server configuration", exact: true }).click();
        expect(await page.evaluate(() => window.__serverSettings.saves)).toEqual([]);
    });

    test("a save reply from the previous server cannot display success on the next", async ({ page }) => {
        await page.evaluate(() => { window.__serverSettings.delaySave = true; window.__noxa.openSettings("server"); });
        await page.getByRole("button", { name: "Apply server configuration", exact: true }).click();
        await expect.poll(() => page.evaluate(() => typeof window.__serverSettings.finishSave)).toBe("function");
        await page.evaluate(() => { window.__noxa.state.serverGeneration++; window.__serverSettings.finishSave(); });
        await expect(page.locator("#settings-content")).toContainText("The server changed");
        expect(await page.evaluate(() => window.__serverSettings.toasts)).toEqual([]);
    });

    test("settings search indexes server labels without requesting protected data", async ({ page }) => {
        await page.evaluate(() => window.__noxa.openSettings("application"));
        await page.locator("#settings-search").fill("Maximum clients");
        await expect(page.locator(".set-search-hit")).toHaveCount(1);
        expect(await page.evaluate(() => window.__serverSettings.loads)).toBe(0);
        await page.locator(".set-search-hit").click();
        await expect(page.getByLabel("Maximum clients (0 = unlimited)")).toHaveValue("100");
    });
});

test("role cosmetics use visible snapshot members, highest hoist and safe inline icons", async ({ page }) => {
    await page.evaluate(async () => {
        const v = window.__noxa;
        v.state.authorizationModel = "roles-v1";
        v.state.channels = [{ ChannelID: 42, Name: "Lounge" }];
        const low = { id: 20, name: "Member", position: 1, color: "#abcdef", hoist: true };
        const high = { id: 30, name: "Helper <img src=x>", position: 2, icon: "★", hoist: true };
        v.state.clients = [
            { client_id: "alice", unique_id: "alice", nickname: "Alice", channel_id: 42, roles: [low, high] },
            { client_id: "bob", unique_id: "bob", nickname: "Bob", channel_id: 42, roles: [low] },
        ];
        v.showWorkspace(false);
        v.renderTree();
    });
    await expect(page.locator(".hoist-section")).toHaveCount(2);
    await expect(page.locator(".hoist-section").first().locator(".client-name")).toHaveText("Alice");
    await expect(page.locator(".hoist-section").last().locator(".client-name")).toHaveText("Bob");
    await expect(page.locator('.client[data-clid="alice"]').last().locator(".client-name")).toHaveCSS("color", "rgb(171, 205, 239)");
    await expect(page.locator('.client[data-clid="alice"]').last().locator(".group-badge")).toHaveText("★ Helper <img src=x>");
    await expect(page.locator(".hoist-icon img, .group-badge img")).toHaveCount(0);
    await page.setViewportSize({ width: 720, height: 800 });
    await page.locator("#workspace-sidebar-toggle").click();
    await expect(page.locator(".hoist-section").first()).toBeVisible();
    await page.screenshot({ path: "../../.cache/role-cosmetics.png" });
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.evaluate(() => window.__noxa.openClientInfo(window.__noxa.state.clients[0]));
    await expect(page.locator(".ci-member-roles .group-badge")).toHaveCount(2);
    await page.evaluate(() => {
        for (const callback of window.__events.snapshot || []) callback(JSON.stringify({ root_channels: [{ ChannelID: 42, Name: "Lounge", clients: [
            { client_id: "alice", unique_id: "alice", nickname: "Alice", channel_id: 42 },
        ] }] }));
    });
    await expect(page.locator(".hoist-section, .client .group-badge")).toHaveCount(0);
    await expect(page.locator(".ci-member-roles")).toBeHidden();
    await expect(page.getByText("Bob", { exact: true })).toHaveCount(0);
});

test("@quickwins workspace labels, devices and language switch", async ({ page }) => {
    await page.evaluate(() => {
        const v = window.__noxa;
        v.state.settings.language = "en";
        v.state.settings.capture_device_id = "mic-usb";
        v.state.settings.playback_device_id = "speaker-usb";
        v.state.channels = [{ ChannelID: 42, Name: "Lounge" }];
        v.state.myChannelID = 42;
        v.state.myClientID = "me";
        v.state.clients = [{ client_id: "me", unique_id: "me", nickname: "Alice", channel_id: 42 }];
        v.showWorkspace(false);
        v.applyAppearance();
        v.renderTree();
    });
    await expect(page.locator("#channel-member-count")).toHaveText("1 in voice");
    await expect(page.locator('#chat-scope option[value="global"]')).toHaveText("Entire server");
    await page.locator(".channel-actions > summary").click();
    await expect(page.locator("#chat-pins-btn")).toHaveText("Pinned messages");
    await expect(page.locator("#chat-pins-btn svg")).toHaveCount(1);
    await page.locator("#voice-options > summary").click();
    await expect(page.locator("#voice-input-device")).toContainText("USB Mic");
    await expect(page.locator("#voice-output-device")).toContainText("USB Speakers");
    await page.screenshot({ path: "../../.cache/ui-quickwins-workspace-en.png" });
    await page.evaluate(() => { window.__noxa.state.settings.language = "de"; window.__noxa.applyAppearance(); });
    await expect(page.locator("#channel-member-count")).toHaveText("1 im Sprachkanal");
    await expect(page.locator("#chat-pins-btn")).toHaveText("Angeheftete Nachrichten");
    await expect(page.locator("#voice-input-label")).toHaveText("Mikrofon");
    await expect(page.locator('#chat-scope option[value="direct"]')).toHaveText("Direktnachricht");
    await page.locator("#voice-options > summary").click();
    await page.locator(".channel-actions > summary").click();
    await page.setViewportSize({ width: 720, height: 800 });
    await page.screenshot({ path: "../../.cache/ui-quickwins-workspace-de-small.png" });
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});

test("@quickwins file actions and checksum stay accessible in both languages", async ({ page }) => {
    await page.evaluate(() => {
        const v = window.__noxa;
        v.state.myChannelID = 42;
        v.state.channels = [{ ChannelID: 42, Name: "Lounge" }];
        window.__fileListResponse = { entries: [{ name: "notes.txt", size: 1200, sha256: "a".repeat(64), uploader: "alice", uploaded_at: 1234 }], folders: [] };
        v.showWorkspace(false);
    });
    await page.locator("#tab-files").click();
    await expect(page.locator(".fb-grid thead")).not.toContainText("sha-256");
    await expect(page.locator(".fb-sha")).toBeHidden();
    await expect(page.getByRole("button", { name: "Download", exact: true })).toBeVisible();
    await page.locator(".fb-action-menu > summary").click();
    await expect(page.getByRole("button", { name: "Verify checksum", exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Verify checksum", exact: true }).click();
    await expect(page.locator(".fb-sha")).toHaveText("✓ ok");
    expect(await page.evaluate(() => window.__callArgs.VerifyFileForTab.at(-1))).toEqual([await page.evaluate(() => window.__noxa.state.activeTabID), 42, "", "notes.txt", "a".repeat(64)]);
    await page.evaluate(() => { window.__noxa.state.settings.language = "de"; window.__noxa.applyAppearance(); });
    await expect(page.locator(".fb-details summary")).toHaveText("Dateidetails");
    await page.locator(".fb-details summary").click();
    await expect(page.locator(".fb-sha")).toHaveText("a".repeat(64));
    await page.locator(".fb-action-menu > summary").click();
    await expect.poll(() => page.locator(".fb-action-list").evaluate(menu => {
        const bounds = menu.getBoundingClientRect();
        const list = menu.closest(".fb-list").getBoundingClientRect();
        return bounds.top >= list.top && bounds.bottom <= list.bottom;
    })).toBe(true);
    await page.screenshot({ path: "../../.cache/ui-quickwins-files-de.png" });
    await page.locator(".fb-action-menu > summary").press("Escape");
    await expect(page.locator(".fb-action-menu")).not.toHaveAttribute("open");
});

test("@quickwins leaving a channel keeps the server session until its authoritative event", async ({ page }) => {
    await page.evaluate(() => {
        const v = window.__noxa;
        v.state.activeTabID = "server-a";
        v.state.myClientID = "me";
        v.state.myChannelID = 42;
        v.state.channels = [{ ChannelID: 42, Name: "Lounge" }];
        v.state.clients = [{ client_id: "me", unique_id: "me", nickname: "Alice", channel_id: 42 }];
        v.showWorkspace(false);
        v.renderTree();
    });
    await page.locator("#voice-leave-channel").click();
    expect(await page.evaluate(() => window.__callArgs.JoinChannelForTab.at(-1))).toEqual(["server-a", 0]);
    expect(await page.evaluate(() => window.__calls.Disconnect || 0)).toBe(0);
    expect(await page.evaluate(() => window.__noxa.state.myChannelID)).toBe(42);
    await page.evaluate(() => {
        for (const callback of window.__events.event || []) callback(JSON.stringify({ type: "user_moved", data: { client_id: "me", from_channel_id: 42, channel_id: 0, by_client_id: "me" } }));
    });
    await expect(page.locator("#voice-leave-channel")).toBeDisabled();
    await expect(page.locator("#app")).toBeVisible();
    await expect(page.locator("#channel-member-count")).toHaveText("0 in voice");
});

test("@quickwins encrypted message recovery explains each failure and translates live", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        window.__noxa.openPM("peer", "Bob");
    });
    await expect(page.locator("#chat-head-title")).toHaveText("DM — Bob");
    await page.evaluate(() => {
        for (const callback of window.__events.event || []) callback(JSON.stringify({ type: "chat", data: { direct: true, from_unique_id: "peer", from: "Bob", text: "[encrypted message — decryption failed]", client_msg_id: "failure" } }));
    });
    await expect(page.locator(".missing-key .msg-text")).toHaveText("This message could not be decrypted. Ask the sender to resend it.");
    await expect(page.locator(".missing-key .msg-lock svg")).toHaveCount(1);
    await page.evaluate(() => { window.__noxa.state.settings.language = "de"; window.__noxa.applyAppearance(); });
    await expect(page.locator(".missing-key .msg-text")).toContainText("nicht entschlüsselt");
});

test("@quickwins transfer estimates and new-message counts translate without changing their meaning", async ({ page }) => {
    await page.evaluate(() => {
        const v = window.__noxa;
        v.showWorkspace(false);
        v.state.myChannelID = 42;
        v.state.channels = [{ ChannelID: 42, Name: "Lounge" }];
        v.refreshHeader();
        for (let id = 1; id <= 35; id++) {
            for (const callback of window.__events.event || []) callback(JSON.stringify({ type: "chat", data: { id, channel_id: 42, from: "Bob", from_unique_id: "peer", text: "Message " + id, enc: id === 35 } }));
        }
    });
    await expect(page.locator("#chat-log .msg.rich")).toHaveCount(35);
    await expect(page.locator("#chat-log .msg-lock")).toHaveAttribute("aria-label", /channel key/);
    await page.locator("#chat-log").evaluate(log => { log.scrollTop = 0; log.dispatchEvent(new Event("scroll")); });
    await page.evaluate(() => {
        for (const callback of window.__events.event || []) callback(JSON.stringify({ type: "chat", data: { id: 36, channel_id: 42, from: "Bob", from_unique_id: "peer", text: "New message" } }));
    });
    await expect(page.locator("#chat-newpill")).toHaveText("1 new message ↓");
    await page.evaluate(() => {
        for (const callback of window.__events.event || []) callback(JSON.stringify({ type: "chat", data: { id: 37, channel_id: 42, from: "Bob", from_unique_id: "peer", text: "Another message" } }));
        for (const callback of window.__events.ft_progress || []) callback({ id: "eta", name: "notes.zip", direction: "download", status: "active", total: 121000, transferred: 1000, bytes_per_sec: 1000 });
    });
    await expect(page.locator("#chat-newpill")).toHaveText("2 new messages ↓");
    const readPointer = await page.evaluate(() => window.__noxa.state.settings.last_read_channels?.[42]);
    await page.evaluate(() => { window.__noxa.state.settings.language = "de"; window.__noxa.applyAppearance(); });
    await expect(page.locator("#chat-newpill")).toHaveText("2 neue Nachrichten ↓");
    expect(await page.evaluate(() => window.__noxa.state.settings.last_read_channels?.[42])).toBe(readPointer);
    await page.locator("#chat-newpill").click();
    await expect(page.locator("#chat-newpill")).toBeHidden();
    await page.evaluate(() => { window.__noxa.state.settings.language = "en"; window.__noxa.applyAppearance(); });
    await page.locator("#tab-transfers").click();
    await expect(page.locator(".tr-meta")).toContainText("About 2 min remaining");
    await page.evaluate(() => { window.__noxa.state.settings.language = "de"; window.__noxa.applyAppearance(); });
    await expect(page.locator(".tr-meta")).toContainText("Noch etwa 2 Min.");
    await page.locator(".tr-close").click();
});

async function installSaveScenario(page, settings = {}) {
    await page.evaluate((settings) => {
        const app = window.go.main.App;
        window.__noxa.state.settings = { ...window.__noxa.state.settings, ...settings };
        window.__persistedSettings = structuredClone(window.__noxa.state.settings);
        window.__saveAttempts = 0;
        window.__saveMode = "pending";
        window.go.main.App = new Proxy(app, { get(target, method) {
            if (method === "GetSettings") return async () => {
                if (window.__refreshError) throw new Error("refresh unavailable");
                return structuredClone(window.__persistedSettings);
            };
            if (method === "SaveSettings") return async (value) => {
                window.__saveAttempts++;
                const snapshot = structuredClone(value);
                if (window.__saveMode === "pending") await new Promise(resolve => { window.__finishSave = resolve; });
                if (window.__saveMode === "reject") throw new Error("disk unavailable");
                if (window.__saveMode === "error") return "disk full";
                window.__persistedSettings = snapshot;
                return "";
            };
            return target[method];
        } });
    }, settings);
}

test("saving guest audio settings does not require whisper permission", async ({ page }) => {
    await installSaveScenario(page);
    await page.evaluate(() => {
        window.__saveMode = "success";
        window.__noxa.state.myClientID = "guest";
        const app = window.go.main.App;
        window.go.main.App = new Proxy(app, { get(target, method) {
            if (method === "WhisperSetForTab") return async () => { throw new Error("whisper denied"); };
            return target[method];
        } });
        window.__noxa.openSettings("capture");
    });
    await page.locator("#set-ok").click();
    await expect(page.locator("#settings-overlay")).toHaveCount(0);
});

test("persisted audio settings report an apply warning and allow retry", async ({ page }) => {
    await installSaveScenario(page);
    await page.evaluate(() => {
        window.__saveMode = "success";
        window.__noxa.applyLiveAudioSettings = async () => { throw new Error("microphone unavailable"); };
        window.__noxa.openSettings("capture");
    });
    await page.locator("#set-ok").click();
    await expect(page.locator(".settings-save-status")).toHaveText("Settings saved, but audio changes could not be applied: microphone unavailable");
    await expect(page.locator("#settings-overlay")).toBeVisible();
    expect(await page.evaluate(() => window.__saveAttempts)).toBe(1);
    await page.evaluate(() => { window.__noxa.applyLiveAudioSettings = async () => {}; });
    await page.locator("#set-ok").click();
    await expect(page.locator("#settings-overlay")).toHaveCount(0);
});

test("inactive camera tracks do not occupy the video grid", async ({ page }) => {
    await page.evaluate(async () => {
        window.__noxa.showWorkspace(false);
        const video = await import("/src/video.js");
        const canvas = document.createElement("canvas");
        const stream = canvas.captureStream(1);
        const track = stream.getVideoTracks()[0];
        window.__cameraPlaceholder = track;
        Object.defineProperty(track, "muted", { configurable: true, get: () => window.__cameraMuted });
        window.__cameraMuted = true;
        video.videoTrackAdded(track.id, stream, { client_id: "becksn", nickname: "becksn" });
    });
    await expect(page.locator("#video-grid")).toBeHidden();
    await page.evaluate(() => { window.__cameraMuted = false; window.__cameraPlaceholder.dispatchEvent(new Event("unmute")); });
    await expect(page.locator("#video-grid")).toBeVisible();
    await page.evaluate(() => { window.__cameraMuted = true; window.__cameraPlaceholder.dispatchEvent(new Event("mute")); });
    await expect(page.locator("#video-grid")).toBeHidden();
    await page.evaluate(() => { window.__cameraMuted = false; window.__cameraPlaceholder.dispatchEvent(new Event("unmute")); });
    await expect(page.locator("#video-grid")).toBeVisible();
});

test("incoming voice resumes suspended playback and applies the selected output", async ({ page }) => {
    await page.evaluate(async () => {
        const NativeContext = window.AudioContext;
        const input = new NativeContext();
        const stream = input.createMediaStreamDestination().stream;
        navigator.mediaDevices.getUserMedia = async () => stream;
        class FakePeerConnection {
            constructor() { this.senders = []; this.iceConnectionState = "connected"; }
            addTransceiver(track) {
                const sender = { track, getParameters: () => ({}), setParameters: async () => {} };
                this.senders.push(sender);
                return { sender };
            }
            getSenders() { return this.senders; }
            getTransceivers() { return []; }
            async createOffer() { return { type: "offer", sdp: "test" }; }
            async setLocalDescription() {}
            async setRemoteDescription() {}
            close() { this.connectionState = "closed"; }
        }
        window.RTCPeerConnection = FakePeerConnection;
        const state = window.__noxa.state;
        state.myClientID = "listener";
        state.myChannelID = 42;
        state.channels = [{ ChannelID: 42, Name: "Public" }];
        await window.__noxa.ensureVoiceForChannel();
        const playback = new NativeContext();
        await playback.suspend();
        window.__playback = playback;
        window.__sinks = [];
        playback.setSinkId = async (id) => { window.__sinks.push(id); };
        window.AudioContext = function () { return playback; };
        state.pc.ontrack({ track: stream.getAudioTracks()[0], streams: [stream] });
    });
    await expect.poll(() => page.evaluate(() => window.__playback.state)).toBe("running");
    await page.evaluate(async () => {
        window.__noxa.state.settings.playback_device_id = "speaker-2";
        await window.__noxa.applyLiveAudioSettings();
    });
    expect(await page.evaluate(() => window.__sinks.at(-1))).toBe("speaker-2");
    await page.evaluate(() => window.__playback.suspend());
    await page.locator("body").click({ position: { x: 5, y: 5 } });
    await expect.poll(() => page.evaluate(() => window.__playback.state)).toBe("running");
});

test("offline settings save does not require a live whisper connection", async ({ page }) => {
    await installSaveScenario(page);
    await page.evaluate(() => {
        window.__saveMode = "success";
        window.__noxa.state.myClientID = "";
        const app = window.go.main.App;
        window.go.main.App = new Proxy(app, { get(target, method) {
            if (method === "WhisperSetForTab") return async () => "not connected";
            return target[method];
        } });
        window.__noxa.openSettings();
    });
    await page.locator("#set-ok").click();
    await expect(page.locator("#settings-overlay")).toHaveCount(0);
    expect(await page.evaluate(() => window.__saveAttempts)).toBe(1);
});

test("bookmark retries after persistence succeeds but refreshing settings fails", async ({ page }) => {
    await installSaveScenario(page, { bookmarks: [{ name: "Original", addr: "example.test:12333", nickname: "Alice" }] });
    await page.evaluate(() => {
        window.__saveMode = "success";
        let failOnce = true;
        const app = window.go.main.App;
        window.go.main.App = new Proxy(app, { get(target, method) {
            if (method === "SaveSettings") return async value => {
                const error = await target.SaveSettings(value);
                // The real backend emits settings_update before resolving SaveSettings.
                for (const cb of window.__events.settings_update || []) cb(structuredClone(window.__persistedSettings));
                if (failOnce) { window.__refreshError = true; failOnce = false; }
                return error;
            };
            return target[method];
        } });
        window.__noxa.showWorkspace(false);
    });
    await page.locator("#menubar > .menu-item").filter({ hasText: /^Bookmarks/ }).click();
    await page.getByRole("menuitem", { name: "Manage bookmarks…", exact: true }).click();
    await page.locator(".bm-edit").click();
    const dialog = page.getByRole("dialog", { name: "Edit bookmark", exact: true });
    await dialog.locator(".bm-f-name").fill("Renamed");
    await dialog.getByRole("button", { name: "Save", exact: true }).click();
    await expect(dialog.locator(".bookmark-save-status")).toContainText("refresh unavailable");
    await page.evaluate(() => { window.__refreshError = false; });
    await dialog.getByRole("button", { name: "Save", exact: true }).click();
    await expect(dialog).toHaveCount(0);
    await expect(page.locator(".bm-name")).toHaveText("Renamed");
});

test("settings save blocks duplicates, retains draft after failure and retries", async ({ page }) => {
    const errors = [];
    page.on("pageerror", error => errors.push(error.message));
    await installSaveScenario(page);
    await page.evaluate(() => window.__noxa.openSettings());
    const dialog = page.locator("#settings-overlay");
    await page.getByRole("spinbutton", { name: "Chat max lines", exact: true }).fill("777");
    await page.locator("#set-apply").click();
    await expect(page.locator("#set-apply")).toBeDisabled();
    await expect(page.locator("#set-ok")).toBeDisabled();
    await page.keyboard.press("Escape");
    await expect(dialog).toBeVisible();
    await page.evaluate(() => {
        document.querySelector("#set-ok").click();
        window.__noxa.openSettings("playback");
    });
    expect(await page.evaluate(() => window.__saveAttempts)).toBe(1);
    await page.evaluate(() => { window.__saveMode = "reject"; window.__finishSave(); });
    await expect(dialog.locator(".settings-save-status")).toContainText("disk unavailable");
    await expect(page.locator("#set-apply")).toBeEnabled();
    await expect(page.getByRole("spinbutton", { name: "Chat max lines", exact: true })).toHaveValue("777");
    await page.evaluate(() => { window.__saveMode = "error"; });
    await page.locator("#set-ok").click();
    await expect(dialog.locator(".settings-save-status")).toContainText("disk full");
    await expect(dialog).toBeVisible();
    await page.evaluate(() => { window.__saveMode = "success"; });
    await page.locator("#set-ok").click();
    await expect(dialog).toHaveCount(0);
    expect(await page.evaluate(() => window.__noxa.state.settings.chat_max_lines)).toBe(777);
    expect(errors).toEqual([]);
});

test("bookmark save keeps edits on failure and commits only on success", async ({ page }) => {
    await installSaveScenario(page, { bookmarks: [{ name: "Original", addr: "example.test:12333", nickname: "Alice" }] });
    await page.evaluate(() => window.__noxa.showWorkspace(false));
    await page.locator("#menubar > .menu-item").filter({ hasText: /^Bookmarks/ }).click();
    await page.getByRole("menuitem", { name: "Manage bookmarks…", exact: true }).click();
    await page.locator(".bm-edit").click();
    const dialog = page.getByRole("dialog", { name: "Edit bookmark", exact: true });
    await dialog.locator(".bm-f-name").fill("Renamed");
    await dialog.getByRole("button", { name: "Save", exact: true }).click();
    await expect(dialog).toBeVisible();
    await expect(dialog.getByRole("button", { name: "Saving…", exact: true })).toBeDisabled();
    expect(await page.evaluate(() => window.__noxa.state.settings.bookmarks[0].name)).toBe("Original");
    await page.keyboard.press("Escape");
    await expect(dialog).toBeVisible();
    await page.evaluate(() => { window.__saveMode = "error"; window.__finishSave(); });
    await expect(dialog.locator(".bookmark-save-status")).toContainText("disk full");
    await expect(dialog.locator(".bm-f-name")).toHaveValue("Renamed");
    await page.evaluate(() => { window.__saveMode = "reject"; });
    await dialog.getByRole("button", { name: "Save", exact: true }).click();
    await expect(dialog.locator(".bookmark-save-status")).toContainText("disk unavailable");
    await page.evaluate(() => { window.__saveMode = "success"; });
    await dialog.getByRole("button", { name: "Save", exact: true }).click();
    await expect(dialog).toHaveCount(0);
    await expect(page.locator(".bm-name")).toHaveText("Renamed");
    expect(await page.evaluate(() => window.__noxa.state.settings.bookmarks[0].name)).toBe("Renamed");
});

test("DND save reports errors and changes state only after success", async ({ page }) => {
    await installSaveScenario(page, { dnd_enabled: false });
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        window.__dndFeedback = [];
        window.__noxa.toast = (...args) => window.__dndFeedback.push(args);
    });
    const toggle = async () => {
        await page.locator("#menubar > .menu-item").filter({ hasText: /^View/ }).click();
        await page.getByRole("menuitem", { name: "Do not disturb", exact: true }).click();
    };
    await toggle();
    expect(await page.evaluate(() => window.__noxa.state.settings.dnd_enabled)).toBe(false);
    await toggle();
    expect(await page.evaluate(() => window.__saveAttempts)).toBe(1);
    await page.evaluate(() => { window.__saveMode = "error"; window.__finishSave(); });
    await expect.poll(() => page.evaluate(() => window.__dndFeedback)).toEqual([["save failed: disk full", "warn", "alert", { bypassDND: true }]]);
    expect(await page.evaluate(() => window.__noxa.state.settings.dnd_enabled)).toBe(false);
    await page.evaluate(() => { window.__saveMode = "reject"; });
    await toggle();
    await expect.poll(() => page.evaluate(() => window.__dndFeedback.at(-1))).toEqual(["save failed: disk unavailable", "warn", "alert", { bypassDND: true }]);
    await page.evaluate(() => { window.__saveMode = "success"; });
    await toggle();
    await expect.poll(() => page.evaluate(() => window.__noxa.state.settings.dnd_enabled)).toBe(true);
    expect(await page.evaluate(() => window.__dndFeedback.at(-1))).toEqual(["do not disturb on", "info", "alert", { bypassDND: true }]);
});

test("DND disable failures remain visible while ordinary notifications stay muted", async ({ page }) => {
    await installSaveScenario(page, { dnd_enabled: true });
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        window.__saveMode = "error";
        window.__noxa.toast("ordinary notification");
    });
    await expect(page.locator("#toasts .toast").filter({ hasText: "ordinary notification" })).toHaveCount(0);
    await page.locator("#menubar > .menu-item").filter({ hasText: /^View/ }).click();
    await page.getByRole("menuitem", { name: "Do not disturb", exact: true }).click();
    await expect(page.locator("#toasts .toast").filter({ hasText: "save failed: disk full" })).toBeVisible();
    expect(await page.evaluate(() => window.__noxa.state.settings.dnd_enabled)).toBe(true);
});

test("audio output failures warn once and ignore obsolete selections", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.state.settings.playback_device_id = "missing-speaker";
        const element = { setSinkId: async () => { throw new Error("device missing"); } };
        for (let n = 0; n < 5; n++) window.__noxa.applyOutputSettings(element);
    });
    await expect(page.locator("#toasts .toast").filter({ hasText: "Could not switch audio output" })).toHaveCount(1);
    await page.evaluate(async () => {
        window.__noxa.state.settings.playback_device_id = "old-speaker";
        window.__noxa.applyOutputSettings({ setSinkId: () => new Promise((_, reject) => { window.__failOldOutput = reject; }) });
        await Promise.resolve();
        window.__noxa.state.settings.playback_device_id = "working-speaker";
        window.__failOldOutput(new Error("obsolete error"));
    });
    await expect(page.locator("#toasts .toast").filter({ hasText: "Could not switch audio output" })).toHaveCount(1);
});

test("settings search announces counts and explains the result cap", async ({ page }, testInfo) => {
    await page.evaluate(() => window.__noxa.openSettings());
    const search = page.locator("#settings-search");
    await search.fill("a");
    await expect(page.locator(".set-search-hit")).toHaveCount(40);
    await expect(page.locator(".settings-search-summary")).toContainText(/Showing 40 of \d+ results\. Narrow your search/);
    await page.screenshot({ path: testInfo.outputPath("settings-search-count.png") });
    await search.fill("voice volume");
    await expect(page.locator(".settings-search-summary")).toHaveText("1 result");
    await search.fill("nothing-matches-this-value");
    await expect(page.locator(".settings-search-summary")).toHaveText("0 results");
    await search.fill("");
    await expect(page.locator(".settings-search-summary")).toBeHidden();
    await page.evaluate(async () => (await import("/src/i18n.js")).setLanguage("de"));
    await search.fill("sprachlautstärke");
    await expect(page.locator(".settings-search-summary")).toHaveText("1 Ergebnis");
    await search.fill("a");
    await expect(page.locator(".settings-search-summary")).toContainText(/40 von \d+ Ergebnissen angezeigt/);
});

test("settings search reuses labels and invalidates after edits, language changes and reopen", async ({ page }) => {
    const errors = [];
    page.on("pageerror", error => errors.push(error.message));
    await page.evaluate(() => {
        window.__noxa.openSettings();
        window.__searchControls = 0;
        const createElement = document.createElement.bind(document);
        document.createElement = (name, options) => {
            if (name === "select") window.__searchControls++;
            return createElement(name, options);
        };
    });
    const search = page.locator("#settings-search");
    await search.fill("volume");
    await expect(page.locator(".set-search-hit").first()).toBeVisible();
    const firstBuild = await page.evaluate(() => window.__searchControls);
    expect(firstBuild).toBeGreaterThan(0);
    await search.fill("volum");
    await search.fill("volume");
    expect(await page.evaluate(() => window.__searchControls)).toBe(firstBuild);
    expect(await page.evaluate(() => window.__calls.ListIdentities || 0)).toBe(0);
    await search.fill("voice volume");
    await page.locator(".set-search-hit").first().click();
    await expect(page.getByRole("slider", { name: "Voice volume", exact: true })).toBeVisible();
    await page.getByRole("slider", { name: "Voice volume", exact: true }).fill("65");
    const beforeEditedSearch = await page.evaluate(() => window.__searchControls);
    await search.fill("volume");
    expect(await page.evaluate(() => window.__searchControls)).toBeGreaterThan(beforeEditedSearch);
    await page.evaluate(async () => (await import("/src/i18n.js")).setLanguage("de"));
    const beforeGermanSearch = await page.evaluate(() => window.__searchControls);
    await search.fill("lautstärke");
    await expect(page.locator(".set-search-hit").first()).toContainText("Wiedergabe");
    const germanBuild = await page.evaluate(() => window.__searchControls);
    expect(germanBuild).toBeGreaterThan(beforeGermanSearch);
    await search.fill("lautstärk");
    expect(await page.evaluate(() => window.__searchControls)).toBe(germanBuild);
    await page.keyboard.press("Escape");
    await page.evaluate(async () => {
        (await import("/src/i18n.js")).setLanguage("en");
        window.__noxa.openSettings();
    });
    const beforeReopenedSearch = await page.evaluate(() => window.__searchControls);
    await search.fill("volume");
    expect(await page.evaluate(() => window.__searchControls)).toBeGreaterThan(beforeReopenedSearch);
    expect(errors).toEqual([]);
});

test("German menus translate remaining actions and bookmark dialogs", async ({ page }, testInfo) => {
    await page.evaluate(async () => {
        window.__noxa.showWorkspace(false);
        (await import("/src/i18n.js")).setLanguage("de");
        (await import("/src/menu.js")).initMenu();
    });
    await page.getByRole("menuitem", { name: "Ansicht", exact: true }).click();
    for (const name of ["Details ein-/ausblenden", "Chat in eigenem Fenster", "Nicht stören", "Immer im Vordergrund", "Design: dunkel", "Design: hell", "Design: hoher Kontrast"]) {
        await expect(page.getByRole("menuitem", { name, exact: true })).toBeVisible();
    }
    await page.screenshot({ path: testInfo.outputPath("german-view-menu.png") });
    await page.keyboard.press("Escape");
    await page.getByRole("menuitem", { name: "Lesezeichen", exact: true }).click();
    await expect(page.getByRole("menuitem", { name: "Aktuellen Server als Lesezeichen speichern", exact: true })).toBeVisible();
    await page.getByRole("menuitem", { name: "Lesezeichen verwalten…", exact: true }).click();
    const dialog = page.getByRole("dialog", { name: "Lesezeichen verwalten", exact: true });
    await expect(dialog).toContainText("Noch keine Lesezeichen");
    await dialog.getByRole("button", { name: "Schließen", exact: true }).click();
    await page.getByRole("menuitem", { name: "Selbst", exact: true }).click();
    await page.getByRole("menuitem", { name: "Spitznamen ändern…", exact: true }).click();
    await expect(page.getByRole("dialog", { name: "Spitznamen ändern", exact: true })).toContainText("Spitzname für die nächste Verbindung:");
    await page.getByRole("button", { name: "Abbrechen", exact: true }).click();
});

test("client language translates every settings page and persists on Apply @a11y", async ({ page }, testInfo) => {
    const errors = [];
    page.on("pageerror", error => errors.push(error.message));
    await page.evaluate(() => {
        const app = window.go.main.App;
        window.go.main.App = new Proxy(app, { get(target, method) {
            if (method === "GetSettings") return async () => structuredClone(window.__savedSettings || window.__noxa.state.settings);
            if (method === "ListIdentities") return async () => [{ id: "test", name: "My identity", unique_id: "identity-123456789", active: true, protection: "dpapi", security_level: 4 }];
            return target[method];
        } });
        window.__noxa.openSettings();
    });
    await page.getByRole("combobox", { name: "Language", exact: true }).selectOption("de");
    await page.getByRole("spinbutton", { name: "Chat max lines", exact: true }).fill("500");
    await page.getByRole("button", { name: "Apply", exact: true }).click();
    await expect(page.getByRole("dialog", { name: "Einstellungen", exact: true })).toBeVisible();
    await expect(page.getByRole("combobox", { name: "Sprache", exact: true })).toHaveValue("de");
    await expect(page.locator("html")).toHaveAttribute("lang", "de");
    await expect(page.getByRole("spinbutton", { name: "Chat max. Zeilen", exact: true })).toHaveValue("500");
    await page.screenshot({ path: testInfo.outputPath("settings-german-application.png") });
    const pages = [
        ["Anwendung", "Chat max. Zeilen"], ["Aufnahme", "Aufnahmegerät"],
        ["Wiedergabe", "Ausgabegerät"], ["Tastenkürzel", "Als neues Profil speichern…"],
        ["Flüstern", "Flüstern aktivieren"], ["Downloads", "Downloadordner"],
        ["Chat", "Zeitstempel"], ["Sicherheit", "Identitäten"],
        ["Server", "Maximale Clientanzahl (0 = unbegrenzt)"],
        ["Benachrichtigungen", "Gesprochene Systemmeldungen"],
    ];
    for (const [tab, label] of pages) {
        await page.getByRole("tab", { name: tab, exact: true }).click();
        await expect(page.locator("#settings-content").getByText(label, { exact: true })).toBeVisible();
        if (tab === "Sicherheit") {
            await expect(page.getByText("My identity", { exact: false })).toBeVisible();
            await page.screenshot({ path: testInfo.outputPath("settings-german-security.png") });
        }
    }
    await expect(page.getByRole("button", { name: "Sprachmeldung testen", exact: true })).toBeVisible();
    await page.screenshot({ path: testInfo.outputPath("settings-german-notifications.png") });
    expect(await page.locator(".notify-matrix").evaluate(table => {
        const content = document.getElementById("settings-content");
        return table.getBoundingClientRect().right <= content.getBoundingClientRect().right;
    })).toBe(true);
    await page.getByRole("textbox", { name: "Einstellungen suchen", exact: true }).fill("Lautstärke");
    await expect(page.locator(".set-search-hit").first()).toContainText("Wiedergabe");
    await page.getByRole("button", { name: "Abbrechen", exact: true }).click();
    await page.evaluate(() => window.__noxa.openSettings());
    await expect(page.getByRole("combobox", { name: "Sprache", exact: true })).toHaveValue("de");
    await page.getByRole("combobox", { name: "Sprache", exact: true }).selectOption("en");
    await page.getByRole("button", { name: "Abbrechen", exact: true }).click();
    expect(await page.evaluate(() => window.__noxa.state.settings.language)).toBe("de");
    await page.evaluate(() => window.__noxa.openSettings());
    await page.getByRole("combobox", { name: "Sprache", exact: true }).selectOption("en");
    await page.getByRole("button", { name: "OK", exact: true }).click();
    await page.evaluate(() => window.__noxa.openSettings());
    await expect(page.getByRole("dialog", { name: "Settings", exact: true })).toBeVisible();
    await expect(page.getByRole("combobox", { name: "Language", exact: true })).toHaveValue("en");
    expect(errors).toEqual([]);
});

test("terminal audio finishes its cue before speech and suppresses disconnect cascades", async ({ page }) => {
    await page.evaluate(async () => {
        const { state, soundEngine, speechQueue } = window.__noxa;
        Object.assign(state.settings, { play_sounds: true, effects_enabled: true, spoken_messages: true,
            language: "en", speech_volume: 90, sound_volume: 100, event_sounds: {}, speech_events: {}, notify_matrix: {}, dnd_enabled: false });
        state.myClientID = "audio-self"; state.replayingTabID = "";
        state.lastConnect = { addr: "localhost" };
        await soundEngine.preload(); await soundEngine.resume(); speechQueue.clear();
        window.__audioTimeline = [];
        const play = soundEngine.play.bind(soundEngine);
        soundEngine.play = (id, options = {}) => {
            const started = performance.now();
            const ok = play(id, { ...options, onEnded: () => {
                window.__audioTimeline.push({ id, end: performance.now() }); options.onEnded?.();
            } });
            if (ok) window.__audioTimeline.push({ id, start: started });
            return ok;
        };
        for (const cb of window.__events.event) cb(JSON.stringify({ type: "kicked", data: {
            client_id: "audio-self", ban: true, from_server: true, reason: "visual-only-reason" } }));
        for (const cb of window.__events.disconnected) cb();
    });
    await expect.poll(() => page.evaluate(() => window.__audioTimeline.some(x => x.id === "speech_en_banned" && x.start))).toBeTruthy();
    const timeline = await page.evaluate(() => window.__audioTimeline);
    expect(timeline.filter(x => x.start).map(x => x.id)).toEqual(["ban", "speech_en_banned"]);
    const gap = timeline.find(x => x.id === "speech_en_banned" && x.start).start - timeline.find(x => x.id === "ban" && x.end).end;
    expect(gap).toBeGreaterThanOrEqual(140);
    expect(gap).toBeLessThan(400);
    await expect(page.getByText(/visual-only-reason/).first()).toBeVisible();
});

test("individual and all speech previews use draft settings and stop on close", async ({ page }, testInfo) => {
    await page.evaluate(() => {
        Object.assign(window.__noxa.state.settings, { language: "en", spoken_messages: true, effects_enabled: false,
            speech_admin: true, speech_connection: true, speech_events: {}, event_sounds: {}, notify_matrix: {}, dnd_enabled: false });
        window.__noxa.openSettings("notifications");
    });
    await expect(page.getByRole("button", { name: "Preview You were banned from the server.", exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Preview You were banned from the server.", exact: true }).click();
    await expect.poll(() => page.evaluate(() => window.__noxa.speechQueue.current?.event)).toBe("banned");
    await page.getByRole("button", { name: "Stop preview", exact: true }).click();
    await page.getByRole("checkbox", { name: "Do not disturb", exact: true }).check();
    await page.getByRole("button", { name: "Preview all spoken messages", exact: true }).click();
    expect(await page.evaluate(() => window.__noxa.speechQueue.current)).toBeNull();
    await page.getByRole("checkbox", { name: "Do not disturb", exact: true }).uncheck();
    await page.getByRole("button", { name: "Preview all spoken messages", exact: true }).scrollIntoViewIfNeeded();
    await page.screenshot({ path: testInfo.outputPath("noxa-audio-settings.png"), fullPage: true });
    await page.getByRole("button", { name: "Preview all spoken messages", exact: true }).click();
    await expect.poll(() => page.evaluate(() => window.__noxa.speechQueue.current?.preview)).toBe(true);
    await page.getByRole("button", { name: "Cancel", exact: true }).click();
    await expect.poll(() => page.evaluate(() => window.__noxa.speechQueue.current)).toBeNull();
    expect(await page.evaluate(() => window.__noxa.state.settings.effects_enabled)).toBe(false);
});

test("static speech follows language, rare events, draft volume and mute settings", async ({ page }) => {
    await page.evaluate(async () => {
        const { state, soundEngine, speechQueue } = window.__noxa;
        state.settings = { ...state.settings, language:"de", play_sounds:true, sound_volume:100, speech_volume:75,
            spoken_messages:true, speech_admin:true, speech_connection:true, speech_events:{}, event_sounds:{}, notify_matrix:{}, dnd_enabled:false };
        state.myClientID="speech-self";state.replayingTabID="";
        await soundEngine.preload();await soundEngine.resume();
        window.__speechPlayed=[];
        const play=soundEngine.play.bind(soundEngine);
        soundEngine.play=(id, options)=>{const ok=play(id,options);if(ok)window.__speechPlayed.push({id,volume:options?.volume});return ok;};
        speechQueue.clear();
        for(const cb of window.__events.event)cb(JSON.stringify({type:"kicked",data:{client_id:"speech-self",ban:true,from_server:true,reason:"a dynamic reason must remain visual"}}));
    });
    await expect.poll(()=>page.evaluate(()=>window.__speechPlayed.map(x=>x.id))).toContain("speech_de_banned");
    expect(await page.evaluate(()=>window.__speechPlayed.filter(x=>x.id.startsWith("speech_")).map(x=>x.id))).toEqual(["speech_de_banned"]);
    await page.evaluate(()=>{
        const {state,speechQueue}=window.__noxa;speechQueue.clear();
        state.settings.language="en";state.settings.play_sounds=false;
        window.__noxa.openSettings("notifications");
    });
    await page.getByRole("slider",{name:"Speech volume",exact:true}).fill("200");
    await page.getByRole("button",{name:"Test spoken message",exact:true}).click();
    await expect.poll(()=>page.evaluate(()=>window.__speechPlayed.at(-1))).toEqual({id:"speech_en_test",volume:200});
    expect(await page.evaluate(()=>window.__noxa.state.settings.speech_volume)).toBe(75);
    await page.getByRole("button",{name:"Stop preview",exact:true}).click();
    await page.getByRole("checkbox",{name:"Spoken system messages",exact:true}).uncheck();
    const count=await page.evaluate(()=>window.__speechPlayed.length);
    await page.getByRole("button",{name:"Test spoken message",exact:true}).click();
    await page.waitForTimeout(250);
    expect(await page.evaluate(()=>window.__speechPlayed.length)).toBe(count);
    await page.getByRole("button",{name:"Cancel",exact:true}).click();
    expect(await page.evaluate(()=>window.__noxa.speechQueue.current)).toBeNull();
});

test("sound previews use draft volume, finish Test All, and cancel on close @a11y", async ({ page }) => {
    await page.evaluate(async () => {
        const { state, soundEngine } = window.__noxa;
        state.settings = { ...state.settings, play_sounds: false, sound_volume: 100, event_sounds: {}, dnd_enabled: false };
        await soundEngine.preload();
        window.__previewedSounds = [];
        const original = soundEngine.play.bind(soundEngine);
        soundEngine.play = (name, options) => {
            const result = original(name, options);
            if (result) window.__previewedSounds.push({ name, volume: options?.settings?.sound_volume });
            return result;
        };
        window.__noxa.openSettings("notifications");
    });
    const volume = page.getByRole("slider", { name: "Sound volume", exact: true });
    await volume.fill("0");
    await page.getByRole("button", { name: "Preview Joined channel", exact: true }).click();
    expect(await page.evaluate(() => window.__previewedSounds.length)).toBe(0);
    await volume.fill("200");
    await page.getByRole("button", { name: "Preview Joined channel", exact: true }).click();
    await expect.poll(() => page.evaluate(() => window.__previewedSounds.at(-1))).toEqual({ name: "own_channel_join", volume: 200 });
    expect(await page.evaluate(() => window.__noxa.state.settings.sound_volume)).toBe(100);
    await page.getByRole("button", { name: "Stop preview", exact: true }).click();
    await page.evaluate(() => { window.__previewedSounds = []; });
    await page.getByRole("button", { name: "Test all sounds", exact: true }).click();
    await expect(page.getByRole("status").filter({ hasText: "Preview finished" })).toBeVisible({ timeout: 20000 });
    expect(await page.evaluate(() => new Set(window.__previewedSounds.map(x => x.name)).size)).toBe(33);
    await page.getByRole("button", { name: "Preview connection", exact: true }).click();
    await page.getByRole("button", { name: "Cancel", exact: true }).click();
    await expect.poll(() => page.evaluate(() => window.__noxa.soundEngine.active.size)).toBe(0);
    const count = await page.evaluate(() => window.__previewedSounds.length);
    await page.waitForTimeout(650);
    expect(await page.evaluate(() => window.__previewedSounds.length)).toBe(count);
});

async function prepareServerInformation(page) {
    await page.evaluate(() => {
        const app = window.go.main.App;
        window.__serverInfo = {
            name: "noXa community", version: "test", platform: "linux/amd64",
            uptime_seconds: 60, clients_online: 2, max_clients: 100, channels_online: 1,
        };
        window.go.main.App = new Proxy(app, { get(target, key) {
            if (key === "ServerInfoForTab") return async () => structuredClone(window.__serverInfo);
            return target[key];
        } });
        window.runtime.ClipboardSetText = async (value) => { window.__copiedAddress = value; return true; };
        window.__noxa.state.myClientID = "client-a";
        window.__noxa.state.lastConnect = { addr: "voice.example:12333" };
        window.__noxa.showWorkspace(false);
    });
}

test("server information opens from the name, Connections menu and latency with accessible traffic details @a11y", async ({ page }, testInfo) => {
    await prepareServerInformation(page);
    await page.setViewportSize({ width: 1004, height: 768 });
    await page.evaluate(() => {
        window.__noxa.state.pc = { getStats: async () => new Map([
            ["in", { id: "in", type: "inbound-rtp", kind: "audio", packetsReceived: 990, packetsLost: 10, jitter: .004, bytesReceived: 2097152, timestamp: 1000 }],
            ["out", { id: "out", type: "outbound-rtp", kind: "audio", packetsSent: 2000, bytesSent: 4194304, timestamp: 1000 }],
        ]) };
    });
    await page.locator("#server-name").click();
    const dialog = page.getByRole("dialog", { name: "Server information" });
    await expect(dialog.locator('[data-stat="name"]')).toHaveText("noXa community");
    await expect(dialog.locator('[data-stat="address"]')).toHaveText("voice.example:12333");
    await expect(dialog.locator('[data-stat="platform"]')).toHaveText("linux/amd64");
    await expect(dialog.locator('[data-stat="control-in"]')).toHaveText("2.00 KiB");
    await expect(dialog.locator('[data-stat="control-out"]')).toHaveText("1.00 KiB");
    await expect(dialog.locator('[data-stat="loss"]')).toHaveText("1.00 %");
    await expect(dialog.locator('[data-stat="jitter"]')).toHaveText("4.0 ms");
    await expect(dialog.locator('[data-stat="media-in"]')).toHaveText("2.00 MiB");
    await expect(dialog.locator('[data-stat="rate-in"]')).toHaveText("—");
    await dialog.getByRole("button", { name: "Copy server address" }).click();
    expect(await page.evaluate(() => window.__copiedAddress)).toBe("voice.example:12333");
    await auditAccessibility(page, "server information");
    await dialog.locator(".server-info").screenshot({ path: testInfo.outputPath("server-information.png") });
    await page.keyboard.press("Escape");
    await expect(page.locator("#server-name")).toBeFocused();
    await page.getByRole("menuitem", { name: "Connections", exact: true }).click();
    await page.getByRole("menuitem", { name: "Server information", exact: true }).click();
    await expect(dialog).toBeVisible();
    await page.keyboard.press("Escape");
    await page.evaluate(() => {
        const button = document.getElementById("voice-latency");
        button.hidden = false;
        button.textContent = "12 ms";
    });
    await page.locator("#voice-latency").click();
    await expect(dialog).toBeVisible();
    await page.setViewportSize({ width: 420, height: 600 });
    await expect(dialog.getByRole("button", { name: "Close", exact: true })).toBeInViewport();
    expect(await dialog.locator(".server-info").evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(true);
    await dialog.locator(".server-info").screenshot({ path: testInfo.outputPath("server-information-small.png") });
});

test("server information shows reported video processors without inventing GPU support or retaining a replaced peer", async ({ page }, testInfo) => {
    await prepareServerInformation(page);
    await page.clock.install();
    await page.evaluate(() => {
        window.__processorStats = () => new Map([
            ["vp8", { id: "vp8", type: "codec", mimeType: "video/VP8" }],
            ["send", { id: "send", type: "outbound-rtp", kind: "video", codecId: "vp8", framesEncoded: 10, encoderImplementation: "libvpx", powerEfficientEncoder: false }],
            ["receive", { id: "receive", type: "inbound-rtp", kind: "video", codecId: "vp8", framesDecoded: 10, decoderImplementation: "<img src=x onerror=alert(1)>" }],
        ]);
        window.__noxa.state.pc = { getStats: async () => window.__processorStats() };
    });
    await page.locator("#server-name").click();
    const dialog = page.getByRole("dialog", { name: "Server information" });
    await dialog.getByText("Video processing diagnostics", { exact: true }).click();
    const send = dialog.locator('[data-video-processors="encoders"]');
    const receive = dialog.locator('[data-video-processors="decoders"]');
    await expect(send).toHaveText("VP8 · libvpx · power efficient: no");
    await expect(receive).toHaveText("VP8 · <img src=x onerror=alert(1)> · power efficient: not reported");
    await expect(receive.locator("img")).toHaveCount(0);
    await page.setViewportSize({ width: 420, height: 600 });
    expect(await dialog.locator(".server-info-body").evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true);
    await dialog.locator(".server-info-processing").scrollIntoViewIfNeeded();
    await dialog.locator(".server-info").screenshot({ path: testInfo.outputPath("video-processing.png") });
    await page.evaluate(() => {
        window.__noxa.state.pc.getStats = () => new Promise(resolve => { window.__releaseProcessorStats = resolve; });
    });
    await page.clock.runFor(2000);
    await expect.poll(() => page.evaluate(() => typeof window.__releaseProcessorStats)).toBe("function");
    await page.evaluate(() => {
        window.__noxa.state.pc = { getStats: async () => new Map() };
        window.__releaseProcessorStats(window.__processorStats());
    });
    await expect(send).toHaveText("—");
    await expect(receive).toHaveText("—");
});

test("server information suspends hidden polling, avoids overlapping calls and stops on close", async ({ page }) => {
    await prepareServerInformation(page);
    await page.clock.install();
    await page.locator("#server-name").click();
    const dialog = page.getByRole("dialog", { name: "Server information" });
    await expect(dialog.locator('[data-stat="ping"]')).toHaveText("12 ms");
    const calls = () => page.evaluate(() => window.__calls.GetClientInfoForTab || 0);
    const initial = await calls();
    await page.clock.runFor(1000);
    expect(await calls()).toBe(initial);
    await page.evaluate(() => { window.__clientInfoGate = new Promise((resolve) => { window.__finishInfo = resolve; }); });
    await page.clock.runFor(1000);
    await expect.poll(calls).toBe(initial + 1);
    await page.clock.runFor(10000);
    expect(await calls()).toBe(initial + 1);
    await page.evaluate(() => window.__finishInfo());
    await page.evaluate(() => {
        window.__testHidden = true;
        Object.defineProperty(document, "hidden", { configurable: true, get: () => window.__testHidden });
        document.dispatchEvent(new Event("visibilitychange"));
    });
    await page.clock.runFor(10000);
    expect(await calls()).toBe(initial + 1);
    await page.evaluate(() => { window.__testHidden = false; document.dispatchEvent(new Event("visibilitychange")); });
    await expect.poll(calls).toBe(initial + 2);
    await page.keyboard.press("Escape");
    await page.clock.runFor(10000);
    expect(await calls()).toBe(initial + 2);
});

test("server information handles older servers and discards late results after a tab reset", async ({ page }) => {
    await prepareServerInformation(page);
    await page.evaluate(() => {
        delete window.__serverInfo.platform;
        window.__clientInfoResponse = { ping_ms: -1 };
    });
    await page.locator("#server-name").click();
    const dialog = page.getByRole("dialog", { name: "Server information" });
    await expect(dialog.locator('[data-stat="platform"]')).toHaveText("Unavailable on this server");
    for (const key of ["ping", "loss", "media-in", "control-in"]) await expect(dialog.locator(`[data-stat="${key}"]`)).toHaveText("—");
    await page.keyboard.press("Escape");
    await page.evaluate(() => { window.__clientInfoGate = new Promise((resolve) => { window.__finishInfo = resolve; }); });
    await page.locator("#server-name").click();
    await page.evaluate(() => { for (const cb of window.__events.tab_reset) cb("another-tab"); });
    await expect(dialog).toHaveCount(0);
    await page.evaluate(() => window.__finishInfo());
    await expect(dialog).toHaveCount(0);
    await page.evaluate(() => { window.__noxa.state.myClientID = "other-client"; window.__serverInfo.name = "Other server"; window.__noxaMeta.openServerInfo(); });
    await expect(dialog.locator('[data-stat="name"]')).toHaveText("Other server");
});

async function showB3Workspace(page) {
    await page.evaluate(() => {
        const v = window.__noxa;
        Object.assign(v.state, {
            myClientID: "daniel", myUniqueID: "uid-daniel", myChannelID: 2,
            channels: [
                { ChannelID: 1, Name: "Echo Test", ParentID: 0 },
                { ChannelID: 2, Name: "Public", ParentID: 0, Topic: "Open voice chat for everyone, including guests." },
                { ChannelID: 3, Name: "Gaming", ParentID: 0 },
            ],
            clients: ["Daniel", "Alex", "Mia", "Jonas"].map((nickname) => ({
                client_id: nickname.toLowerCase(), unique_id: "uid-" + nickname.toLowerCase(),
                nickname, channel_id: 2, is_speaking: nickname === "Mia",
            })),
        });
        v.showWorkspace(false);
        v.renderTree();
    });
}

test("selected quick wins group consecutive authors and count every search hit", async ({ page }) => {
    await showB3Workspace(page);
    await page.evaluate(() => {
        const base = Date.now();
        for (const [i, name, text] of [[1, "Alex", "needle needle <tag>"], [2, "Alex", "needle again"], [3, "Mia", "something else"]]) {
            window.__noxaChat.addChat({ id: 9000 + i, channel_id: 2, from: name,
                from_unique_id: "uid-" + name.toLowerCase(), text, sent_at: base + i, edited: i === 2 });
        }
    });
    await expect(page.locator('[data-msg-id="9002"]')).toHaveClass(/grouped/);
    const grouped = page.locator('[data-msg-id="9002"]');
    const timestamp = await grouped.locator('.msg-time').boundingBox(), body = await grouped.locator('.msg-text').boundingBox();
    expect(Math.abs(timestamp.y - body.y)).toBeLessThan(6);
    await expect(page.locator('[data-msg-id="9003"]')).not.toHaveClass(/grouped/);
    await page.locator("#chat-search-btn").click();
    await page.locator("#chat-search").fill("needle");
    await expect(page.locator("#chat-search-count")).toContainText("2 of");
    await expect(page.locator('#chat-log mark')).toHaveCount(3);
    await expect(page.locator('[data-msg-id="9001"] .msg-text')).toContainText("<tag>");
    await expect(page.locator('[data-msg-id="9001"] tag')).toHaveCount(0);
    await page.locator("#chat-search").fill("absent");
    await expect(page.locator("#chat-search-count")).toContainText("0 of");
});

test("selected quick wins retain voice context and explicit mute semantics", async ({ page }) => {
    await showB3Workspace(page);
    await expect(page.locator("#voice-context")).toContainText("Public");
    await page.getByRole("button", { name: "Mute microphone", exact: true }).click();
    await expect(page.locator("#voice-mute")).toHaveText("Microphone muted");
    await expect(page.locator("#voice-mute svg")).toHaveCount(1);
    await expect(page.getByRole("button", { name: "Leave voice channel", exact: true })).toBeVisible();
    await expect(page.getByRole("button", { name: "Disconnect server", exact: true })).toBeVisible();
    await page.getByRole("tab", { name: "Files", exact: true }).click();
    await expect(page.locator("#voice-context")).toContainText("Public");
});

test("selected quick wins retry only the unchanged failed draft", async ({ page }) => {
    await showB3Workspace(page);
    await page.evaluate(() => {
        window.__retryPayloads = [];
        window.__sendChatHandler = (_scope, _target, text) => {
            window.__retryPayloads.push(text);
            return new Promise(resolve => { window.__resolveRetry = resolve; });
        };
    });
    await page.locator("#chat-text").fill("first draft");
    await page.locator("#chat-send").click();
    await expect.poll(() => page.evaluate(() => window.__retryPayloads.length)).toBe(1);
    await page.locator("#chat-text").fill("new draft");
    await page.evaluate(() => window.__resolveRetry("offline"));
    await expect(page.locator("#chat-send-error")).toContainText("offline");
    await expect(page.locator("#chat-retry")).toBeHidden();
    await page.locator("#chat-send").click();
    await expect.poll(() => page.evaluate(() => window.__retryPayloads.length)).toBe(2);
    await page.evaluate(() => window.__resolveRetry("offline again"));
    await page.locator("#chat-retry").click();
    await expect.poll(() => page.evaluate(() => window.__retryPayloads)).toEqual(["first draft", "new draft", "new draft"]);
    await page.evaluate(() => window.__resolveRetry(""));
    await expect(page.locator("#chat-text")).toHaveValue("");
    await expect(page.locator("#chat-send-error")).toBeHidden();
});

test("selected quick wins retry failed attachments without repeating successful text", async ({ page }) => {
    await showB3Workspace(page);
    await page.evaluate(() => {
        window.__uploadCount = 0;
        window.__sentQuick = [];
        window.__uploadAttachmentHandler = () => {
            if (++window.__uploadCount === 1) throw new Error("upload offline");
            return "[file:retry.vcx#dGVzdA==#retry.txt]";
        };
        window.__sendChatHandler = (_scope, _target, text) => { window.__sentQuick.push(text); return ""; };
    });
    await page.locator("#chat-file").setInputFiles({ name: "retry.txt", mimeType: "text/plain", buffer: Buffer.from("retry") });
    await expect(page.locator(".file-preview")).toHaveCount(1);
    await page.locator("#chat-text").fill("successful text");
    await page.locator("#chat-send").click();
    await expect(page.locator("#chat-text")).toHaveValue("");
    await page.locator("#chat-retry").click();
    await expect(page.locator(".file-preview")).toHaveCount(0);
    expect(await page.evaluate(() => window.__sentQuick)).toEqual(["successful text", "[file:retry.vcx#dGVzdA==#retry.txt]"]);
});

test("selected quick wins microphone meter measures muted input and releases its clone", async ({ page }) => {
    await showB3Workspace(page);
    await page.evaluate(async () => {
        const ctx = new AudioContext();
        const osc = ctx.createOscillator(), dest = ctx.createMediaStreamDestination();
        osc.connect(dest); osc.start(); await ctx.resume();
        const source = dest.stream.getAudioTracks()[0];
        const clone = source.clone.bind(source);
        source.clone = () => { window.__meterClone = clone(); return window.__meterClone; };
        source.enabled = false;
        window.__meterSource = source;
        window.__meterAudio = await import("/src/audio.js");
        window.__meterAudio.startMicMeter(dest.stream);
        window.__meterCleanup = () => { source.stop(); osc.stop(); return ctx.close(); };
    });
    await expect.poll(() => page.locator("#mic-meter").getAttribute("aria-valuenow").then(Number)).toBeGreaterThan(0);
    expect(await page.evaluate(() => window.__meterSource.enabled)).toBe(false);
    await page.evaluate(() => window.__meterAudio.stopMicMeter());
    await expect(page.locator("#mic-meter")).toBeHidden();
    expect(await page.evaluate(() => window.__meterClone.readyState)).toBe("ended");
    expect(await page.evaluate(() => window.__meterSource.readyState)).toBe("live");
    await page.evaluate(() => window.__meterCleanup());
});

test("selected quick wins completed downloads open their scoped folder", async ({ page }) => {
    await showB3Workspace(page);
    await page.evaluate(() => {
        window.__noxa.state.activeTabID = "download-tab";
        for (const callback of window.__events.ft_progress || []) {
            callback({ id: "finished", name: "notes.zip", direction: "download", status: "done", total: 100, transferred: 100 });
            callback({ id: "pending", name: "pending.zip", direction: "download", status: "active", total: 100, transferred: 1 });
            callback({ id: "upload", name: "upload.zip", direction: "upload", status: "done", total: 100, transferred: 100 });
        }
    });
    await page.locator("#tab-transfers").click();
    await expect(page.getByRole("button", { name: "Open folder", exact: true })).toHaveCount(1);
    await page.getByRole("button", { name: "Open folder", exact: true }).click();
    expect(await page.evaluate(() => window.__callArgs.OpenDownloadFolderForTab)).toEqual([["download-tab", "finished"]]);
    await page.evaluate(() => { window.__noxa.state.settings.language = "de"; window.__noxa.applyAppearance(); });
    await expect(page.getByRole("button", { name: "Ordner öffnen", exact: true })).toHaveCount(1);
});

test("private groups restore persisted unread state after native tab activation", async ({ page }) => {
    await showB3Workspace(page);
    await page.evaluate(() => {
        const app = window.go.main.App;
        window.go.main.App = new Proxy(app, { get(target, key) {
            if (key === "ConversationForTab") return async () => ({ conversations: [{ id: "restored", name: "Restored private group", owner: "uid-daniel", revision: 1, epoch: 1, unread_count: 3, read_message_id: 4, latest_message_id: 7, members: [{ unique_id: "uid-daniel", pending: false }] }], messages: [] });
            if (key === "SessionInfoForTab") return async () => ({ connected: true, client_id: "client-daniel", unique_id: "uid-daniel", authorization_model: "roles-v1" });
            return target[key];
        } });
        for (const callback of window.__events.tab_reset || []) callback("restored-server");
    });
    await expect(page.locator('.group-list-item[data-group-id="restored"] .group-unread')).toHaveText("3");
});

test("private group sidebar resizes and collapses within the full application layout", async ({ page }, testInfo) => {
    await showB3Workspace(page);
    await page.evaluate(async () => {
        window.__noxa.state.activeTabID = "group-layout-server";
        const app = window.go.main.App;
        window.go.main.App = new Proxy(app, { get(target, key) {
            if (key === "ConversationForTab") return async () => ({ conversations: [], messages: [] });
            return target[key];
        } });
        const groups = await import("/src/conversations.js"); groups.initConversations();
    });
    const split = page.locator("#sidebar-scroll");
    const divider = page.getByRole("separator", { name: "Resize channels and private groups" });
    await expect(page.locator(".group-sidebar-status")).toHaveText("No private groups yet.");
    const initial = await divider.boundingBox(), bounds = await split.boundingBox();
    expect(initial.y).toBeGreaterThan(bounds.y + bounds.height * 0.7);
    await page.screenshot({ path: testInfo.outputPath("group-sidebar-empty.png") });
    await page.mouse.move(initial.x + initial.width / 2, initial.y + 4);
    await page.mouse.down(); await page.mouse.move(initial.x + initial.width / 2, bounds.y + bounds.height / 2, { steps: 6 }); await page.mouse.up();
    expect((await divider.boundingBox()).y).toBeLessThan(initial.y - 50);
    await page.getByRole("button", { name: "Private groups", exact: true }).click();
    await expect(page.locator(".group-sidebar-body")).toBeHidden();
    await page.evaluate(() => { window.__noxa.state.settings.language = "de"; window.__noxa.applyAppearance(); });
    await page.getByRole("button", { name: "Private Gruppen", exact: true }).click();
    await expect(page.getByRole("separator", { name: "Größe von Kanälen und privaten Gruppen anpassen" })).toBeVisible();
    await page.screenshot({ path: testInfo.outputPath("group-sidebar-resized.png") });
});

test("private group workspace keeps hidden channel messages unread until returning to channel", async ({ page }) => {
    await showB3Workspace(page);
    await page.evaluate(async () => {
        window.__noxa.state.activeTabID = "group-read-server";
        const app = window.go.main.App;
        const group = { id: "read-group", name: "Private team", owner: "uid-daniel", revision: 1, epoch: 1,
            members: [{ unique_id: "uid-daniel", pending: false, joined_epoch: 1 }] };
        window.go.main.App = new Proxy(app, { get(target, key) {
            if (key === "ConversationForTab") return async () => ({ conversations: [structuredClone(group)], messages: [] });
            if (key === "ChatHistoryForTab") return async () => ({ messages: [] });
            return target[key];
        } });
        window.__noxaChat.onMyChannelChanged();
        window.__noxaChat.addChat({ id: 9201, channel_id: 2, from: "Alex", from_unique_id: "uid-alex", text: "Already read in channel" });
        const groups = await import("/src/conversations.js");
        groups.initConversations();
    });
    await expect(page.locator('#chat-log [data-msg-id="9201"]')).toBeVisible();
    await expect.poll(() => page.evaluate(() => window.__savedSettings?.last_read_channels?.[2])).toBe(9201);
    await page.locator("#private-groups-sidebar").getByRole("button", { name: "Private team", exact: true }).click();
    await expect(page.locator("#private-groups .group-content h3")).toHaveText("Private team");
    await expect(page.locator("#chat-pane")).toBeHidden();
    await page.evaluate(() => {
        for (const callback of window.__events.event || []) callback(JSON.stringify({ type: "chat", data: {
            id: 9202, channel_id: 2, from: "Alex", from_unique_id: "uid-alex", text: "Unread while private team is open",
        } }));
    });
    await expect.poll(() => page.evaluate(() => window.__noxa.chatUnread(2)?.n)).toBe(1);
    await expect(page.locator('#chat-log [data-msg-id="9202"]')).toHaveCount(0);
    // Exercise a real rerender while hidden as well as the incoming-message path.
    await page.evaluate(() => window.dispatchEvent(new Event("noxa-language-changed")));
    expect(await page.evaluate(() => window.__noxa.state.settings.last_read_channels[2])).toBe(9201);
    expect(await page.evaluate(() => window.__savedSettings.last_read_channels[2])).toBe(9201);
    await page.getByRole("button", { name: "Back to channel", exact: true }).click();
    await expect(page.locator("#chat-pane")).toBeVisible();
    await expect(page.locator('#chat-log [data-msg-id="9202"]')).toBeVisible();
    expect(await page.evaluate(() => window.__noxa.chatUnread(2))).toBeNull();
    await expect.poll(() => page.evaluate(() => window.__savedSettings?.last_read_channels?.[2])).toBe(9202);
});

test("selected quick wins anchor scrollback across trimming and language changes", async ({ page }) => {
    await showB3Workspace(page);
    await page.evaluate(() => {
        window.__noxa.state.settings.chat_max_lines = 80;
        window.__noxaChat.onMyChannelChanged();
        for (let id = 1; id <= 80; id++) window.__noxaChat.addChat({ id, channel_id: 2, from: "Alex", from_unique_id: "alex", text: "Message " + id });
    });
    await expect(page.locator("#chat-log .msg.rich")).toHaveCount(80);
    const log = page.locator("#chat-log");
    await log.evaluate(log => { log.scrollTop = log.scrollHeight / 2; log.dispatchEvent(new Event("scroll")); });
    const anchor = await log.evaluate(log => {
        const row = [...log.querySelectorAll(".msg.rich")].find(row => row.getBoundingClientRect().top >= log.getBoundingClientRect().top);
        return { key: row.dataset.scrollKey, y: row.getBoundingClientRect().top - log.getBoundingClientRect().top };
    });
    const readPointer = await page.evaluate(() => window.__noxa.state.settings.last_read_channels?.[2]);
    await page.evaluate(() => {
        for (let id = 81; id <= 85; id++) window.__noxaChat.addChat({ id, channel_id: 2, from: "Alex", from_unique_id: "alex", text: "Message " + id });
        window.__noxa.state.settings.language = "de";
        window.__noxa.applyAppearance();
    });
    const after = await log.evaluate((log, key) => [...log.querySelectorAll(".msg.rich")].find(row => row.dataset.scrollKey === key).getBoundingClientRect().top - log.getBoundingClientRect().top, anchor.key);
    expect(Math.abs(after - anchor.y)).toBeLessThan(2);
    expect(await page.evaluate(() => window.__noxa.state.settings.last_read_channels?.[2])).toBe(readPointer);
    await expect(page.locator("#chat-newpill")).toHaveText("5 neue Nachrichten ↓");
});

for (const change of ["history edit", "history deletion", "concurrent edit", "concurrent reaction"]) test(`selected quick wins reconcile cached history and preserve pagination: ${change}`, async ({ page }) => {
    await showB3Workspace(page);
    await page.evaluate(() => {
        window.__historyCursors = [];
        const app = window.go.main.App;
        window.go.main.App = new Proxy(app, { get(target, key) {
            if (key !== "ChatHistoryForTab") return target[key];
            return async (_tab, _channel, before) => {
                window.__historyCursors.push(before);
                if (before) return { messages: [] };
                return new Promise(resolve => { window.__resolveHistory = resolve; });
            };
        } });
        for (const id of [100, 200]) window.__noxaChat.addChat({ id, channel_id: 2, from: "Alex", from_unique_id: "alex", text: "cached", version: 1 });
        window.__noxaChat.onMyChannelChanged();
    });
    await expect.poll(() => page.evaluate(() => typeof window.__resolveHistory)).toBe("function");
    await page.evaluate(change => {
        if (change === "concurrent edit") window.__noxaChat.onChatEdited({ message_id: 200, body: "concurrent", version: 3 });
        if (change === "concurrent reaction") window.__noxaChat.onChatReaction({ message_id: 200, reactions: { "👍": 3 } });
        const messages = Array.from({ length: 50 }, (_, i) => ({
            id: 249 - i, from_nickname: "Alex", from_unique_id: "alex", body: "authoritative", version: 2,
            edited: true, deleted: change === "history deletion" && i === 49,
        }));
        window.__resolveHistory({ messages });
    }, change);
    await expect(page.locator("#chat-log .msg.rich")).toHaveCount(50);
    await expect(page.locator('[data-msg-id="100"]')).toHaveCount(0);
    await expect(page.locator('[data-msg-id="200"] .msg-text')).toHaveText(change === "concurrent edit" ? "concurrent" : change === "history deletion" ? "Message deleted" : "authoritative");
    if (change === "concurrent reaction") await expect(page.locator('[data-msg-id="200"] .react-chip')).toHaveText("👍 3");
    await page.locator("#chat-log").evaluate(log => { log.scrollTop = 0; log.dispatchEvent(new Event("scroll")); });
    await expect.poll(() => page.evaluate(() => window.__historyCursors)).toEqual([0, 200]);
});

test("selected quick wins fullscreen keeps names and share audio has independent percentages", async ({ page }) => {
    await showB3Workspace(page);
    await page.evaluate(async () => {
        const v = window.__noxa;
        v.state.clients.push({ client_id: "screen-peer", nickname: "Screen Person", unique_id: "screen-uid" });
        window.__shareLevel = { volume: 75, muted: false };
        v.shareAudioCtl = {
            get: () => window.__shareLevel,
            setVolume: (_id, volume) => { window.__shareLevel.volume = volume; },
            setMuted: (_id, muted) => { window.__shareLevel.muted = muted; },
        };
        const canvas = document.createElement("canvas");
        const context = canvas.getContext("2d");
        window.__videoPaint = setInterval(() => { context.fillStyle = "#123456"; context.fillRect(0, 0, 300, 150); }, 50);
        const stream = canvas.captureStream(20), track = stream.getVideoTracks()[0];
        Object.defineProperty(track, "id", { value: "screen-peer|screen" });
        window.__quickVideo = await import("/src/video.js");
        window.__quickVideo.videoTrackAdded(track.id, stream, { client_id: "screen-peer" });
    });
    const tile = page.locator('.vtile[data-slot="screen"]');
    await expect(tile).toBeVisible();
    const gridBounds = await page.locator('#video-grid').boundingBox(), nameBounds = await tile.locator('.vtile-name').boundingBox();
    expect(nameBounds.y).toBeGreaterThanOrEqual(gridBounds.y);
    expect(nameBounds.y + nameBounds.height).toBeLessThanOrEqual(gridBounds.y + gridBounds.height);
    await tile.hover();
    await tile.locator(".vtile-fullscreen").click();
    await expect.poll(() => page.evaluate(() => document.fullscreenElement?.className || "")).toContain("vtile");
    await expect(tile.locator(".vtile-name")).toBeVisible();
    await expect(tile.locator(".vtile-name")).toHaveText("Screen Person");
    await page.keyboard.press("Escape");
    await expect.poll(() => page.evaluate(() => !!document.fullscreenElement)).toBe(false);
    await tile.click({ button: "right" });
    await page.getByRole("slider", { name: "Shared audio", exact: true }).fill("45");
    await expect(page.locator(".ctx-vol-pct")).toHaveText("45%");
    expect(await page.evaluate(() => window.__shareLevel.volume)).toBe(45);
    await page.getByText("Mute shared audio", { exact: true }).click();
    expect(await page.evaluate(() => window.__shareLevel.muted)).toBe(true);
    await expect(tile).toBeVisible();
    await page.evaluate(async () => { await window.__quickVideo.setLowBandwidth(true, false); });
    await expect(tile.locator(".vtile-preview-age")).toHaveText("Preview updated just now");
    await page.clock.install();
    await page.clock.fastForward(123000);
    await expect(tile.locator(".vtile-preview-age")).toHaveText("Preview updated 2 minutes ago");
    await page.evaluate(() => { window.__noxa.state.settings.language = "de"; window.__noxa.applyAppearance(); });
    await expect(tile.locator(".vtile-preview-age")).toHaveText("Vorschau vor 2 Minuten aktualisiert");
    await expect(tile.locator(".vtile-kind")).toHaveText("Bildschirmfreigabe");
    await page.evaluate(() => { clearInterval(window.__videoPaint); window.__quickVideo.clearVideoGrid(); });
    await expect(page.locator("#video-grid")).toBeHidden();
});

test("B3 participant strip follows live channel membership and opens member controls", async ({ page }) => {
    await showB3Workspace(page);
    const strip = page.getByRole("region", { name: "Voice participants" });
    await expect(strip.getByRole("button")).toHaveCount(4);
    await strip.getByRole("button", { name: /Mia.*speaking/ }).click();
    await expect(page.locator("#client-card .card-nick")).toHaveText("Mia");
    await page.getByRole("slider", { name: "User volume" }).fill("75");
    await page.getByRole("slider", { name: "User volume" }).press("Tab");
    await expect.poll(() => page.evaluate(() => window.__savedSettings?.user_volumes?.["uid-mia"])).toBe(75);
    await page.getByRole("button", { name: "Message Mia", exact: true }).click();
    await expect(page.locator("#chat-head-title")).toContainText("Mia");
    await page.evaluate(() => {
        window.__noxa.state.myChannelID = 3;
        window.__noxa.renderTree();
    });
    await expect(strip.getByRole("button")).toHaveCount(0);
    await expect(strip).toContainText("No one else is here yet");
});

test("B3 shows your detected speech even when your own playback is muted or deafened", async ({ page }) => {
    await showB3Workspace(page);
    await page.evaluate(() => {
        const v = window.__noxa;
        v.state.settings.muted_users = ["uid-daniel"];
        v.setDeafened(true);
        for (const cb of window.__events.event) cb(JSON.stringify({
            type: "speaking_changed", data: { client_id: "daniel", speaking: true },
        }));
    });
    const self = page.locator('#voice-participants [data-client-id="daniel"]');
    await expect(self).toContainText("Talking");
    await expect(self).toHaveClass(/speaking/);
    await expect(page.locator('#channel-tree .client[data-clid="daniel"]')).toHaveClass(/speaking/);
    await page.getByRole("tab", { name: "Files", exact: true }).click();
    await expect(self).toBeVisible();
    await expect(self).toContainText("Talking");
    await page.evaluate(() => {
        for (const cb of window.__events.event) cb(JSON.stringify({
            type: "speaking_changed", data: { client_id: "daniel", speaking: false },
        }));
    });
    await expect(self).toContainText("In voice");
    await expect(self).not.toHaveClass(/speaking/);
    await page.locator("#voice-mute").click();
    await expect(self).toContainText("Microphone muted");
});

test("tray follows detected self speech, input/output mute, and voice teardown without polling", async ({ page }) => {
    await showB3Workspace(page);
    await page.evaluate(() => {
        window.__noxa.state.pc = { close() {} };
        window.__noxa.renderTree();
        for (const cb of window.__events.event) cb(JSON.stringify({
            type: "speaking_changed", data: { client_id: "daniel", speaking: true },
        }));
    });
    const flags = () => page.evaluate(() => window.__callArgs.SetTrayVoiceState?.at(-1));
    await expect.poll(flags).toEqual([true, false, false]);
    const count = await page.evaluate(() => window.__calls.SetTrayVoiceState);
    await page.evaluate(() => {
        for (let i = 0; i < 20; i++) window.__noxa.renderTree();
        for (const cb of window.__events.event) cb(JSON.stringify({
            type: "speaking_changed", data: { client_id: "mia", speaking: false },
        }));
    });
    expect(await page.evaluate(() => window.__calls.SetTrayVoiceState)).toBe(count);
    await page.locator("#voice-deafen").click();
    await expect.poll(flags).toEqual([true, false, true]);
    await page.locator("#voice-mute").click();
    await expect.poll(flags).toEqual([false, true, true]);
    await page.locator("#voice-deafen").click();
    await expect.poll(flags).toEqual([false, true, false]);
    await page.locator("#voice-mute").click();
    await expect.poll(flags).toEqual([true, false, false]);
    await page.evaluate(() => window.__noxa.resetVoiceSession());
    await expect.poll(flags).toEqual([false, false, false]);
});

test("B3 keeps voice controls outside the Chat and Files panels @a11y", async ({ page }) => {
    await showB3Workspace(page);
    await page.evaluate(() => window.__noxa.openPM("uid-mia", "Mia"));
    await expect(page.locator("#chat-head-title")).toContainText("Mia");
    await page.getByRole("tab", { name: "Files", exact: true }).click();
    await expect(page.locator("#chat-head-title")).toHaveText("Public");
    await expect(page.locator("#files-pane")).toBeVisible();
    await expect(page.locator("#chat-pane")).toBeHidden();
    await expect(page.locator("#chat-input-row")).toBeHidden();
    await expect(page.getByRole("button", { name: "Mute microphone", exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Mute microphone", exact: true }).click();
    await expect(page.getByRole("button", { name: "Unmute microphone", exact: true })).toHaveAttribute("aria-pressed", "true");
    await page.getByRole("button", { name: "Deafen", exact: true }).click();
    await expect(page.getByRole("button", { name: "Undeafen", exact: true })).toHaveAttribute("aria-pressed", "true");
    await auditAccessibility(page, "B3 files and voice toolbar");
    await page.getByRole("tab", { name: "Chat", exact: true }).click();
    await expect(page.locator("#chat-head-title")).toContainText("Mia");
});

test("B3 alpha details work with the keyboard and notifications stay readable @a11y", async ({ page }, testInfo) => {
    await showB3Workspace(page);
    await page.locator("#alpha-badge").focus();
    await page.keyboard.press("Enter");
    await expect(page.locator(".alpha-notice")).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(page.locator("#alpha-badge")).toBeFocused();
    await page.evaluate(() => {
        const self = window.__noxa.state.clients.find((c) => c.client_id === "daniel");
        self.is_speaking = true;
        window.__noxa.renderTree();
        window.__noxaPolish.recordNotification("warn", "insufficient permission: b_channel_modify");
    });
    for (const width of [1000, 640]) {
        await page.setViewportSize({ width, height: width === 1000 ? 730 : 480 });
        await page.screenshot({ path: testInfo.outputPath(`workspace-${width}.png`) });
        if (width === 640) await page.locator("#workspace-sidebar-toggle").click();
        await page.locator("#notif-bell").click();
        const message = page.locator(".nc-text");
        await expect(message).toHaveText("insufficient permission: b_channel_modify");
        expect(await message.evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(true);
        await auditAccessibility(page, `notifications ${width}`);
        await page.screenshot({ path: testInfo.outputPath(`notifications-${width}.png`) });
        await page.getByRole("button", { name: "Close notifications", exact: true }).click();
    }
});

test("B3 restores the persisted member volume after a failed save", async ({ page }) => {
    await showB3Workspace(page);
    await page.evaluate(() => {
        const app = window.go.main.App;
        window.go.main.App = new Proxy(app, {
            get(target, method) {
                if (method === "SaveSettings") return async (value) => {
                    if (window.__failVolumeSave) return "disk full";
                    window.__savedSettings = structuredClone(value);
                    return "";
                };
                if (method === "GetSettings") return async () => structuredClone(window.__savedSettings);
                return target[method];
            },
        });
    });
    await page.locator('#voice-participants [data-client-id="mia"]').click();
    const slider = page.getByRole("slider", { name: "User volume" });
    await slider.fill("75");
    await expect.poll(() => page.evaluate(() => window.__noxa.state.settings.user_volumes?.["uid-mia"])).toBe(75);
    await page.evaluate(() => { window.__failVolumeSave = true; });
    await slider.fill("150");
    await expect(page.locator("#member-action-error")).toHaveText("Could not save volume. Try again.");
    await expect(slider).toHaveValue("75");
    await expect(page.locator("#member-volume-value")).toHaveText("75%");
    await expect.poll(() => page.evaluate(() => window.__savedSettings.user_volumes["uid-mia"])).toBe(75);
});

test("B3 ignores a volume save failure after selecting another member", async ({ page }) => {
    await showB3Workspace(page);
    await page.evaluate(() => {
        const app = window.go.main.App;
        window.go.main.App = new Proxy(app, {
            get(target, method) {
                if (method === "SaveSettings") return () => new Promise((resolve) => { window.__finishVolumeSave = resolve; });
                return target[method];
            },
        });
    });
    await page.locator('#voice-participants [data-client-id="mia"]').click();
    await page.getByRole("slider", { name: "User volume" }).fill("150");
    await expect.poll(() => page.evaluate(() => typeof window.__finishVolumeSave)).toBe("function");
    await page.locator('#voice-participants [data-client-id="alex"]').click();
    await page.evaluate(() => window.__finishVolumeSave("disk full"));
    await expect(page.locator("#client-card .card-nick")).toHaveText("Alex");
    await expect(page.getByRole("slider", { name: "User volume" })).toHaveValue("100");
    await expect(page.locator("#member-volume-value")).toHaveText("100%");
    await expect(page.locator("#member-action-error")).toBeHidden();
});

test("B3 shows the details opener only while the pane is closed", async ({ page }) => {
    await showB3Workspace(page);
    const opener = page.locator("#details-toggle");
    await expect(opener).toBeVisible();
    await opener.click();
    await expect(opener).toBeHidden();
    await page.locator("#details-close").click();
    await expect(opener).toBeVisible();
    await expect(opener).toBeFocused();
});

test("B3 fits desktop and small windows without losing the composer or toolbar", async ({ page }) => {
    await showB3Workspace(page);
    for (const viewport of [{ width: 1440, height: 900 }, { width: 1000, height: 730 }, { width: 640, height: 480 }]) {
        await page.setViewportSize(viewport);
        await expect(page.locator("#chat-text")).toBeVisible();
        await expect(page.locator("#voice-mute")).toBeVisible();
        await expect(page.locator("#voice-disconnect")).toBeVisible();
        const bounds = await page.evaluate(() => ({
            overflow: document.documentElement.scrollWidth > innerWidth,
            composer: document.getElementById("chat-input-row").getBoundingClientRect().bottom,
            toolbar: document.getElementById("voice-bar").getBoundingClientRect().bottom,
        }));
        expect(bounds.overflow).toBe(false);
        expect(bounds.composer).toBeLessThanOrEqual(viewport.height);
        expect(bounds.toolbar).toBeLessThanOrEqual(viewport.height);
    }
});

const settings = {
    settings_version: 4,
    capture_device_id: "",
    playback_device_id: "",
    activation_mode: "ptt",
    vad_threshold: 25,
    volume: 100,
    chat_max_lines: 200,
    window_opacity: 100,
    camera_fps: 30,
    sound_volume: 100,
    auto_away_minutes: 0,
    notification_matrix: {},
    bookmarks: [],
    onboarding_done: true,
    alpha_dismissed: "test",
};

test.beforeEach(async ({ page }) => {
    await page.addInitScript(({ initialSettings }) => {
        window.__events = {};
        window.__calls = {};
        window.__callArgs = {};
        window.__browserURLs = [];
        window.__savedSettings = null;
        window.__tabs = [];
        window.runtime = {
            EventsOn(name, callback) {
                const listeners = (window.__events[name] ||= []);
                listeners.push(callback);
                return () => {
                    const index = listeners.indexOf(callback);
                    if (index >= 0) listeners.splice(index, 1);
                };
            },
            EventsEmit() {},
            WindowIsFullscreen: async () => false,
            BrowserOpenURL(url) {
                window.__browserURLs.push(url);
                if (window.__browserOpenThrow) throw new Error("browser unavailable");
                return window.__browserOpenReject ? Promise.reject(new Error("browser unavailable")) : Promise.resolve();
            },
        };
        const app = new Proxy({}, {
            get(_target, nativeMethod) {
                return async (...args) => {
                    let method = nativeMethod;
                    window.__calls[method] = (window.__calls[method] || 0) + 1;
                    (window.__callArgs[method] ||= []).push(structuredClone(args));
                    if (method.endsWith("ForContext")) {
                        method = method.replace(/ForContext$/, "");
                        args = args.slice(1);
                        (window.__callArgs[method] ||= []).push(structuredClone(args));
                    }
                    if (method === "DMHistoryContextForTab") return { tab_id: args[0], identity_uid: window.__dmStorageIdentity || "playwright-identity", activation: "0", identity_revision: "0" };
                    if (method === "GetSettings") {
                        const persisted = sessionStorage.getItem("startup-settings");
                        if (persisted) {
                            await new Promise((resolve) => { window.__resolveStartupSettings = resolve; });
                            return JSON.parse(persisted);
                        }
                        return structuredClone(initialSettings);
                    }
                    if (method === "SaveSettings") { window.__savedSettings = structuredClone(args[0]); return ""; }
                    if (method === "GetServerConfigForTab") return {
                        max_clients: 100, client_timeout_seconds: 90, opus_bitrate: 64000,
                        opus_fec: true, opus_dtx: false, opus_stereo: false,
                    };
                    if (method === "SetServerConfigForTab") return structuredClone(args[1]);
                    if (method === "CertificateClockWarning") return window.__certificateClockWarning || "";
                    if (method === "ConnectBookmarkTabWithID") {
                        if (typeof window.__connectBookmarkHandler === "function") {
                            return await window.__connectBookmarkHandler(...args);
                        }
                        if (window.__connectBookmarkGate) await window.__connectBookmarkGate;
                        return {
                            tab_id: window.__connectTabID || "",
                            error: window.__connectBookmarkResult || "",
                        };
                    }
                    if (method === "ConnectGuestBookmarkTabWithID") {
                        if (typeof window.__guestConnectHandler === "function") {
                            return await window.__guestConnectHandler(...args);
                        }
                        return { tab_id: window.__guestConnectTabID || "", error: "" };
                    }
                    if (method === "ListTabs") return structuredClone(window.__tabs);
                    if (method === "DMHistoryLoad") return structuredClone(window.__dmHistory?.[args[0]] || []);
                    if (method === "Connected") {
                        if (window.__connectedGate) await window.__connectedGate;
                        return !!window.__connected;
                    }
                    if (method === "ClientID") {
                        if (window.__clientIDGate) await window.__clientIDGate;
                        return window.__activeClient || "client-a";
                    }
                    if (method === "SessionInfoForTab") {
                        const session = {
                            authorization_model: window.__authorizationModel || "",
                            client_id: window.__activeClient || "client-a", is_admin: true, is_guest: false,
                            connected: window.__tabs.find((tab) => tab.id === args[0])?.connected ?? true, security: "",
                        };
                        if (window.__clientIDGate) await window.__clientIDGate;
                        return session;
                    }
                    if (method === "IsAdmin") return true;
                    if (method === "ChannelEditForTab") {
                        if (window.__channelEditReject) throw new Error("Connection lost");
                        if (window.__channelEditGate) await window.__channelEditGate;
                        return window.__channelEditError || "";
                    }
                    if (method === "ChannelEditTreeForTab") return window.__channelTreeError || "";
                    if (method === "IsGuest") return false;
                    if (method === "IdentityUID") return "playwright-identity";
                    if (method === "ClientVersionShort") return "test";
                    if (method === "ClientVersion") {
                        if (window.__clientVersionGate) await window.__clientVersionGate;
                        if (window.__clientVersionReject) throw new Error("version unavailable");
                        return window.__clientVersion || "test";
                    }
                    if (method === "DisconnectTab" && typeof window.__disconnectTabHandler === "function") {
                        return await window.__disconnectTabHandler(...args);
                    }
                    if (method === "Disconnect" || method === "DisconnectTab") {
                        if (typeof window.__disconnectHandler === "function") {
                            return await window.__disconnectHandler(...args);
                        }
                        if (window.__disconnectReject) throw new Error("disconnect unavailable");
                        return "";
                    }
                    if (method === "CloseTab" && typeof window.__closeTabHandler === "function") {
                        return await window.__closeTabHandler(...args);
                    }
                    if (method === "SendICECandidateForTab") {
                        if (window.__sendICECandidateReject) throw new Error("signal closed");
                        return "";
                    }
                    if (method === "UploadChatAttachment" || method === "UploadChatAttachmentForTab") {
                        if (method.endsWith("ForTab")) args = args.slice(1);
                        if (typeof window.__uploadAttachmentHandler === "function") {
                            return await window.__uploadAttachmentHandler(...args);
                        }
                        if (window.__uploadAttachmentReject) throw new Error("upload unavailable");
                        return window.__uploadAttachmentResult || "[file:blob.vcx#dGVzdA==#file.bin]";
                    }
                    if (method === "DownloadChatAttachment" || method === "DownloadChatAttachmentForTab") {
                        if (method.endsWith("ForTab")) args = args.slice(1);
                        if (typeof window.__downloadAttachmentHandler === "function") {
                            return await window.__downloadAttachmentHandler(...args);
                        }
                        if (window.__attachmentGate) await window.__attachmentGate;
                        if (window.__attachmentReject) throw new Error(window.__attachmentReject);
                        return window.__attachmentData || "";
                    }
                    if (method === "SendChat" || method === "SendChatForTab") {
                        if (method.endsWith("ForTab")) args = args.slice(1);
                        if (typeof window.__sendChatHandler === "function") {
                            return await window.__sendChatHandler(...args);
                        }
                        if (window.__sendChatReject) throw new Error("send unavailable");
                        return window.__sendChatResult || "";
                    }
                    if (method === "SendChatReply" || method === "SendChatReplyForTab") {
                        if (window.__sendChatReplyReject) throw new Error("reply unavailable");
                        return window.__sendChatReplyResult || "";
                    }
                    if (method === "VerifyFileForTab") {
                        if (window.__verifyFileGate) await window.__verifyFileGate;
                        if (window.__verifyFileReject) throw new Error("verify unavailable");
                        return window.__verifyFileResult ?? true;
                    }
                    if (method === "FileListForTab") {
                        if (window.__fileListGate) await window.__fileListGate;
                        return structuredClone(window.__fileListResponse || {
                            entries: [], folders: [], used_bytes: 0, quota_bytes: 0,
                        });
                    }
                    if (method === "SaveChatAttachment" || method === "SaveChatAttachmentForTab") {
                        if (window.__saveAttachmentGate) await window.__saveAttachmentGate;
                        return window.__saveAttachmentResult || "";
                    }
                    if (method === "WebRTCAnswerForTab" && window.__webRTCAnswerReject) {
                        throw new Error("answer rejected");
                    }
                    if (method === "BanListForTab") return structuredClone(window.__bans || { bans: [] });
                    if (method === "CheckForUpdate") return structuredClone(window.__updateInfo || { available: false, version: "test", size: 0 });
                    if (method === "IdentityInfo") return {};
                    if (method === "GetAvatarForTab") return structuredClone(window.__avatarResponse || {});
                    if (method === "ServerIconGetForTab" || method === "ServerBannerGetForTab" || method === "ChannelIconGetForTab" || method === "EmojiGetForTab") {
                        return structuredClone(window.__assetResponse || {});
                    }
                    if (method === "GetClientInfoForTab") {
                        const response = structuredClone(window.__clientInfoResponse || {
                            nickname: "Alice", unique_id: "user-a", connected_at: Date.now() / 1000 - 120,
                            idle_seconds: 5, ping_ms: 12, ip: "127.0.0.1", port: 12333,
                            bytes_in: 1024, bytes_out: 2048,
                        });
                        const gate = window.__clientInfoGate;
                        window.__clientInfoGate = null;
                        if (gate) await gate;
                        return response;
                    }
                    if (method === "SetActiveTab") {
                        window.__tabs = window.__tabs.map((tab) => ({ ...tab, active: tab.id === args[0] }));
                        window.__activeClient = args[0] === "tab-b" ? "client-b" : "client-a";
                        for (const cb of window.__events.tab_update || []) cb(structuredClone(window.__tabs));
                        for (const cb of window.__events.tab_reset || []) cb(args[0]);
                        return "";
                    }
                    return "";
                };
            },
        });
        window.go = { main: { App: app } };
        window.__mediaDevices = [
            { kind: "audioinput", deviceId: "mic-built-in", label: "Built-in Mic" },
            { kind: "audioinput", deviceId: "mic-usb", label: "USB Mic" },
            { kind: "audiooutput", deviceId: "speaker-usb", label: "USB Speakers" },
        ];
        Object.defineProperty(navigator, "mediaDevices", { configurable: true, value: {
            enumerateDevices: async () => structuredClone(window.__mediaDevices),
            getUserMedia: async () => { throw new Error("not needed by this workflow"); },
        }});
    }, { initialSettings: settings });
    await page.goto("/");
    await page.waitForFunction(() => !!window.__noxa?.openSettings);
});

test("session snapshot rejects native activation before frontend reset", async ({ page }) => {
    const result = await page.evaluate(async () => {
        const original = window.go.main.App;
        let nativeTab = "a";
        let release;
        const gate = new Promise((resolve) => { release = resolve; });
        const calls = [];
        window.go.main.App = new Proxy(original, { get(target, method) {
            if (method === "SessionInfoForTab") return async (tabID) => {
                calls.push([method, tabID]);
                await gate;
                if (tabID !== nativeTab) throw new Error("server tab changed");
                return { client_id: "a-client", is_admin: false, is_guest: true, connected: true };
            };
            if (method === "ClientID") return async () => { await gate; return nativeTab + "-client"; };
            if (method === "IsAdmin") return async () => nativeTab === "b";
            if (method === "IsGuest") return async () => nativeTab !== "b";
            return target[method];
        } });
        for (const cb of window.__events.tab_reset) cb("a");
        nativeTab = "b";
        release();
        await new Promise((resolve) => setTimeout(resolve, 50));
        const { myClientID, isGuest } = window.__noxa.state;
        return { myClientID, isGuest, calls };
    });
    expect(result).toEqual({ myClientID: "", isGuest: true, calls: [["SessionInfoForTab", "a"]] });
});

for (const flow of ["login", "reconnect"]) test(`session snapshot keeps an offline ${flow} offline`, async ({ page }) => {
    await page.evaluate(async (flow) => {
        const original = window.go.main.App;
        window.__offlineSnapshotCalls = 0;
        window.go.main.App = new Proxy(original, { get(target, method) {
            if (method === "SessionInfoForTab") return async () => {
                window.__offlineSnapshotCalls++;
                return { client_id: "", is_admin: false, is_guest: true, connected: false, security: "offline" };
            };
            return target[method];
        } });
        if (flow === "login") {
            document.getElementById("login-addr").value = "closed.example:12333";
            document.getElementById("login-nick").value = "Alice";
            await window.__noxa.connectFromLogin();
        } else {
            window.__noxa.state.settings.reconnect_on_loss = false;
            window.__noxa.state.lastSuccessfulConnect = {
                addr: "closed.example:12333", nick: "Alice", pw: "", spw: "", bookmark: "",
            };
            for (const callback of window.__events.tray_reconnect || []) callback();
        }
    }, flow);
    await expect.poll(() => page.evaluate(() => window.__offlineSnapshotCalls)).toBe(1);
    await expect(page.locator("#conn-pill")).not.toHaveClass(/\bup\b/);
    expect(await page.evaluate(() => window.__calls.MOTDForTab || 0)).toBe(0);
});

test("session snapshot leaves one retry scheduled after a disconnect during recovery", async ({ page }) => {
    await page.clock.install();
    await page.evaluate(() => {
        const original = window.go.main.App;
        window.__sessionSnapshotPending = false;
        const gate = new Promise((resolve) => { window.__releaseSessionSnapshot = resolve; });
        window.go.main.App = new Proxy(original, { get(target, method) {
            if (method === "SessionInfoForTab") return async () => {
                window.__sessionSnapshotPending = true;
                await gate;
                return { client_id: "closed-client", is_admin: true, is_guest: false, connected: true, security: "TLS" };
            };
            return target[method];
        } });
        const state = window.__noxa.state;
        state.settings.reconnect_on_loss = true;
        state.lastConnect = { addr: "closed.example:12333", nick: "Alice", pw: "", spw: "", bookmark: "" };
        for (const callback of window.__events.disconnected) callback();
    });
    await page.clock.runFor(5000);
    await expect.poll(() => page.evaluate(() => window.__sessionSnapshotPending)).toBe(true);
    await page.evaluate(() => {
        for (const callback of window.__events.disconnected) callback();
        window.__releaseSessionSnapshot();
    });
    await page.clock.runFor(100);
    expect(await page.evaluate(() => window.__noxa.state.reconnectAttempts)).toBe(2);
    await page.clock.runFor(4900);
    expect(await page.evaluate(() => window.__calls.ConnectBookmarkTabWithID)).toBe(2);
});

test("session snapshot preserves the retry limit after repeated offline reconnects", async ({ page }) => {
    await page.clock.install();
    await page.evaluate(() => {
        const original = window.go.main.App;
        window.go.main.App = new Proxy(original, { get(target, method) {
            if (method === "SessionInfoForTab") return async () => ({
                client_id: "", is_admin: false, is_guest: true, connected: false, security: "offline",
            });
            return target[method];
        } });
        const state = window.__noxa.state;
        state.settings.reconnect_on_loss = true;
        state.lastConnect = { addr: "closed.example:12333", nick: "Alice", pw: "", spw: "", bookmark: "" };
        for (const callback of window.__events.disconnected) callback();
    });
    for (let attempt = 1; attempt <= 5; attempt++) {
        await page.clock.runFor(5000);
        await expect.poll(() => page.evaluate(() => window.__calls.ConnectBookmarkTabWithID || 0)).toBe(attempt);
    }
    await page.clock.runFor(10000);
    expect(await page.evaluate(() => window.__calls.ConnectBookmarkTabWithID)).toBe(5);
    expect(await page.evaluate(() => window.__noxa.state.reconnectAttempts)).toBe(5);
    await expect(page.locator("#conn-pill")).not.toHaveClass(/\bup\b/);
});

for (const flow of ["tab", "login", "reconnect"]) test(`session snapshot cannot revive a disconnected ${flow}`, async ({ page }) => {
    await page.evaluate((flow) => {
        const original = window.go.main.App;
        window.__sessionSnapshotPending = false;
        window.__noxa.state.settings.reconnect_on_loss = false;
        const gate = new Promise((resolve) => { window.__releaseSessionSnapshot = resolve; });
        window.go.main.App = new Proxy(original, { get(target, method) {
            if (method === "SessionInfoForTab") return async () => {
                window.__sessionSnapshotPending = true;
                await gate;
                return { client_id: "obsolete-client", is_admin: true, is_guest: false, connected: true, security: "TLS" };
            };
            return target[method];
        } });
        if (flow === "tab") {
            for (const cb of window.__events.tab_reset) cb("a");
        } else if (flow === "login") {
            document.getElementById("login-addr").value = "closed.example:12333";
            document.getElementById("login-nick").value = "Alice";
            void window.__noxa.connectFromLogin();
        } else {
            window.__noxa.state.lastSuccessfulConnect = {
                addr: "closed.example:12333", nick: "Alice", pw: "", spw: "", bookmark: "",
            };
            for (const callback of window.__events.tray_reconnect || []) callback();
        }
    }, flow);
    await expect.poll(() => page.evaluate(() => window.__sessionSnapshotPending)).toBe(true);
    const result = await page.evaluate(async () => {
        for (const callback of window.__events.disconnected) callback();
        window.__releaseSessionSnapshot();
        await new Promise((resolve) => setTimeout(resolve, 50));
        const { myClientID, isGuest } = window.__noxa.state;
        return { myClientID, isGuest };
    });
    expect(result).toEqual({ myClientID: "", isGuest: true });
    await expect(page.locator("#conn-pill")).not.toHaveClass(/\bup\b/);
});

test("session snapshot ignores an earlier activation of the same tab", async ({ page }) => {
    const result = await page.evaluate(async () => {
        const original = window.go.main.App;
        let release;
        let first = true;
        const gate = new Promise((resolve) => { release = resolve; });
        window.go.main.App = new Proxy(original, { get(target, method) {
            if (method === "SessionInfoForTab") return async () => {
                if (first) {
                    first = false;
                    await gate;
                    return { client_id: "obsolete-client", is_admin: true, is_guest: false, connected: true, authorization_model: "roles-v1" };
                }
                return { client_id: "current-client", is_admin: false, is_guest: true, connected: true };
            };
            if (method === "ClientID") return async () => {
                if (first) { first = false; await gate; return "obsolete-client"; }
                return "current-client";
            };
            return target[method];
        } });
        for (const tab of ["a", "b", "a"]) for (const cb of window.__events.tab_reset) cb(tab);
        await new Promise((resolve) => setTimeout(resolve, 30));
        release();
        await new Promise((resolve) => setTimeout(resolve, 30));
        const { myClientID, isGuest, authorizationModel } = window.__noxa.state;
        return { myClientID, isGuest, authorizationModel };
    });
    expect(result).toEqual({ myClientID: "current-client", isGuest: true, authorizationModel: "" });
});

async function auditAccessibility(page, context) {
    const snapshot = await page.locator("body").ariaSnapshot();
    const issues = await page.evaluate(() => {
        const found = [];
        const ids = new Map();
        for (const element of document.querySelectorAll("[id]")) {
            const id = element.id;
            if (!id) continue;
            ids.set(id, (ids.get(id) || 0) + 1);
        }
        for (const [id, count] of ids) {
            if (count > 1) found.push(`duplicate id #${id} (${count} instances)`);
        }

        for (const attribute of ["aria-labelledby", "aria-describedby", "aria-controls"]) {
            for (const element of document.querySelectorAll(`[${attribute}]`)) {
                for (const id of element.getAttribute(attribute).trim().split(/\s+/)) {
                    if (id && !document.getElementById(id)) {
                        found.push(`${element.tagName.toLowerCase()}[${attribute}] references missing #${id}`);
                    }
                }
            }
        }
        for (const label of document.querySelectorAll("label[for]")) {
            const id = label.getAttribute("for");
            if (id && !document.getElementById(id)) found.push(`label[for] references missing #${id}`);
        }
        for (const image of document.querySelectorAll("img:not([alt])")) {
            found.push(`image is missing alt text${image.id ? ` (#${image.id})` : ""}`);
        }
        return found;
    });

    const namedRoles = new Set([
        "button", "checkbox", "combobox", "dialog", "link", "menuitem", "menuitemcheckbox",
        "menuitemradio", "radio", "searchbox", "slider", "spinbutton", "switch", "tab", "textbox",
    ]);
    for (const line of snapshot.split("\n")) {
        const match = line.match(/^\s*-\s+([a-z]+)\b(.*)$/);
        if (!match || !namedRoles.has(match[1])) continue;
        const name = match[2].match(/"((?:[^"\\]|\\.)*)"/);
        if (!name || !name[1].trim()) issues.push(`unnamed ${match[1]} in accessibility tree: ${line.trim()}`);
    }

    expect(issues, `${context} accessibility issues\n\n${snapshot}`).toEqual([]);
}

test("switches the capture device and persists it", async ({ page }) => {
    await page.evaluate(() => window.__noxa.openSettings("capture"));
    const select = page.locator("#settings-content .set-row").filter({ hasText: "Capture device" }).locator("select");
    await expect(select).toHaveValue("");
    await select.selectOption("mic-usb");
    await page.getByRole("button", { name: "Apply", exact: true }).click();
    await expect.poll(() => page.evaluate(() => window.__savedSettings?.capture_device_id)).toBe("mic-usb");
});

test("refreshes capture and playback device lists on demand", async ({ page }) => {
    await page.evaluate(() => window.__noxa.openSettings("capture"));
    const captureRow = page.locator("#settings-content .set-row").filter({ hasText: "Capture device" });
    const captureSelect = captureRow.locator("select");
    await expect(captureSelect).toHaveAccessibleName("Capture device");
    await expect(captureSelect.getByRole("option", { name: "USB Mic" })).toHaveCount(1);
    await captureSelect.selectOption("mic-usb");

    await page.evaluate(() => window.__mediaDevices.push({
        kind: "audioinput", deviceId: "mic-studio", label: "Studio Mic",
    }));
    await expect(captureSelect.getByRole("option", { name: "Studio Mic" })).toHaveCount(0);
    await captureRow.getByRole("button", { name: "Refresh capture devices" }).click();
    await expect(captureSelect).toHaveAccessibleName("Capture device");
    await expect(captureSelect.getByRole("option", { name: "Studio Mic" })).toHaveCount(1);
    await expect(captureSelect).toHaveValue("mic-usb");

    await page.getByRole("tab", { name: /Playback/ }).click();
    const playbackRow = page.locator("#settings-content .set-row").filter({ hasText: "Output device" });
    const playbackSelect = playbackRow.locator("select");
    await expect(playbackSelect).toHaveAccessibleName("Output device");
    await expect(playbackSelect.getByRole("option", { name: "USB Speakers" })).toHaveCount(1);

    await page.evaluate(() => window.__mediaDevices.push({
        kind: "audiooutput", deviceId: "speaker-bt", label: "Bluetooth Speakers",
    }));
    await expect(playbackSelect.getByRole("option", { name: "Bluetooth Speakers" })).toHaveCount(0);
    await playbackRow.getByRole("button", { name: "Refresh playback devices" }).click();
    await expect(playbackSelect).toHaveAccessibleName("Output device");
    await expect(playbackSelect.getByRole("option", { name: "Bluetooth Speakers" })).toHaveCount(1);
});

test("mute control switches to an unmute affordance and back", async ({ page }) => {
    await page.evaluate(() => window.__noxa.showWorkspace(false));
    const mute = page.getByRole("button", { name: "Mute microphone" });
    await expect(mute).toHaveText("Microphone on");
    await expect(mute).toHaveAttribute("title", "Mute");
    await expect(mute).toHaveAttribute("aria-pressed", "false");

    await mute.click();
    const unmute = page.getByRole("button", { name: "Unmute microphone" });
    await expect(unmute).toHaveText("Microphone muted");
    await expect(unmute).toHaveAttribute("title", "Unmute");
    await expect(unmute).toHaveAttribute("aria-pressed", "true");

    await unmute.click();
    await expect(page.getByRole("button", { name: "Mute microphone" })).toHaveText("Microphone on");
});

test("voice activation reopens after silence and keeps mute and PTT private", async ({ page }) => {
    // Use real browser tracks: disabling a track must silence its consumers.
    await page.locator("body").click({ position: { x: 1, y: 1 } });
    await page.evaluate(async () => {
        const v = window.__noxa;
        const ctx = new AudioContext();
        const oscillator = ctx.createOscillator();
        const gain = ctx.createGain();
        const output = ctx.createMediaStreamDestination();
        oscillator.connect(gain).connect(output);
        gain.gain.value = 0;
        oscillator.start();
        await ctx.resume();
        window.__voiceSignal = { ctx, gain };
        v.state.settings.activation_mode = "vad";
        v.state.settings.ptt_release_delay_ms = 0;
        v.state.settings.warn_muted_talking = false;
        v.state.localStream = output.stream;
        v.state.pttActive = false;
        v.state.muted = false;
        v.applyVoiceState();
        v.startVADMonitor();
        await v.state.voiceMonitorCtx.resume();
    });
    const transmitting = () => page.evaluate(() => window.__noxa.state.localStream.getAudioTracks()[0].enabled);
    expect(await transmitting()).toBe(false);
    for (let cycle = 0; cycle < 2; cycle++) {
        await page.evaluate(() => { window.__voiceSignal.gain.gain.value = 0.5; });
        await expect.poll(transmitting).toBe(true);
        await page.evaluate(() => { window.__voiceSignal.gain.gain.value = 0; });
        await expect.poll(transmitting).toBe(false);
    }
    await page.evaluate(() => {
        window.__noxa.state.muted = true;
        window.__voiceSignal.gain.gain.value = 0.5;
        window.__noxa.applyVoiceState();
    });
    await expect.poll(() => page.evaluate(() => window.__noxa.state.pttActive)).toBe(true);
    expect(await transmitting()).toBe(false);
    await page.evaluate(() => {
        const v = window.__noxa;
        v.state.settings.activation_mode = "ptt";
        v.state.muted = false;
        v.setPTT(false);
    });
    expect(await transmitting()).toBe(false);
    await page.evaluate(() => window.__noxa.setPTT(true));
    expect(await transmitting()).toBe(true);
    await page.evaluate(() => {
        window.__noxa.resetVoiceSession();
        return window.__voiceSignal.ctx.close();
    });
});

test("voice monitor releases its local capture on restart and disconnect", async ({ page }) => {
    const result = await page.evaluate(async () => {
        const v = window.__noxa;
        const inputCtx = new AudioContext();
        const track = inputCtx.createMediaStreamDestination().stream.getAudioTracks()[0];
        const clones = [];
        const clone = track.clone.bind(track);
        track.clone = () => {
            const copy = clone();
            clones.push(copy);
            return copy;
        };
        v.state.localStream = new MediaStream([track]);
        v.startVADMonitor();
        const firstCtx = v.state.voiceMonitorCtx;
        v.startVADMonitor();
        const afterRestart = clones.map((copy) => copy.readyState);
        const senderAfterRestart = track.readyState;
        v.resetVoiceSession();
        await inputCtx.close();
        return {
            afterRestart, senderAfterRestart,
            afterDisconnect: clones.map((copy) => copy.readyState),
            senderAfterDisconnect: track.readyState,
            firstContext: firstCtx.state,
            monitorContext: v.state.voiceMonitorCtx,
            timer: v.state.vadMonitor,
        };
    });
    expect(result).toEqual({
        afterRestart: ["ended", "live"], senderAfterRestart: "live",
        afterDisconnect: ["ended", "ended"], senderAfterDisconnect: "ended",
        firstContext: "closed", monitorContext: null, timer: null,
    });
});

test("retries a missing microphone without interrupting video or screen sharing", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        window.__getUserMediaCalls = 0;
        window.__cameraRequests = 0;
        window.__allowMicrophone = false;
        window.__shareTracksStopped = 0;

        const canvas = document.createElement("canvas");
        const videoStream = canvas.captureStream(1);
        window.__cameraTrack = videoStream.getVideoTracks()[0];
        const audioContext = new AudioContext();
        window.__retryAudioContext = audioContext;
        const audioStream = audioContext.createMediaStreamDestination().stream;
        navigator.mediaDevices.getUserMedia = async (constraints) => {
            window.__getUserMediaCalls++;
            if (constraints.video) window.__cameraRequests++;
            if (constraints.audio && !window.__allowMicrophone) {
                throw new DOMException("test permission denial", "NotAllowedError");
            }
            if (constraints.video) return videoStream;
            if (constraints.audio) return audioStream;
            return new MediaStream();
        };

        class FakePeerConnection {
            constructor() {
                this.senders = [];
                this.transceivers = [];
                this.iceConnectionState = "connected";
            }
            addTransceiver(track, options = {}) {
                const sender = {
                    track: typeof track === "string" ? null : track,
                    getParameters: () => ({ encodings: [{}] }),
                    setParameters: async () => {},
                    replaceTrack: async (nextTrack) => { sender.track = nextTrack; },
                };
                const transceiver = {
                    sender,
                    receiver: { track: { kind: typeof track === "string" ? track : track.kind } },
                    direction: options.direction || "sendrecv",
                    currentDirection: options.direction || "sendrecv",
                };
                this.senders.push(sender);
                this.transceivers.push(transceiver);
                return transceiver;
            }
            getSenders() { return this.senders; }
            getTransceivers() { return this.transceivers; }
            async createOffer() { return { type: "offer", sdp: "test-offer" }; }
            async setLocalDescription(description) { this.localDescription = description; }
            async setRemoteDescription(description) { this.remoteDescription = description; }
            async addIceCandidate() {}
            close() { this.iceConnectionState = "closed"; }
        }
        window.RTCPeerConnection = FakePeerConnection;
        window.__noxa.state.myClientID = "client-a";
        window.__noxa.state.myChannelID = 0;
        window.__noxa.state.channels = [{ ChannelID: 42, Name: "Lobby" }];
        window.__noxa.state.clients = [{
            client_id: "client-a", unique_id: "user-a", nickname: "Alice",
            channel_id: 0, is_speaking: false,
        }];
        const moved = JSON.stringify({ type: "user_moved", data: { client_id: "client-a", channel_id: 42 } });
        for (const callback of window.__events.event || []) callback(moved);
    });

    await expect(page.locator("#voice-status")).toHaveText("voice on");
    expect(await page.evaluate(() => window.__cameraRequests)).toBe(0);
    await expect(page.locator("#local-video")).toBeHidden();
    await page.getByRole("button", { name: "Camera off — click to turn on", exact: true }).click();
    await expect.poll(() => page.evaluate(() => window.__cameraRequests)).toBe(1);
    await expect(page.locator("#local-video")).toBeVisible();
    await expect(page.locator("#mic-status > span")).toHaveText("Microphone access denied — video only");
    const retry = page.getByRole("button", { name: "Retry microphone access" });
    await expect(retry).toBeVisible();

    await page.evaluate(() => {
        const state = window.__noxa.state;
        window.__pcBeforeMicRetry = state.pc;
        state.screenSharing = true;
        state.shareStream = {
            getTracks: () => [{ stop: () => { window.__shareTracksStopped++; } }],
        };
        window.__allowMicrophone = true;
    });

    await retry.click();

    await expect(page.locator("#mic-status")).toBeEmpty();
    await expect(page.locator("#ptt-btn")).toBeEnabled();
    await expect(page.locator("#ptt-btn")).toBeFocused();
    await expect.poll(() => page.evaluate(() => ({
        samePeerConnection: window.__noxa.state.pc === window.__pcBeforeMicRetry,
        audioTracks: window.__noxa.state.localStream.getAudioTracks().length,
        cameraLive: window.__cameraTrack.readyState === "live",
        sharing: window.__noxa.state.screenSharing,
        shareTracksStopped: window.__shareTracksStopped,
        unpublishedShare: window.__calls.SetScreenShareForTab || 0,
        slots: window.__callArgs.WebRTCOfferForTab.at(-1)[2].map(({ slot }) => slot),
    }))).toEqual({
        samePeerConnection: true,
        audioTracks: 1,
        cameraLive: true,
        sharing: true,
        shareTracksStopped: 0,
        unpublishedShare: 0,
        slots: ["cam", "mic"],
    });

    await page.evaluate(() => { window.__noxa.state.screenSharing = false; });
    await page.getByRole("button", { name: "Camera on — click to turn off", exact: true }).click();
    const stop = page.getByRole("button", { name: "Turn off", exact: true });
    if (await stop.isVisible()) await stop.click();
    await expect.poll(() => page.evaluate(() => window.__cameraTrack.readyState)).toBe("ended");
    await expect(page.locator("#local-video")).toBeHidden();
    expect(await page.evaluate(() => window.__noxa.state.localStream.getVideoTracks().length)).toBe(0);
});

test("camera stays off on joins and reconnects, and discards a late enable request", async ({ page }) => {
    await page.evaluate(async () => {
        const v = window.__noxa;
        v.showWorkspace(false);
        window.__cameraRequests = 0;
        window.__denyCamera = true;
        window.__cameraAudio = new AudioContext();
        navigator.mediaDevices.getUserMedia = async (constraints) => {
            if (constraints.video) {
                window.__cameraRequests++;
                if (window.__denyCamera) throw new DOMException("test denial", "NotAllowedError");
                const stream = document.createElement("canvas").captureStream(1);
                window.__enabledCameraTrack = stream.getVideoTracks()[0];
                if (window.__delayCamera) await new Promise((resolve) => { window.__resolveCameraEnable = resolve; });
                return stream;
            }
            return window.__cameraAudio.createMediaStreamDestination().stream;
        };
        // Negotiate with a real in-page peer; no camera or remote server is used.
        const app = window.go.main.App;
        window.__cameraRemote = new RTCPeerConnection();
        window.go.main.App = new Proxy(app, {
            get(target, key) {
                if (key !== "WebRTCOfferForTab") return target[key];
                return async (_tab, sdp) => {
                    await window.__cameraRemote.setRemoteDescription({ type: "offer", sdp });
                    const answer = await window.__cameraRemote.createAnswer();
                    await window.__cameraRemote.setLocalDescription(answer);
                    return answer.sdp;
                };
            },
        });
        v.state.myChannelID = 42;
        v.state.channels = [{ ChannelID: 42, Name: "Lobby" }];
        await v.ensureVoiceForChannel();
    });
    await expect(page.locator("#voice-status")).toHaveText("voice on");
    expect(await page.evaluate(() => window.__cameraRequests)).toBe(0);
    const enable = page.getByRole("button", { name: "Camera off — click to turn on", exact: true });
    await enable.click();
    await expect(enable).toBeEnabled();
    await expect(page.locator("#local-video")).toBeHidden();
    expect(await page.evaluate(() => window.__cameraRequests)).toBe(1);

    await page.evaluate(() => { window.__denyCamera = false; });
    await enable.click();
    await expect(page.locator("#local-video")).toBeVisible();
    await page.evaluate(async () => {
        const v = window.__noxa;
        v.resetVoiceSession();
        window.__cameraRemote.close();
        window.__cameraRemote = new RTCPeerConnection();
        await v.ensureVoiceForChannel();
    });
    await expect(page.locator("#voice-status")).toHaveText("voice on");
    await expect(page.locator("#local-video")).toBeHidden();
    expect(await page.evaluate(() => window.__enabledCameraTrack.readyState)).toBe("ended");
    expect(await page.evaluate(() => window.__cameraRequests)).toBe(2);

    await page.evaluate(() => { window.__delayCamera = true; });
    await enable.click();
    await expect.poll(() => page.evaluate(() => typeof window.__resolveCameraEnable)).toBe("function");
    await page.evaluate(() => {
        window.__noxa.resetVoiceSession();
        window.__resolveCameraEnable();
    });
    await expect.poll(() => page.evaluate(() => window.__enabledCameraTrack.readyState)).toBe("ended");
    await expect(page.locator("#local-video")).toBeHidden();
    expect(await page.evaluate(() => window.__noxa.state.localStream)).toBeNull();
});

test("disconnect immediately releases camera capture waiting for a negotiation", async ({ page }) => {
    await page.evaluate(async () => {
        const v = window.__noxa;
        v.state.myChannelID = 42;
        v.state.localStream = new MediaStream();
        v.state.pc = new RTCPeerConnection();
        const video = await import("/src/video.js");
        video.resetCameraState();
        navigator.mediaDevices.getUserMedia = async () => {
            const stream = document.createElement("canvas").captureStream(1);
            window.__queuedCamera = stream.getVideoTracks()[0];
            return stream;
        };
        video.queuePeerNegotiation(v.state.pc, () => new Promise(resolve => { window.__finishQueuedOffer = resolve; }));
        window.__queuedCameraStart = video.cameraToggle();
    });
    await expect.poll(() => page.evaluate(() => window.__queuedCamera?.readyState)).toBe("live");
    await page.evaluate(() => window.__noxa.resetVoiceSession());
    expect(await page.evaluate(() => window.__queuedCamera.readyState)).toBe("ended");
    await page.evaluate(async () => { window.__finishQueuedOffer(); await window.__queuedCameraStart; });
});

async function installMediaUpdateFixture(page) {
    await page.evaluate(() => {
        const v = window.__noxa;
        v.showWorkspace(false);
        window.__mediaUpdate = { reads: 0, offers: [], limits: { video_max_width: 0, video_max_height: 0, video_max_bitrate: 1000000 } };
        class LimitPeer {
            constructor() { this.transceivers = []; this.signalingState = "stable"; }
            getTransceivers() { return this.transceivers; }
            getSenders() { return this.transceivers.map(tr => tr.sender); }
            addTransceiver(track, options) {
                let params = { encodings: [{}] };
                const sender = { track: typeof track === "string" ? null : track, getParameters: () => structuredClone(params),
                    setParameters: async value => {
                        if (window.__mediaUpdate.failCaps) throw new Error("encoder failure");
                        params = structuredClone(value);
                    } };
                const tr = { sender, receiver: { track: { kind: typeof track === "string" ? track : track.kind } }, direction: options.direction };
                this.transceivers.push(tr);
                return tr;
            }
            async createOffer(options) { window.__mediaUpdate.offers.push(options); return { type: "offer", sdp: "offer" }; }
            async setLocalDescription() {}
            async setRemoteDescription() {}
            close() { this.closed = true; }
        }
        window.__limitPeer = LimitPeer;
        Object.assign(v.state, { activeTabID: "limit-tab", myClientID: "limit-self", myChannelID: 42,
            channels: [{ ChannelID: 42, Name: "Lobby" }], pc: new LimitPeer(), localStream: document.createElement("canvas").captureStream(1),
            mediaLimits: { video_max_width: 0, video_max_height: 0, video_max_bitrate: 0 } });
        window.__mediaUpdate.track = v.state.localStream.getVideoTracks()[0];
        v.state.pc.addTransceiver(window.__mediaUpdate.track, { direction: "sendrecv" });
        const app = window.go.main.App;
        window.go.main.App = new Proxy(app, { get(target, key) {
            if (key === "GetMediaLimitsForTab") return async tab => {
                if (tab !== "limit-tab") throw new Error("wrong tab");
                const fixture = window.__mediaUpdate;
                fixture.reads++;
                if (fixture.failRead) throw new Error("limits unavailable");
                if (fixture.holdRead) return await new Promise(resolve => { fixture.finishRead = resolve; });
                return { ...fixture.limits };
            };
            if (key === "WebRTCOfferForTab") return async tab => {
                if (tab !== "limit-tab") throw new Error("wrong offer tab");
                if (window.__mediaUpdate.failOffer) throw new Error("offer failure");
                if (window.__mediaUpdate.holdOffer) await new Promise(resolve => { window.__mediaUpdate.finishOffer = resolve; });
                return "answer";
            };
            return target[key];
        } });
        window.__emitLimits = () => { for (const cb of window.__events.media_limits_changed || []) cb(""); };
    });
}

test("live media invalidation applies bitrate and rebuilds only for dimension changes", async ({ page }) => {
    await installMediaUpdateFixture(page);
    await page.evaluate(() => window.__emitLimits());
    await expect.poll(() => page.evaluate(() => window.__noxa.state.pc.getSenders()[0].getParameters().encodings[0].maxBitrate)).toBe(850000);
    expect(await page.evaluate(() => window.__mediaUpdate.offers)).toEqual([]);
    await page.evaluate(() => { window.__mediaUpdate.limits.video_max_width = 320; window.__mediaUpdate.limits.video_max_height = 180; window.__emitLimits(); });
    await expect.poll(() => page.evaluate(() => window.__mediaUpdate.offers.length)).toBe(1);
    await page.evaluate(() => window.__emitLimits());
    await expect.poll(() => page.evaluate(() => window.__mediaUpdate.reads)).toBe(3);
    expect(await page.evaluate(() => ({ offers: window.__mediaUpdate.offers, capture: window.__mediaUpdate.track.readyState, enabled: window.__mediaUpdate.track.enabled })))
        .toEqual({ offers: [{ iceRestart: true }], capture: "live", enabled: true });
});

for (const failure of ["failRead", "failCaps", "failOffer"]) {
    test(`live media update releases its session after ${failure}`, async ({ page }) => {
        await installMediaUpdateFixture(page);
        const errors = [];
        page.on("pageerror", error => errors.push(error.message));
        await page.evaluate(failure => {
            window.__mediaUpdate[failure] = true;
            window.__mediaUpdate.limits = { video_max_width: 320, video_max_height: 180, video_max_bitrate: 1000000 };
            window.__emitLimits();
        }, failure);
        await expect(page.locator("#voice-status")).toHaveText("voice unavailable");
        expect(await page.evaluate(() => ({ state: window.__mediaUpdate.track.readyState, peer: window.__noxa.state.pc, limits: window.__noxa.state.mediaLimits })))
            .toEqual({ state: "ended", peer: null, limits: null });
        expect(errors).toEqual([]);
    });
}

for (const turns of [1, 3, 5, 7, 9, 11, 13]) {
    test(`live media invalidation arriving around completion is retained (${turns} microtasks)`, async ({ page }) => {
        await installMediaUpdateFixture(page);
        await page.evaluate(turns => {
            const sender = window.__noxa.state.pc.getSenders()[0];
            const apply = sender.setParameters;
            sender.setParameters = async parameters => {
                sender.setParameters = apply;
                await apply(parameters);
                const invalidate = remaining => queueMicrotask(() => {
                    if (remaining > 0) invalidate(remaining - 1);
                    else { window.__mediaUpdate.limits.video_max_bitrate = 500000; window.__emitLimits(); }
                });
                invalidate(turns);
            };
            window.__emitLimits();
        }, turns);
        await expect.poll(() => page.evaluate(() => window.__noxa.state.pc.getSenders()[0].getParameters().encodings[0].maxBitrate)).toBe(425000);
        expect(await page.evaluate(() => window.__mediaUpdate.reads)).toBe(2);
    });
}

test("live media invalidation discards an outdated bridge result before applying it", async ({ page }) => {
    await installMediaUpdateFixture(page);
    await page.evaluate(() => { window.__mediaUpdate.holdRead = true; window.__emitLimits(); });
    await expect.poll(() => page.evaluate(() => typeof window.__mediaUpdate.finishRead)).toBe("function");
    await page.evaluate(() => {
        window.__emitLimits();
        window.__mediaUpdate.holdRead = false;
        window.__mediaUpdate.finishRead({ video_max_width: 100, video_max_height: 50, video_max_bitrate: 100 });
    });
    await expect.poll(() => page.evaluate(() => window.__noxa.state.pc.getSenders()[0].getParameters().encodings[0].maxBitrate)).toBe(850000);
    expect(await page.evaluate(() => ({ reads: window.__mediaUpdate.reads, offers: window.__mediaUpdate.offers, state: window.__mediaUpdate.track.readyState })))
        .toEqual({ reads: 2, offers: [], state: "live" });
});

test("live media update timeout tears down its peer and late completion leaves a replacement alone", async ({ page }) => {
    await installMediaUpdateFixture(page);
    await page.clock.install();
    await page.evaluate(() => { window.__mediaUpdate.holdRead = true; window.__emitLimits(); });
    await expect.poll(() => page.evaluate(() => typeof window.__mediaUpdate.finishRead)).toBe("function");
    await page.clock.fastForward(10001);
    await expect(page.locator("#voice-status")).toHaveText("voice unavailable");
    expect(await page.evaluate(() => window.__mediaUpdate.track.readyState)).toBe("ended");
    await page.evaluate(() => {
        const state = window.__noxa.state;
        state.pc = new window.__limitPeer();
        state.localStream = document.createElement("canvas").captureStream(1);
        state.mediaLimits = { video_max_bitrate: 2000000 };
        window.__mediaUpdate.finishRead({ video_max_width: 100, video_max_height: 50 });
    });
    expect(await page.evaluate(() => ({ closed: !!window.__noxa.state.pc.closed, limits: window.__noxa.state.mediaLimits,
        state: window.__noxa.state.localStream.getTracks()[0].readyState })))
        .toEqual({ closed: false, limits: { video_max_bitrate: 2000000 }, state: "live" });
});

for (const stage of ["queue", "constraints", "offer"]) {
    test(`live media deadline covers ${stage} and ignores late work`, async ({ page }) => {
        await installMediaUpdateFixture(page);
        await page.clock.install();
        const errors = [];
        page.on("pageerror", error => errors.push(error.message));
        await page.evaluate(async stage => {
            const fixture = window.__mediaUpdate;
            fixture.limits = { video_max_width: 320, video_max_height: 180, video_max_bitrate: 1000000 };
            if (stage === "queue") {
                const video = await import("/src/video.js");
                video.queuePeerNegotiation(window.__noxa.state.pc, () => new Promise(resolve => { fixture.finishWork = resolve; }));
            } else if (stage === "constraints") {
                fixture.track.applyConstraints = () => new Promise(resolve => { fixture.finishWork = resolve; });
            } else fixture.holdOffer = true;
            window.__emitLimits();
        }, stage);
        await expect.poll(() => page.evaluate(stage => typeof window.__mediaUpdate[stage === "offer" ? "finishOffer" : "finishWork"], stage)).toBe("function");
        await page.clock.fastForward(10001);
        await expect(page.locator("#voice-status")).toHaveText("voice unavailable");
        expect(await page.evaluate(() => window.__mediaUpdate.track.readyState)).toBe("ended");
        await page.evaluate(stage => window.__mediaUpdate[stage === "offer" ? "finishOffer" : "finishWork"](), stage);
        await expect.poll(() => page.evaluate(() => window.__noxa.state.pc)).toBeNull();
        expect(errors).toEqual([]);
    });
}

test("live media update waits for startup's offer before rebuilding", async ({ page }) => {
    await installMediaUpdateFixture(page);
    await page.evaluate(() => {
        window.__noxa.resetVoiceSession();
        window.RTCPeerConnection = window.__limitPeer;
        navigator.mediaDevices.getUserMedia = async () => new MediaStream();
        window.__mediaUpdate.holdOffer = true;
        window.__mediaUpdate.start = window.__noxa.ensureVoiceForChannel();
    });
    await expect.poll(() => page.evaluate(() => typeof window.__mediaUpdate.finishOffer)).toBe("function");
    await page.evaluate(() => {
        window.__mediaUpdate.limits = { video_max_width: 320, video_max_height: 180, video_max_bitrate: 1000000 };
        window.__emitLimits();
    });
    expect(await page.evaluate(() => ({ reads: window.__mediaUpdate.reads, offers: window.__mediaUpdate.offers.length }))).toEqual({ reads: 1, offers: 1 });
    await page.evaluate(async () => {
        window.__mediaUpdate.holdOffer = false;
        window.__mediaUpdate.finishOffer();
        await window.__mediaUpdate.start;
    });
    await expect.poll(() => page.evaluate(() => window.__mediaUpdate.offers.length)).toBe(2);
    await expect(page.locator("#voice-status")).toHaveText("voice on");
});

test("live media timeout releases startup ownership before an old offer finishes", async ({ page }) => {
    await installMediaUpdateFixture(page);
    await page.clock.install();
    await page.evaluate(() => {
        window.__noxa.resetVoiceSession();
        window.RTCPeerConnection = window.__limitPeer;
        navigator.mediaDevices.getUserMedia = async () => new MediaStream();
        window.__mediaUpdate.holdOffer = true;
        window.__mediaUpdate.firstStart = window.__noxa.ensureVoiceForChannel();
    });
    await expect.poll(() => page.evaluate(() => typeof window.__mediaUpdate.finishOffer)).toBe("function");
    await page.evaluate(() => { window.__mediaUpdate.finishOldOffer = window.__mediaUpdate.finishOffer; window.__emitLimits(); });
    await page.clock.fastForward(10001);
    await expect(page.locator("#voice-status")).toHaveText("voice unavailable");
    await page.evaluate(() => { window.__mediaUpdate.nextStart = window.__noxa.ensureVoiceForChannel(); });
    await expect.poll(() => page.evaluate(() => window.__mediaUpdate.offers.length)).toBe(2);
    await page.evaluate(async () => {
        window.__mediaUpdate.finishOldOffer();
        await window.__mediaUpdate.firstStart;
    });
    await expect(page.locator("#voice-status")).toHaveText("voice connecting…");
    await page.evaluate(async () => { window.__mediaUpdate.finishOffer(); await window.__mediaUpdate.nextStart; });
    await expect(page.locator("#voice-status")).toHaveText("voice on");
    expect(await page.evaluate(() => window.__mediaUpdate.offers.length)).toBe(2);
});

test("voice startup limits deadline releases microphone capture before any peer exists", async ({ page }) => {
    await installMediaUpdateFixture(page);
    await page.clock.install();
    await page.evaluate(() => {
        window.__noxa.resetVoiceSession();
        window.__mediaUpdate.track = document.createElement("canvas").captureStream(1).getVideoTracks()[0];
        navigator.mediaDevices.getUserMedia = async () => new MediaStream([window.__mediaUpdate.track]);
        window.__mediaUpdate.holdRead = true;
        window.__mediaUpdate.start = window.__noxa.ensureVoiceForChannel();
    });
    await expect.poll(() => page.evaluate(() => typeof window.__mediaUpdate.finishRead)).toBe("function");
    await page.evaluate(() => window.__emitLimits());
    await page.clock.fastForward(10001);
    await expect(page.locator("#voice-status")).toHaveText("voice unavailable");
    expect(await page.evaluate(() => window.__mediaUpdate.track.readyState)).toBe("ended");
    await page.evaluate(async () => { window.__mediaUpdate.finishRead({ video_max_bitrate: 100 }); await window.__mediaUpdate.start; });
    expect(await page.evaluate(() => window.__noxa.state.pc)).toBeNull();
});

test("voice startup refetches limits invalidated while its initial snapshot is pending", async ({ page }) => {
    await installMediaUpdateFixture(page);
    await page.evaluate(() => {
        window.__noxa.resetVoiceSession();
        window.RTCPeerConnection = window.__limitPeer;
        navigator.mediaDevices.getUserMedia = async () => new MediaStream();
        window.__mediaUpdate.holdRead = true;
        window.__mediaUpdate.start = window.__noxa.ensureVoiceForChannel();
    });
    await expect.poll(() => page.evaluate(() => typeof window.__mediaUpdate.finishRead)).toBe("function");
    await page.evaluate(async () => {
        window.__emitLimits();
        window.__mediaUpdate.holdRead = false;
        window.__mediaUpdate.finishRead({ video_max_bitrate: 100 });
        await window.__mediaUpdate.start;
    });
    expect(await page.evaluate(() => ({ reads: window.__mediaUpdate.reads, limits: window.__noxa.state.mediaLimits })))
        .toEqual({ reads: 2, limits: { video_max_width: 0, video_max_height: 0, video_max_bitrate: 1000000 } });
});

test("late media limits cannot replace the next server's voice session", async ({ page }) => {
    await page.evaluate(() => {
        const v = window.__noxa;
        v.showWorkspace(false);
        v.state.myChannelID = 42;
        v.state.channels = [{ ChannelID: 42, Name: "Lobby" }];
        window.__limitsCapture = document.createElement("canvas").captureStream(1);
        navigator.mediaDevices.getUserMedia = async () => window.__limitsCapture;
        const app = window.go.main.App;
        window.go.main.App = new Proxy(app, { get(target, key) {
            if (key === "GetMediaLimitsForTab") return () => new Promise(resolve => { window.__finishLimits = resolve; });
            return target[key];
        } });
        window.__limitsStart = v.ensureVoiceForChannel();
    });
    await expect.poll(() => page.evaluate(() => typeof window.__finishLimits)).toBe("function");
    const result = await page.evaluate(async () => {
        const v = window.__noxa;
        v.resetVoiceSession();
        v.state.serverGeneration++;
        const nextStream = document.createElement("canvas").captureStream(1);
        const nextPeer = new RTCPeerConnection();
        const nextLimits = { video_max_bitrate: 2000000 };
        v.state.localStream = nextStream;
        v.state.pc = nextPeer;
        v.state.mediaLimits = nextLimits;
        window.__finishLimits({ video_max_bitrate: 1000000, video_max_width: 320, video_max_height: 180 });
        await window.__limitsStart;
        const result = { sameStream: v.state.localStream === nextStream, live: nextStream.getTracks()[0].readyState,
            samePeer: v.state.pc === nextPeer, limits: v.state.mediaLimits, oldCapture: window.__limitsCapture.getTracks()[0].readyState };
        v.resetVoiceSession();
        return result;
    });
    expect(result).toEqual({ sameStream: true, live: "live", samePeer: true, limits: { video_max_bitrate: 2000000 }, oldCapture: "ended" });
});

test("media-limit bridge failure releases capture and exits connecting state", async ({ page }) => {
    await page.evaluate(async () => {
        const v = window.__noxa;
        v.showWorkspace(false);
        v.state.myChannelID = 42;
        v.state.channels = [{ ChannelID: 42, Name: "Lobby" }];
        window.__limitsCapture = document.createElement("canvas").captureStream(1);
        navigator.mediaDevices.getUserMedia = async () => window.__limitsCapture;
        const app = window.go.main.App;
        window.go.main.App = new Proxy(app, { get(target, key) {
            if (key === "GetMediaLimitsForTab") return async () => { throw new Error("bridge unavailable"); };
            return target[key];
        } });
        await v.ensureVoiceForChannel();
    });
    await expect(page.locator("#voice-status")).toHaveText("voice unavailable");
    expect(await page.evaluate(() => window.__limitsCapture.getTracks()[0].readyState)).toBe("ended");
    expect(await page.evaluate(() => window.__noxa.state.mediaLimits)).toBeNull();
});

test("camera settings preview requires an explicit test and releases capture on exit", async ({ page }) => {
    await page.evaluate(() => {
        window.__cameraRequests = 0;
        navigator.mediaDevices.getUserMedia = async (constraints) => {
            if (!constraints.video || constraints.audio) throw new Error("expected camera-only test");
            window.__cameraRequests++;
            const stream = document.createElement("canvas").captureStream(1);
            window.__previewTrack = stream.getVideoTracks()[0];
            return stream;
        };
        window.__noxa.openSettings("capture");
    });
    expect(await page.evaluate(() => window.__cameraRequests)).toBe(0);
    const start = page.getByRole("button", { name: "Test camera", exact: true });
    await start.click();
    await expect(page.getByLabel("Camera test preview")).toBeVisible();
    await page.getByRole("button", { name: "Stop camera test", exact: true }).click();
    expect(await page.evaluate(() => window.__previewTrack.readyState)).toBe("ended");
    await start.click();
    await page.locator('[data-page="playback"]').click();
    expect(await page.evaluate(() => window.__previewTrack.readyState)).toBe("ended");
    await page.locator('[data-page="capture"]').click();
    await start.click();
    await page.getByRole("button", { name: "Cancel", exact: true }).click();
    expect(await page.evaluate(() => window.__previewTrack.readyState)).toBe("ended");
    expect(await page.evaluate(() => window.__noxa.state.localStream)).toBeNull();
});

test("a camera test resolved after settings closes is immediately stopped", async ({ page }) => {
    await page.evaluate(() => {
        navigator.mediaDevices.getUserMedia = () => new Promise((resolve) => { window.__resolveCamera = resolve; });
        window.__noxa.openSettings("capture");
    });
    await page.getByRole("button", { name: "Test camera", exact: true }).click();
    await page.getByRole("button", { name: "Cancel", exact: true }).click();
    await page.evaluate(() => {
        const stream = document.createElement("canvas").captureStream(1);
        window.__previewTrack = stream.getVideoTracks()[0];
        window.__resolveCamera(stream);
    });
    await expect.poll(() => page.evaluate(() => window.__previewTrack.readyState)).toBe("ended");
});

test("ignores a delayed microphone failure after the voice session changes", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        const stream = document.createElement("canvas").captureStream(1);
        window.__noxa.state.myChannelID = 42;
        window.__noxa.state.localStream = stream;
        window.__noxa.state.pc = {
            close() {},
            getSenders: () => [],
            getTransceivers: () => [],
        };
        navigator.mediaDevices.getUserMedia = () => new Promise((_resolve, reject) => {
            window.__rejectStaleMicRetry = reject;
        });
        window.__staleMicRetry = window.__noxa.retryMicrophoneAccess();
    });
    await expect.poll(() => page.evaluate(() => typeof window.__rejectStaleMicRetry)).toBe("function");

    await page.evaluate(async () => {
        window.__noxa.resetVoiceSession();
        window.__rejectStaleMicRetry(new DOMException("late denial", "NotAllowedError"));
        await window.__staleMicRetry;
    });

    await expect(page.locator("#mic-status")).toBeEmpty();
    expect(await page.evaluate(() => window.__noxa.state.micState)).toBe("unknown");
});

test("routes decrypted direct messages and echoes without mixing global chat or peers", async ({ page }) => {
    await page.evaluate(() => {
        const { state } = window.__noxa;
        window.__dmStorageIdentity = "alpha";
        state.myUniqueID = "alpha";
        state.myNickname = "ALPHA";
        window.__noxa.showWorkspace();
        for (const callback of window.__events.event || []) callback(JSON.stringify({
            type: "chat", data: { direct: true, to_unique_id: "alpha", from_unique_id: "bravo", from: "BRAVO",
                text: "private Grüße 🌿", enc_verified: true, client_msg_id: "received-dm" },
        }));
    });
    await expect(page.locator("#chat-log")).not.toContainText("private Grüße");
    const bravoTab = page.locator("#pm-tabs .pm-tab").filter({ hasText: "BRAVO" });
    await expect(bravoTab).toBeVisible();
    await bravoTab.click();
    await expect(page.locator("#chat-log")).toContainText("private Grüße 🌿");
    await expect(page.locator("#chat-log .msg-tag")).toHaveText("Direct message");
    await expect(page.locator("#chat-log .msg-lock")).toHaveAttribute("title", /end-to-end encrypted/);
    await expect(page.locator("#chat-log .msg-lock")).toHaveAttribute("aria-label", /end-to-end encrypted/);

    await page.evaluate(() => {
        window.__noxaChat.openPM("charlie", "CHARLIE");
        for (const callback of window.__events.event || []) callback(JSON.stringify({
            type: "chat", data: { direct: true, to_unique_id: "bravo", from_unique_id: "alpha", from: "ALPHA",
                text: "echo for Bravo", enc_verified: true, client_msg_id: "sent-dm" },
        }));
    });
    await expect(page.locator("#chat-log")).not.toContainText("echo for Bravo");
    await bravoTab.click();
    await expect(page.locator("#chat-log")).toContainText("echo for Bravo");
    await page.evaluate(() => {
        for (const callback of window.__events.event || []) callback(JSON.stringify({
            type: "chat", data: { direct: true, from_unique_id: "bravo", from: "BRAVO",
                text: "[encrypted message — key unavailable]", client_msg_id: "unopened-dm" },
        }));
    });
    await expect(page.locator("#chat-log .missing-key .msg-lock svg")).toHaveCount(1);
    const records = await page.evaluate(() => window.__callArgs.DMHistoryAppend.map((args) => args[2]));
    expect(records.find((record) => record.client_msg_id === "received-dm").enc_verified).toBe(true);
    expect(records.find((record) => record.client_msg_id === "unopened-dm").enc_verified).toBe(false);
});

test("restores DM history without claiming legacy or plaintext records were verified", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace();
        window.__dmHistory = { bravo: [
            { body: "legacy record", from_nickname: "BRAVO", sent_at: 10 },
            { body: "plaintext record", from_nickname: "BRAVO", sent_at: 11, enc_verified: false },
            { body: "verified record", from_nickname: "BRAVO", sent_at: 12, enc_verified: true },
        ] };
        window.__noxaChat.openPM("bravo", "BRAVO");
    });
    await expect(page.locator("#chat-log")).toContainText("verified record");
    await expect(page.locator("#chat-log .msg-tag")).toHaveText(["Direct message", "Direct message", "Direct message"]);
    await expect(page.locator("#chat-log .msg-lock")).toHaveCount(1);
    await expect(page.locator("#chat-log .msg-lock")).toHaveAttribute("title", /end-to-end encrypted/);
    await expect(page.locator("#chat-log .msg-lock")).toHaveAttribute("aria-label", /end-to-end encrypted/);
});

test("tracks existing unassigned clients when they later join a channel", async ({ page }) => {
    await page.evaluate(() => {
        const { state } = window.__noxa;
        state.myClientID = "alpha";
        window.__noxa.showWorkspace();
        for (const callback of window.__events.snapshot || []) callback(JSON.stringify({
            root_channels: [{ ChannelID: 1, ParentID: 0, Name: "Echo Test", clients: [] }],
            unassigned_clients: [
                { client_id: "alpha", unique_id: "uid-alpha", nickname: "ALPHA", channel_id: 0, is_bot: true, status: "away" },
                { client_id: "bravo", unique_id: "uid-bravo", nickname: "BRAVO", channel_id: 0 },
            ],
        }));
        // Auth's join follows its snapshot; it must not duplicate self or
        // overwrite the richer snapshot metadata with partial event fields.
        for (const callback of window.__events.event || []) callback(JSON.stringify({
            type: "user_joined", data: { client_id: "alpha", unique_id: "uid-alpha", nickname: "ALPHA" },
        }));
        for (const clientID of ["alpha", "bravo"]) {
            for (const callback of window.__events.event || []) callback(JSON.stringify({
                type: "user_moved", data: { client_id: clientID, channel_id: 1 },
            }));
        }
    });
    await expect(page.locator('.channel[data-chid="1"] .ch-count')).toHaveText("2");
    await expect(page.locator('.client[data-clid="bravo"]')).toContainText("BRAVO");
    expect(await page.evaluate(() => window.__noxa.state.clients.map((client) => ({
        id: client.client_id, channel: client.channel_id, bot: !!client.is_bot, status: client.status || "",
    })))).toEqual([
        { id: "alpha", channel: 1, bot: true, status: "away" },
        { id: "bravo", channel: 1, bot: false, status: "" },
    ]);
});

test("restores recent servers when persisted startup settings resolve", async ({ page }) => {
    await page.evaluate((initialSettings) => {
        sessionStorage.setItem("startup-settings", JSON.stringify({
            ...initialSettings,
            recents: [{ addr: "127.0.0.1:12583", nickname: "ALPHA", last_used: 1789289089 }],
        }));
    }, settings);
    await page.reload();
    await page.waitForFunction(() => window.__noxaTabs && window.__resolveStartupSettings);
    await expect(page.locator("#login-recents .recent-row")).toHaveCount(0);
    await page.evaluate(() => window.__resolveStartupSettings());

    const recent = page.getByRole("button", { name: "ALPHA @ 127.0.0.1:12583", exact: true });
    await expect(recent).toBeVisible();
    await recent.click();
    await expect(page.locator("#login-addr")).toHaveValue("127.0.0.1:12583");
    await expect(page.locator("#login-nick")).toHaveValue("ALPHA");
});

test("edits a recent server in the login form and focuses its address", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.state.settings.recents = [{
            addr: "voice.example:12333",
            nickname: "Alice",
            last_used: 123,
        }];
        window.__noxaTabs.renderRecents();
    });

    const row = page.locator("#login-recents .recent-row");
    const label = row.locator(".recent-label");
    await expect(label).toHaveText("Alice @ voice.example:12333");
    await label.click();
    await expect(page.locator("#login-addr")).toHaveValue("voice.example:12333");
    await expect(page.locator("#login-nick")).toHaveValue("Alice");

    const edit = page.getByRole("button", {
        name: "Edit recent server Alice at voice.example:12333",
    });
    await expect(edit).toContainText("Edit");
    await page.locator("#login-addr").fill("wrong.example:12333");
    await page.locator("#login-nick").fill("Wrong nickname");
    await edit.click();

    await expect(page.locator("#login-addr")).toHaveValue("voice.example:12333");
    await expect(page.locator("#login-nick")).toHaveValue("Alice");
    await expect(page.locator("#login-addr")).toBeFocused();
    await expect.poll(() => page.locator("#login-addr").evaluate((input) => ({
        start: input.selectionStart,
        end: input.selectionEnd,
    }))).toEqual({ start: 0, end: "voice.example:12333".length });
});

test("shows connection-quality sample age and clears stale RTT on disconnect", async ({ page }) => {
    await page.evaluate(() => {
        const { state } = window.__noxa;
        state.myClientID = "client-a";
        const pill = document.getElementById("conn-pill");
        pill.textContent = "voice.example:12333";
        pill.classList.add("up");
        window.__noxa.showWorkspace(false);
        window.__noxa.startQualitySampler();
    });

    const pill = page.locator("#conn-pill");
    await expect(pill).toHaveAttribute("data-quality", "good");
    await expect(page.locator("#voice-latency")).toBeVisible();
    await expect(page.locator("#voice-latency")).toHaveText("12 ms");
    await expect(page.locator("#voice-latency")).toHaveAttribute("aria-label", "Server latency: 12 milliseconds, good");
    await expect(pill).toHaveAttribute(
        "title",
        /connection quality: good \(RTT 12 ms, sampled (?:just now|\d+ seconds? ago)\)/,
    );

    await page.evaluate(() => {
        window.__noxa.state.settings.reconnect_on_loss = false;
        for (const callback of window.__events.disconnected || []) callback();
    });
    await expect(pill).not.toHaveAttribute("data-quality", /.+/);
    await expect(pill).toHaveAttribute("title", "Offline — no current RTT sample");
    await expect(page.locator("#voice-latency")).toBeHidden();
});

test("latency reuses the five-second sampler, marks stale samples, and prevents overlapping requests", async ({ page }, testInfo) => {
    await page.clock.install();
    await page.evaluate(() => {
        const v = window.__noxa;
        v.state.myClientID = "client-a";
        v.showWorkspace(false);
        const app = window.go.main.App;
        window.__latencyCalls = 0;
        window.__latencyPending = false;
        window.go.main.App = new Proxy(app, {
            get(target, method) {
                if (method === "GetClientInfoForTab") return async () => {
                    window.__latencyCalls++;
                    if (window.__latencyPending) return await new Promise((resolve) => { window.__finishLatency = resolve; });
                    return { ping_ms: 12 };
                };
                return target[method];
            },
        });
        v.startQualitySampler();
    });
    const latency = page.locator("#voice-latency");
    await expect(latency).toHaveText("12 ms");
    await page.evaluate(() => {
        window.__latencyMutations = 0;
        new MutationObserver((records) => { window.__latencyMutations += records.length; })
            .observe(document.getElementById("voice-latency"), { childList: true, attributes: true, subtree: true });
    });
    await page.clock.fastForward(4000);
    expect(await page.evaluate(() => window.__latencyCalls)).toBe(1);
    expect(await page.evaluate(() => window.__latencyMutations)).toBe(0);
    await page.evaluate(() => { window.__latencyPending = true; });
    await page.clock.fastForward(1000);
    expect(await page.evaluate(() => window.__latencyCalls)).toBe(2);
    await page.clock.fastForward(20000);
    expect(await page.evaluate(() => window.__latencyCalls)).toBe(2);
    await expect(latency).toHaveText("12 ms · stale");
    await page.evaluate(() => window.__finishLatency({ ping_ms: 275 }));
    await expect(latency).toHaveText("275 ms");
    await expect(latency).toHaveAttribute("data-quality", "poor");
    await page.setViewportSize({ width: 1000, height: 730 });
    await page.locator("#voice-bar").screenshot({ path: testInfo.outputPath("latency-desktop.png") });
    await page.setViewportSize({ width: 640, height: 480 });
    await expect(latency).toBeVisible();
    await page.locator("#voice-bar").screenshot({ path: testInfo.outputPath("latency-small.png") });
    await page.evaluate(() => window.__noxa.stopQualitySampler());
    await expect(latency).toBeHidden();
});

test("clears RTT while switching tabs and only samples a connected active tab", async ({ page }) => {
    await page.evaluate(() => {
        const { state } = window.__noxa;
        state.myClientID = "client-a";
        const pill = document.getElementById("conn-pill");
        pill.textContent = "first.example:12333";
        pill.classList.add("up");
        window.__noxa.showWorkspace(false);
        window.__noxa.startQualitySampler();
    });
    const pill = page.locator("#conn-pill");
    await expect(pill).toHaveAttribute("data-quality", "good");

    await page.evaluate(() => {
        window.__tabs = [{
            id: "offline-tab", addr: "offline.example:12333", nickname: "Alice",
            connected: false, active: true, unread: 0, mentions: 0,
        }];
        for (const callback of window.__events.tab_reset || []) callback("offline-tab");
    });

    await expect(pill).not.toHaveAttribute("data-quality", /.+/);
    await expect(pill).not.toHaveClass(/\bup\b/);
    await expect(pill).toHaveAttribute("title", "Offline — no current RTT sample");
});

test("warns after connect when the local clock is outside certificate validity", async ({ page }) => {
    await page.evaluate(() => {
        window.__certificateClockWarning = "Local clock may be inaccurate; check date, time, and time zone.";
    });
    await page.locator("#login-addr").fill("voice.example:12333");
    await page.locator("#login-nick").fill("Alice");
    await page.getByRole("button", { name: "Connect" }).click();

    await expect(page.locator("#toasts")).toContainText(
        "Local clock may be inaccurate; check date, time, and time zone.",
    );
    await expect.poll(() => page.evaluate(() => window.__calls.CertificateClockWarning || 0)).toBe(1);
});

test("login submits an account password separately and clears the input after success", async ({ page }) => {
    await page.locator("#login-nick").fill("registered-member");
    await page.locator("#login-accountpw").fill("account-test-password");
    await page.locator("#login-serverpw").fill("server-test-password");
    await page.locator("#login-connect").click();
    await expect(page.locator("#login-overlay")).toBeHidden();
    expect(await page.evaluate(() => window.__callArgs.ConnectBookmarkTabWithID[0].slice(2)))
        .toEqual(["registered-member", "account-test-password", "server-test-password"]);
    await expect(page.locator("#login-accountpw")).toHaveValue("");
    expect(await page.evaluate(() => JSON.stringify(localStorage))).not.toContain("account-test-password");
});

test("does not paint a completed login over a tab selected during finalization", async ({ page }) => {
    await page.evaluate(() => {
        window.__connectTabID = "new-tab";
        window.__tabs = [
            { id: "new-tab", addr: "new.example:12333", nickname: "Alice", active: true, connected: true },
            { id: "other-tab", addr: "other.example:12333", nickname: "Bob", active: false, connected: true },
        ];
        window.__noxa.state.tabConnects.set("other-tab", {
            addr: "other.example:12333", nick: "Bob", pw: "", spw: "", bookmark: "",
        });
        window.__clientIDGate = new Promise((resolve) => {
            window.__releaseConnectIdentity = resolve;
        });
    });
    await page.locator("#login-addr").fill("new.example:12333");
    await page.locator("#login-nick").fill("Alice");
    await page.locator("#login-serverpw").fill("secret");
    await page.getByRole("button", { name: "Connect" }).click();
    await expect.poll(() => page.evaluate(() => window.__calls.SessionInfoForTab || 0)).toBeGreaterThan(0);

    await page.evaluate(() => {
        window.__tabs = window.__tabs.map((tab) => ({
            ...tab,
            active: tab.id === "other-tab",
        }));
        window.__activeClient = "client-b";
        for (const callback of window.__events.tab_reset || []) callback("other-tab");
        window.__releaseConnectIdentity();
    });

    await expect.poll(() => page.evaluate(() => window.__noxa.state.lastConnect?.addr))
        .toBe("other.example:12333");
    await expect(page.locator("#conn-pill")).not.toHaveText("new.example:12333");
    expect(await page.evaluate(() => window.__noxa.state.tabConnects.get("new-tab")?.spw)).toBe("secret");
});

test("video CPU pressure does not flap quality around its threshold", async ({ page }) => {
    await page.clock.install();
    await page.evaluate(async () => {
        const state = window.__noxa.state;
        Object.assign(state, { activeTabID: "video-tab", myClientID: "self", myChannelID: 1, pc: { getStats: async () => new Map(), getSenders: () => [] } });
        window.__testCPU = 90;
        const app = window.go.main.App;
        window.go.main.App = new Proxy(app, { get(target, key) {
            if (key === "SystemCPUPercent") return async () => {
                if (window.__rejectCPU) throw new Error("CPU telemetry unavailable");
                return window.__testCPU;
            };
            return target[key];
        }});
        const video = await import("/src/video.js");
        const stream = document.createElement("canvas").captureStream(1);
        video.videoTrackAdded(stream.getVideoTracks()[0].id, stream, { client_id: "peer", nickname: "Peer" });
    });
    const qualities = () => page.evaluate(() => (window.__callArgs.SetVideoQualityForTab || []).map(args => args[1]));
    await page.clock.runFor(3000);
    await expect.poll(qualities).toEqual(["low"]);
    for (const cpu of [84, 86, 84, 75, 69, 80]) {
        await page.evaluate(value => { window.__testCPU = value; }, cpu);
        await page.clock.runFor(3000);
    }
    expect(await qualities()).toEqual(["low"]);
    await page.evaluate(() => { window.__testCPU = 60; });
    await page.clock.runFor(12000);
    await page.evaluate(() => { window.__rejectCPU = true; });
    await page.clock.runFor(6000);
    await page.evaluate(() => { window.__rejectCPU = false; });
    await page.clock.runFor(15000);
    expect(await qualities()).toEqual(["low"]);
    await page.clock.runFor(3000);
    await expect.poll(qualities).toEqual(["low", "mid"]);
});

test("video CPU pressure respects efficient decoding and discards stale polls", async ({ page }) => {
    await page.clock.install();
    await page.evaluate(async () => {
        const state = window.__noxa.state;
        window.__efficientDecoder = true;
        const pc = { getStats: async () => new Map([["video", { type: "inbound-rtp", kind: "video", framesDecoded: 20, powerEfficientDecoder: window.__efficientDecoder }]]), getSenders: () => [] };
        Object.assign(state, { activeTabID: "video-tab", myClientID: "self", myChannelID: 1, pc });
        const app = window.go.main.App;
        window.go.main.App = new Proxy(app, { get(target, key) {
            if (key === "SystemCPUPercent") return async () => {
                if (window.__delayCPU) return await new Promise(resolve => { window.__finishCPU = resolve; });
                return 95;
            };
            return target[key];
        }});
        window.__videoForCPU = await import("/src/video.js");
        const stream = document.createElement("canvas").captureStream(1);
        window.__videoForCPU.videoTrackAdded(stream.getVideoTracks()[0].id, stream, { client_id: "peer", nickname: "Peer" });
    });
    await page.clock.runFor(9000);
    expect(await page.evaluate(() => window.__calls.SetVideoQualityForTab || 0)).toBe(0);
    await page.evaluate(() => { window.__efficientDecoder = false; window.__delayCPU = true; });
    await page.clock.runFor(3000);
    await expect.poll(() => page.evaluate(() => typeof window.__finishCPU)).toBe("function");
    await page.evaluate(() => {
        window.__videoForCPU.clearVideoGrid();
        window.__noxa.state.serverGeneration++;
        window.__finishCPU(95);
    });
    await page.clock.runFor(3000);
    expect(await page.evaluate(() => window.__calls.SetVideoQualityForTab || 0)).toBe(0);
});

test("video CPU pressure treats sender and receiver efficiency independently", async ({ page }) => {
    await page.clock.install();
    await page.evaluate(async () => {
        window.__efficientSender = true;
        window.__efficientReceiver = false;
        window.__senderCaps = [];
        const stream = document.createElement("canvas").captureStream(1);
        const track = stream.getVideoTracks()[0];
        const sender = { track, getParameters: () => ({ encodings: [{}] }), setParameters: async value => { window.__senderCaps.push(value); } };
        const pc = {
            getSenders: () => [sender],
            getTransceivers: () => [{ sender, receiver: { track: { kind: "video" } }, direction: "sendonly" }],
            getStats: async () => new Map([
                ["receive", { type: "inbound-rtp", kind: "video", framesDecoded: 20, powerEfficientDecoder: window.__efficientReceiver }],
                ["send", { type: "outbound-rtp", kind: "video", framesEncoded: 20, powerEfficientEncoder: window.__efficientSender }],
            ]),
        };
        Object.assign(window.__noxa.state, { activeTabID: "video-tab", myClientID: "self", myChannelID: 1, pc });
        const app = window.go.main.App;
        window.go.main.App = new Proxy(app, { get(target, key) {
            if (key === "SystemCPUPercent") return async () => 95;
            return target[key];
        }});
        const video = await import("/src/video.js");
        video.videoTrackAdded(track.id, stream, { client_id: "peer", nickname: "Peer" });
    });
    await page.clock.runFor(3000);
    await expect.poll(() => page.evaluate(() => window.__callArgs.SetVideoQualityForTab?.at(-1)?.[1])).toBe("low");
    expect(await page.evaluate(() => window.__senderCaps)).toEqual([]);
    await page.evaluate(() => { window.__efficientSender = false; window.__efficientReceiver = true; });
    await page.clock.runFor(3000);
    await expect.poll(() => page.evaluate(() => window.__callArgs.SetVideoQualityForTab?.at(-1)?.[1])).toBe("mid");
    await expect.poll(() => page.evaluate(() => window.__senderCaps.at(-1)?.encodings[0]?.maxBitrate)).toBe(500000);
});

test("late tab metadata cannot erase successful login credentials", async ({ page }) => {
    await page.evaluate(() => {
        const app = window.go.main.App;
        window.__tabs = [{ id: "login-tab", addr: "voice.example:12333", nickname: "Alice", active: true, connected: true }];
        window.go.main.App = new Proxy(app, { get(target, key) {
            if (key === "ListTabs") return async () => {
                if (!window.__releaseTabMetadata) {
                    await new Promise(resolve => { window.__releaseTabMetadata = resolve; });
                }
                return structuredClone(window.__tabs);
            };
            return target[key];
        }});
        window.__connectBookmarkHandler = async () => ({ tab_id: "login-tab", error: "" });
        for (const callback of window.__events.tab_reset || []) callback("login-tab");
    });
    await expect.poll(() => page.evaluate(() => typeof window.__releaseTabMetadata)).toBe("function");
    await page.locator("#login-addr").fill("voice.example:12333");
    await page.locator("#login-nick").fill("Alice");
    await page.locator("#login-accountpw").fill("account-secret");
    await page.locator("#login-serverpw").fill("server-secret");
    await page.locator("#login-connect").click();
    await expect.poll(() => page.evaluate(() => window.__noxa.state.tabConnects.get("login-tab")?.spw)).toBe("server-secret");
    await page.evaluate(() => window.__releaseTabMetadata());
    await expect.poll(() => page.evaluate(() => window.__noxa.state.lastConnect?.spw)).toBe("server-secret");
    expect(await page.evaluate(() => window.__noxa.state.tabConnects.get("login-tab")?.pw)).toBe("account-secret");
    expect(await page.evaluate(() => window.__noxa.state.lastSuccessfulConnect?.spw)).toBe("server-secret");
});

test("rejects A-to-B-to-A identity results during login finalization", async ({ page }) => {
    await page.evaluate(() => {
        window.__connectTabID = "tab-a";
        window.__tabs = [{
            id: "tab-a", addr: "a.example:12333", nickname: "Alice",
            active: true, connected: true,
        }];
        window.__noxa.state.activeTabID = "tab-a";
        window.__noxa.state.serverGeneration = 10;
        window.__clientIDGate = new Promise((resolve) => {
            window.__releaseABAIdentity = resolve;
        });
    });
    await page.locator("#login-addr").fill("a.example:12333");
    await page.locator("#login-nick").fill("Alice");
    await page.getByRole("button", { name: "Connect" }).click();
    await expect.poll(() => page.evaluate(() => window.__calls.SessionInfoForTab || 0)).toBeGreaterThan(0);

    await page.evaluate(() => {
        // Model A → B → A: the final active tab matches, but the identity
        // response came from the intervening tab and the generation changed.
        window.__activeClient = "client-from-tab-b";
        window.__noxa.state.serverGeneration += 2;
        window.__releaseABAIdentity();
    });

    await expect(page.locator("#login-connect")).toBeEnabled();
    expect(await page.evaluate(() => window.__noxa.state.myClientID)).not.toBe("client-from-tab-b");
    await expect(page.locator("#conn-pill")).not.toHaveClass(/\bup\b/);
});

test("warns about certificate timing after a guest quick-connect", async ({ page }) => {
    await page.evaluate(async () => {
        window.__certificateClockWarning = "Local clock may be inaccurate; check date, time, and time zone.";
        window.__noxa.state.settings.recents = [{
            addr: "quick.example:12333", nickname: "Alice", last_used: 123,
        }];
        await window.__noxaTabs.quickConnectLast();
    });

    await expect(page.locator("#alert-announcer")).toContainText(
        "Certificate timing warning for quick.example:12333",
    );
    expect(await page.evaluate(() => window.__calls.ConnectGuestBookmarkTabWithID)).toBe(1);
    expect(await page.evaluate(() => window.__calls.CertificateClockWarning)).toBe(1);
});

test("reconnects the last server from the tray only while disconnected", async ({ page }) => {
    await page.evaluate(() => {
        const { state } = window.__noxa;
        state.settings.reconnect_on_loss = false;
        state.lastConnect = null;
        state.lastSuccessfulConnect = {
            addr: "voice.example:12333", nick: "Alice", pw: "secret", spw: "", bookmark: "Work",
        };
        const pill = document.getElementById("conn-pill");
        pill.textContent = "offline";
        pill.classList.remove("up");
        for (const callback of window.__events.tray_reconnect || []) callback();
    });

    await expect.poll(() => page.evaluate(() => window.__calls.ConnectBookmarkTabWithID || 0)).toBe(1);
    await expect(page.locator("#conn-pill")).toHaveClass(/\bup\b/);
    expect(await page.evaluate(() => window.__callArgs.ConnectBookmarkTabWithID[0])).toEqual([
        "Work", "voice.example:12333", "Alice", "secret", "",
    ]);

    await page.evaluate(() => {
        for (const callback of window.__events.tray_reconnect || []) callback();
    });
    await page.waitForTimeout(50);
    expect(await page.evaluate(() => window.__calls.ConnectBookmarkTabWithID)).toBe(1);
});

test("keeps an automatic reconnect pinned to the server that dropped", async ({ page }) => {
    await page.evaluate(() => {
        const state = window.__noxa.state;
        state.settings.reconnect_on_loss = true;
        state.lastConnect = {
            addr: "dropped.example:12333", nick: "Alice", pw: "secret", spw: "", bookmark: "Dropped",
        };
        for (const callback of window.__events.disconnected || []) callback();
        state.lastConnect = {
            addr: "switched.example:12333", nick: "Bob", pw: "other", spw: "", bookmark: "Switched",
        };
    });

    await expect.poll(
        () => page.evaluate(() => window.__calls.ConnectBookmarkTabWithID || 0),
        { timeout: 7000 },
    ).toBe(1);
    expect(await page.evaluate(() => window.__callArgs.ConnectBookmarkTabWithID[0])).toEqual([
        "Dropped", "dropped.example:12333", "Alice", "secret", "",
    ]);
});

test("successful automatic reconnect replaces only the exact dropped server tab", async ({ page }) => {
    await page.clock.install();
    await page.evaluate(async () => {
        const state = window.__noxa.state;
        state.activeTabID = "dropped-tab";
        state.settings.reconnect_on_loss = true;
        state.lastConnect = { addr: "voice.example:12333", nick: "Alice", pw: "secret", spw: "", bookmark: "" };
        window.__tabs = [
            { id: "dropped-tab", addr: state.lastConnect.addr, nickname: "Alice", connected: false, active: true },
            { id: "other-tab", addr: state.lastConnect.addr, nickname: "Alice", connected: false, active: false },
        ];
        window.__connectBookmarkHandler = async (_bookmark, addr, nickname) => {
            const tabID = `replacement-${window.__calls.ConnectBookmarkTabWithID}`;
            window.__tabs.forEach((tab) => { tab.active = false; });
            window.__tabs.push({ id: tabID, addr, nickname, connected: true, active: true });
            for (const callback of window.__events.tab_reset || []) callback(tabID);
            return { tab_id: tabID, error: "" };
        };
        window.__closeTabHandler = async (tabID) => {
            window.__tabs = window.__tabs.filter((tab) => tab.id !== tabID);
            for (const callback of window.__events.tab_update || []) callback(structuredClone(window.__tabs));
        };
        for (const callback of window.__events.disconnected || []) callback();
        // A user changes tabs during the retry countdown. Cleanup must still
        // target the tab that dropped, including when both addresses match.
        await window.go.main.App.SetActiveTab("other-tab");
    });
    await page.clock.runFor(5000);
    await expect.poll(() => page.evaluate(() => window.__tabs.map((tab) => tab.id))).toEqual(["other-tab", "replacement-1"]);
    expect(await page.evaluate(() => window.__callArgs.CloseTab)).toEqual([["dropped-tab"]]);

    await page.evaluate(() => {
        window.__tabs.find((tab) => tab.active).connected = false;
        for (const callback of window.__events.disconnected || []) callback();
    });
    await page.clock.runFor(5000);
    await expect.poll(() => page.evaluate(() => window.__tabs.map((tab) => tab.id))).toEqual(["other-tab", "replacement-2"]);
    expect(await page.evaluate(() => window.__callArgs.CloseTab)).toEqual([["dropped-tab"], ["replacement-1"]]);
});

test("failed automatic reconnect preserves the dropped server tab", async ({ page }) => {
    await page.clock.install();
    await page.evaluate(() => {
        const state = window.__noxa.state;
        state.activeTabID = "dropped-tab";
        state.settings.reconnect_on_loss = true;
        state.lastConnect = { addr: "voice.example:12333", nick: "Alice", pw: "secret", spw: "", bookmark: "" };
        window.__tabs = [{ id: "dropped-tab", addr: state.lastConnect.addr, nickname: "Alice", connected: false, active: true }];
        window.__connectBookmarkResult = "connection refused";
        for (const callback of window.__events.disconnected || []) callback();
    });
    await page.clock.runFor(5000);
    await expect.poll(() => page.evaluate(() => window.__calls.ConnectBookmarkTabWithID || 0)).toBe(1);
    expect(await page.evaluate(() => window.__calls.CloseTab || 0)).toBe(0);
    expect(await page.evaluate(() => window.__tabs.map((tab) => tab.id))).toEqual(["dropped-tab"]);
});

test("automatic reconnect does not retire a source tab that is connected again", async ({ page }) => {
    await page.clock.install();
    await page.evaluate(() => {
        const state = window.__noxa.state;
        state.activeTabID = "dropped-tab";
        state.settings.reconnect_on_loss = true;
        state.lastConnect = { addr: "voice.example:12333", nick: "Alice", pw: "secret", spw: "", bookmark: "" };
        window.__tabs = [{ id: "dropped-tab", addr: state.lastConnect.addr, nickname: "Alice", connected: false, active: true }];
        window.__connectBookmarkHandler = async (_bookmark, addr, nickname) => {
            window.__tabs[0].connected = true;
            window.__tabs[0].active = false;
            window.__tabs.push({ id: "replacement", addr, nickname, connected: true, active: true });
            for (const callback of window.__events.tab_reset || []) callback("replacement");
            return { tab_id: "replacement", error: "" };
        };
        for (const callback of window.__events.disconnected || []) callback();
    });
    await page.clock.runFor(5000);
    await expect(page.locator("#conn-pill")).toHaveClass(/\bup\b/);
    expect(await page.evaluate(() => window.__calls.CloseTab || 0)).toBe(0);
    expect(await page.evaluate(() => window.__tabs.map((tab) => tab.id))).toEqual(["dropped-tab", "replacement"]);
});

test("does not finish an in-flight reconnect after an intentional disconnect", async ({ page }) => {
    await page.evaluate(() => {
        const { state } = window.__noxa;
        state.settings.reconnect_on_loss = true;
        state.lastSuccessfulConnect = {
            addr: "voice.example:12333", nick: "Alice", pw: "secret", spw: "", bookmark: "Work",
        };
        window.__connectTabID = "stale-reconnect-tab";
        window.__connectBookmarkGate = new Promise((resolve) => {
            window.__releaseConnectBookmark = resolve;
        });
        for (const callback of window.__events.tray_reconnect || []) callback();
    });
    await expect.poll(() => page.evaluate(() => window.__calls.ConnectBookmarkTabWithID || 0)).toBe(1);

    await page.evaluate(() => {
        for (const callback of window.__events.tray_disconnect || []) callback();
        window.__releaseConnectBookmark();
    });

    await expect.poll(() => page.evaluate(() => window.__calls.DisconnectTab || 0)).toBe(1);
    await expect.poll(() => page.evaluate(() => window.__calls.CloseTab || 0)).toBe(1);
    expect(await page.evaluate(() => window.__callArgs.CloseTab[0])).toEqual(["stale-reconnect-tab"]);
    await expect(page.locator("#conn-pill")).not.toHaveClass(/\bup\b/);
    expect(await page.evaluate(() => window.__noxa.state.lastConnect)).toBeNull();
});

test("labels screen-share controls and explains low-bandwidth data use", async ({ page }) => {
    await page.evaluate(() => {
        const v = window.__noxa;
        v.showWorkspace(false);
        v.state.pc = new RTCPeerConnection();
        window.__shareRemote = new RTCPeerConnection();
        const app = window.go.main.App;
        window.go.main.App = new Proxy(app, { get(target, key) {
            if (key !== "WebRTCOfferForTab") return target[key];
            return async (_tab, sdp) => {
                await window.__shareRemote.setRemoteDescription({ type: "offer", sdp });
                const answer = await window.__shareRemote.createAnswer();
                await window.__shareRemote.setLocalDescription(answer);
                return answer.sdp;
            };
        } });
    });
    await page.getByLabel("Voice options", { exact: true }).click();
    const shareButton = page.locator("#voice-screen");
    const lowBandwidthButton = page.locator("#voice-lowbw");

    await expect(shareButton).toHaveAttribute("title", "Start sharing");
    await expect(shareButton).toHaveAccessibleName("Start sharing");
    await expect(lowBandwidthButton).toHaveAttribute("title", /150 kbps camera or screen-share cap.*incoming data are additional/);
    await expect(lowBandwidthButton).toHaveAccessibleDescription(
        /outgoing video.*68 MB\/hour.*Voice, protocol overhead, and incoming data are additional/,
    );
    await expect(lowBandwidthButton).toHaveAttribute("aria-describedby", "voice-lowbw-estimate");
    await expect(page.locator("#voice-lowbw-estimate")).toBeVisible();
    await expect(page.locator("#voice-lowbw-estimate")).toHaveText("≤68 MB/h video send");

    await lowBandwidthButton.click();
    await expect(page.locator("#voice-lowbw-estimate")).toBeVisible();
    await expect(page.locator("#voice-lowbw-estimate")).toHaveText("≤68 MB/h video send");
    await expect(page.locator("#voice-lowbw-estimate")).toHaveClass(/active/);

    await shareButton.click();
    const shareDialog = page.getByRole("dialog", { name: "Share screen" });
    await expect(shareDialog.getByRole("group", { name: "Source" })).toBeVisible();
    await expect(shareDialog.getByRole("radio")).toHaveCount(3);
    await expect(shareDialog.getByRole("combobox", { name: "Quality preset" })).toBeVisible();
    await expect(shareDialog.getByRole("checkbox", { name: "Include system audio" })).toBeVisible();
    await auditAccessibility(page, "screen-share dialog");

    await page.evaluate(() => {
        navigator.mediaDevices.getDisplayMedia = async () => document.createElement("canvas").captureStream(1);
    });
    await shareDialog.getByRole("button", { name: "Start sharing" }).click();
    await expect(shareButton).toHaveAttribute("title", "Stop sharing");
    await expect(shareButton).toHaveAccessibleName("Stop sharing");

    await shareButton.click();
    await expect(shareButton).toHaveAttribute("title", "Start sharing");
    await expect(shareButton).toHaveAccessibleName("Start sharing");
});

test("switches active server tabs without retaining stale identity", async ({ page }) => {
    await page.evaluate(() => {
        window.__tabs = [
            { id: "tab-a", addr: "a.example:12333", nickname: "alice", active: true, connected: true, unread: 0, mentions: 0 },
            { id: "tab-b", addr: "b.example:12333", nickname: "bob", active: false, connected: true, unread: 2, mentions: 1 },
        ];
        for (const cb of window.__events.tab_update || []) cb(structuredClone(window.__tabs));
        for (const cb of window.__events.tab_reset || []) cb("tab-a");
    });
    await expect(page.locator('.srv-tab[data-tab-id="tab-a"]')).toHaveClass(/active/);
    await page.evaluate(() => {
        const state = window.__noxa.state;
        state.clients = [{ client_id: "old-client", unique_id: "old-user", nickname: "Old", roles: [{ id: 7, name: "Old server admins" }] }];
        state.avatars = new Map([["old-user", "old-avatar"]]);
        state.avatarPending = new Set(["old-user"]);
        const serverIcon = document.getElementById("server-icon");
        serverIcon.src = "data:image/png;base64,AAAA";
        serverIcon.classList.remove("hidden");
    });
    await page.locator('.srv-tab[data-tab-id="tab-b"]').click();
    await expect(page.locator('.srv-tab[data-tab-id="tab-b"]')).toHaveClass(/active/);
    await expect.poll(() => page.evaluate(() => window.__noxa.state.myClientID)).toBe("client-b");
    await expect.poll(() => page.evaluate(() => ({
        clients: window.__noxa.state.clients.length,
        avatars: window.__noxa.state.avatars.size,
        avatarPending: window.__noxa.state.avatarPending.size,
    }))).toEqual({ clients: 0, avatars: 0, avatarPending: 0 });
    await expect(page.locator("#server-icon")).toHaveClass(/hidden/);
    await expect(page.locator("#server-icon")).not.toHaveAttribute("src", /.+/);
});

test("recognises an immediate channel join while switched-tab identity is loading", async ({ page }) => {
    await page.evaluate(() => {
        window.__tabs = [
            { id: "tab-a", addr: "a.example:12333", nickname: "alice", active: true, connected: true, unread: 0, mentions: 0 },
            { id: "tab-b", addr: "b.example:12333", nickname: "bob", active: false, connected: true, unread: 0, mentions: 0 },
        ];
        window.__activeClient = "client-a";
        window.__noxa.state.myClientID = "client-a";
        let release;
        window.__clientIDGate = new Promise((resolve) => { release = resolve; });
        window.__releaseClientID = release;
        for (const cb of window.__events.tab_update || []) cb(structuredClone(window.__tabs));
    });

    await page.locator('.srv-tab[data-tab-id="tab-b"]').click();
    await page.evaluate(() => {
        const snapshot = JSON.stringify({
            root_channels: [{
                ChannelID: 42, ParentID: 0, Name: "Lobby",
                clients: [{ client_id: "client-b", unique_id: "user-b", nickname: "Bob", channel_id: 0, is_speaking: false }],
                children: [],
            }],
        });
        const moved = JSON.stringify({ type: "user_moved", data: { client_id: "client-b", channel_id: 42 } });
        for (const cb of window.__events.snapshot || []) cb(snapshot);
        for (const cb of window.__events.event || []) cb(moved);
        window.__releaseClientID();
        window.__clientIDGate = null;
    });

    await expect.poll(() => page.evaluate(() => window.__noxa.state.myClientID)).toBe("client-b");
    await expect.poll(() => page.evaluate(() => window.__noxa.state.myChannelID)).toBe(42);
});

test("removes cascaded deleted channels and displaces every cached member safely", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        window.__noxa.state.myClientID = "";
        for (const callback of window.__events.snapshot || []) callback(JSON.stringify({
            root_channels: [
                {
                    ChannelID: 10, ParentID: 0, Name: "Parent",
                    clients: [],
                    children: [{
                        ChannelID: 11, ParentID: 10, Name: "Child",
                        clients: [{ client_id: "client-a", unique_id: "user-a", nickname: "Alice", channel_id: 11 }],
                        children: [{
                            ChannelID: 12, ParentID: 11, Name: "Grandchild",
                            clients: [{ client_id: "client-b", unique_id: "user-b", nickname: "Bob", channel_id: 12 }],
                            children: [],
                        }],
                    }],
                },
                {
                    ChannelID: 20, ParentID: 0, Name: "Unaffected",
                    clients: [],
                    children: [{
                        ChannelID: 21, ParentID: 20, Name: "Legacy child",
                        clients: [],
                        children: [{
                            ChannelID: 22, ParentID: 21, Name: "Legacy grandchild",
                            clients: [{ client_id: "client-c", unique_id: "user-c", nickname: "Carol", channel_id: 22 }],
                            children: [],
                        }],
                    }],
                },
            ],
        }));
        window.__noxa.state.myClientID = "client-a";
        window.__noxa.state.myChannelID = 11;
        window.__voiceTracksStopped = 0;
        window.__noxa.state.localStream = {
            getTracks: () => [{ stop: () => { window.__voiceTracksStopped++; } }],
            getAudioTracks: () => [],
            getVideoTracks: () => [],
        };
        document.getElementById("voice-status").textContent = "voice on";
        window.__noxa.state.collapsedChannels.add(10);
        window.__noxa.state.collapsedChannels.add(20);
        window.__noxa.state.expandedVirtual.add(12);
        window.__noxa.state.expandedVirtual.add(22);
        window.__noxa.renderTree();
    });

    await page.locator("#tab-files").click();
    await expect(page.locator("#files-pane .fb-list")).not.toContainText("Join a channel");
    await page.evaluate(() => {
        const event = JSON.stringify({
            type: "channel_deleted",
            data: { channel_id: 10, channel_ids: [10, 11, 12] },
        });
        for (const callback of window.__events.event || []) callback(event);
    });

    await expect.poll(() => page.evaluate(() => ({
        channels: window.__noxa.state.channels.map((channel) => channel.ChannelID),
        clients: Object.fromEntries(window.__noxa.state.clients.map((client) => [client.client_id, client.channel_id])),
        myChannelID: window.__noxa.state.myChannelID,
        localStreamCleared: window.__noxa.state.localStream === null,
        stopped: window.__voiceTracksStopped,
        collapsedDeleted: !window.__noxa.state.collapsedChannels.has(10),
        expandedDeleted: !window.__noxa.state.expandedVirtual.has(12),
    }))).toEqual({
        channels: [20, 21, 22],
        clients: { "client-a": 0, "client-b": 0, "client-c": 22 },
        myChannelID: 0,
        localStreamCleared: true,
        stopped: 1,
        collapsedDeleted: true,
        expandedDeleted: true,
    });
    await expect(page.locator('.channel[data-chid="10"], .channel[data-chid="11"], .channel[data-chid="12"]')).toHaveCount(0);
    await expect(page.locator('.channel[data-chid="20"]')).toHaveCount(1);
    await expect(page.locator("#voice-status")).toHaveText("voice off");
    await expect(page.locator("#files-pane .fb-list")).toContainText("Join a channel to browse its files");

    // Legacy servers send only the parent channel_id. Re-enter the surviving
    // subtree so this second deletion exercises descendant member and voice
    // cleanup rather than merely deleting a leaf.
    await page.evaluate(() => {
        const state = window.__noxa.state;
        state.myClientID = "client-c";
        state.myChannelID = 22;
        state.localStream = {
            getTracks: () => [{ stop: () => { window.__voiceTracksStopped++; } }],
            getAudioTracks: () => [],
            getVideoTracks: () => [],
        };
        document.getElementById("voice-status").textContent = "voice on";
        const event = JSON.stringify({ type: "channel_deleted", data: { channel_id: 20 } });
        for (const callback of window.__events.event || []) callback(event);
    });
    await expect.poll(() => page.evaluate(() => ({
        channels: window.__noxa.state.channels.length,
        carolChannel: window.__noxa.state.clients.find((client) => client.client_id === "client-c")?.channel_id,
        myChannelID: window.__noxa.state.myChannelID,
        localStreamCleared: window.__noxa.state.localStream === null,
        stopped: window.__voiceTracksStopped,
        collapsedDeleted: !window.__noxa.state.collapsedChannels.has(20),
        expandedDeleted: !window.__noxa.state.expandedVirtual.has(22),
    }))).toEqual({
        channels: 0,
        carolChannel: 0,
        myChannelID: 0,
        localStreamCleared: true,
        stopped: 2,
        collapsedDeleted: true,
        expandedDeleted: true,
    });
    await expect(page.locator('.channel[data-chid="20"], .channel[data-chid="21"], .channel[data-chid="22"]')).toHaveCount(0);
    await expect(page.locator("#voice-status")).toHaveText("voice off");
});

test("starts voice, plays the original channel join cue, and switches channels", async ({ page }) => {
    await page.evaluate(async () => {
        window.__getUserMediaCalls = 0;
        window.__playedMedia = [];
        const engine = window.__noxa.soundEngine;
        await engine.preload();
        await engine.resume();
        const source = engine.ctx.createBufferSource.bind(engine.ctx);
        engine.ctx.createBufferSource = () => {
            const node = source();
            const start = node.start.bind(node);
            node.start = (...args) => {
                window.__playedMedia.push([...engine.buffers].find(([, buffer]) => node.buffer === buffer)?.[0]);
                start(...args);
            };
            return node;
        };
        navigator.mediaDevices.getUserMedia = async () => {
            window.__getUserMediaCalls++;
            throw new DOMException("test permission denial", "NotAllowedError");
        };
        window.__noxa.state.myClientID = "client-a";
        window.__noxa.state.myChannelID = 0;
        window.__noxa.state.clients = [{
            client_id: "client-a", unique_id: "user-a", nickname: "Alice",
            channel_id: 0, is_speaking: false,
        }];
        const moved = JSON.stringify({ type: "user_moved", data: { client_id: "client-a", channel_id: 42 } });
        for (const cb of window.__events.event || []) cb(moved);
    });

    await expect(page.locator("#voice-join")).toHaveCount(0);
    await expect.poll(() => page.evaluate(() => window.__getUserMediaCalls)).toBeGreaterThan(0);
    await expect.poll(() => page.evaluate(
        () => window.__playedMedia.some((src) => src.includes("channel_join")),
    )).toBe(true);
    await expect(page.locator("#voice-status")).toHaveText("voice unavailable");
    await expect(page.locator("#mic-status > span")).toHaveText("Microphone access denied");

    const firstCueCount = await page.evaluate(() => window.__playedMedia.length);
    await page.evaluate(() => {
        const emitMove = (clientID, channelID) => {
            const moved = JSON.stringify({ type: "user_moved", data: { client_id: clientID, channel_id: channelID } });
            for (const cb of window.__events.event || []) cb(moved);
        };
        emitMove("someone-else", 42);
        emitMove("client-a", 42);
    });
    await expect.poll(() => page.evaluate(() => window.__playedMedia.length)).toBe(firstCueCount);

    await page.evaluate(() => {
        const moved = JSON.stringify({ type: "user_moved", data: { client_id: "client-a", channel_id: 43 } });
        for (const cb of window.__events.event || []) cb(moved);
    });
    // A switch uses its own authored cue and does not replay channel join.
    await expect.poll(() => page.evaluate(() => window.__noxa.state.myChannelID)).toBe(43);
    await expect.poll(() => page.evaluate(() => window.__playedMedia.at(-1))).toBe("own_channel_switch");
    await page.evaluate(() => {
        const moved = JSON.stringify({ type: "user_moved", data: { client_id: "client-a", channel_id: 0 } });
        for (const cb of window.__events.event || []) cb(moved);
    });
    await expect(page.locator("#mic-status")).toBeEmpty();
});

test("undeafens after a confirmed channel join but preserves deafen on duplicate and remote events", async ({ page }) => {
    await showB3Workspace(page);
    const emitMove = (clientID, channelID) => page.evaluate(({ clientID, channelID }) => {
        for (const callback of window.__events.event || []) callback(JSON.stringify({
            type: "user_moved", data: { client_id: clientID, channel_id: channelID },
        }));
    }, { clientID, channelID });
    await page.evaluate(() => window.__noxa.setDeafened(true));
    await emitMove("mia", 3);
    await emitMove("daniel", 2);
    await expect(page.getByRole("button", { name: "Undeafen", exact: true })).toHaveAttribute("aria-pressed", "true");
    await emitMove("daniel", 3);
    await expect(page.getByRole("button", { name: "Deafen", exact: true })).toHaveAttribute("aria-pressed", "false");
    expect(await page.locator("#remote-video").evaluate((element) => element.muted)).toBe(false);
    await page.evaluate(() => window.__noxa.setDeafened(true));
    await emitMove("daniel", 0);
    expect(await page.evaluate(() => window.__noxa.state.deafened)).toBe(true);
    await emitMove("daniel", 2);
    expect(await page.evaluate(() => window.__noxa.state.deafened)).toBe(false);
});

test("undeafens when a snapshot confirms joining a channel", async ({ page }) => {
    await showB3Workspace(page);
    await page.evaluate(() => {
        window.__noxa.setDeafened(true);
        for (const callback of window.__events.snapshot || []) callback(JSON.stringify({
            root_channels: [{ ChannelID: 3, Name: "Gaming", clients: [{
                client_id: "daniel", unique_id: "uid-daniel", nickname: "Daniel", channel_id: 3,
            }], children: [] }],
        }));
    });
    await expect(page.getByRole("button", { name: "Deafen", exact: true })).toHaveAttribute("aria-pressed", "false");
    expect(await page.locator("#remote-video").evaluate((element) => element.muted)).toBe(false);
});

test.describe("tab-bound file inspection", () => {
    test.beforeEach(async ({ page }) => {
        await page.evaluate(() => {
            const v = window.__noxa;
            v.showWorkspace(false);
            Object.assign(v.state, { activeTabID: "server-a", myChannelID: 42, lastConnect: { addr: "a.example:12333" } });
            const f = window.__fileScope = { nativeTab: "server-a", calls: [], effects: [], copied: [], toasts: [], entries: [{ name: "report.txt", size: 5, sha256: "abc" }] };
            v.toast = text => f.toasts.push(text);
            window.runtime.ClipboardSetText = async value => { f.copied.push(value); return true; };
            const app = window.go.main.App;
            window.go.main.App = new Proxy(app, { get(target, key) {
                const method = key.replace(/ForTab$/, "");
                if (!["FileList", "FileVersions", "FileLink", "FileDelete", "FileRename", "VerifyFile"].includes(method)) return target[key];
                return async (...args) => {
                    const tab = key.endsWith("ForTab") ? args.shift() : f.nativeTab;
                    f.calls.push([method, tab, ...args]);
                    if (tab !== f.nativeTab) {
                        if (method === "FileDelete" || method === "FileRename") return "server changed";
                        throw new Error("server changed");
                    }
                    f.effects.push([method, tab]);
                    if (method === "FileList") return { entries: [...f.entries], folders: [] };
                    if (method === "FileDelete" || method === "FileRename") {
                        if (f.gate) await f.gate;
                        f.completed = true;
                        if (!f.error) f.entries = method === "FileDelete" || args[5] ? [] : [{ ...f.entries[0], name: args[4] }];
                        return f.error || "";
                    }
                    if (method === "VerifyFile") {
                        if (f.gate) await f.gate;
                        f.completed = true;
                        return true;
                    }
                    if (method === "FileVersions") {
                        if (f.gate) await f.gate;
                        f.completed = true;
                        return { entries: [{ name: "old-report.txt", size: 5 }] };
                    }
                    if (f.gate) await f.gate;
                    f.completed = true;
                    return { path: "/dl/00112233445566778899aabbccddeeff", health_port: 12334, scheme: "https", expires_at: 1000, session_bound: true };
                };
            } });
        });
        await page.locator("#tab-files").click();
        await expect(page.locator(".fb-filename")).toHaveText(["report.txt"]);
        await page.evaluate(() => { window.__fileScope.calls = []; window.__fileScope.effects = []; });
    });
    test("native activation cannot redirect a folder refresh", async ({ page }) => {
        await page.evaluate(() => { window.__fileScope.nativeTab = "server-b"; });
        await page.getByRole("button", { name: "Refresh files", exact: true }).click();
        await expect.poll(() => page.evaluate(() => window.__fileScope.calls)).toEqual([["FileList", "server-a", 42, ""]]);
        expect(await page.evaluate(() => window.__fileScope.effects)).toEqual([]);
    });
    for (const [label, method] of [["Versions", "FileVersions"], ["Copy download link (15 min)", "FileLink"]]) {
        test(`native activation cannot redirect ${method}`, async ({ page }) => {
            await page.locator(".fb-action-menu summary").click();
            await page.evaluate(() => { window.__fileScope.nativeTab = "server-b"; });
            await page.getByRole("button", { name: label, exact: true }).click();
            await expect.poll(() => page.evaluate(() => window.__fileScope.calls)).toEqual([[method, "server-a", 42, "", "report.txt"]]);
            expect(await page.evaluate(() => window.__fileScope.effects)).toEqual([]);
        });
    }
    test("late versions cannot render after frontend tab identity changes", async ({ page }) => {
        await page.evaluate(() => { window.__fileScope.gate = new Promise(resolve => { window.__fileScope.finish = resolve; }); });
        await page.locator(".fb-action-menu summary").click();
        await page.getByRole("button", { name: "Versions", exact: true }).click();
        await expect.poll(() => page.evaluate(() => window.__fileScope.calls.length)).toBe(1);
        await page.evaluate(() => { window.__noxa.state.activeTabID = "server-b"; window.__fileScope.finish(); });
        await expect.poll(() => page.evaluate(() => window.__fileScope.completed)).toBe(true);
        await expect(page.locator(".fb-ver-name")).toHaveCount(0);
    });
    test("current link copies the originating server URL", async ({ page }) => {
        await page.locator(".fb-action-menu summary").click();
        await page.getByRole("button", { name: "Copy download link (15 min)", exact: true }).click();
        await expect.poll(() => page.evaluate(() => window.__fileScope.copied)).toEqual(["https://a.example:12334/dl/00112233445566778899aabbccddeeff"]);
    });
    test("late link cannot replace the clipboard after frontend tab identity changes", async ({ page }) => {
        await page.evaluate(() => { window.__fileScope.gate = new Promise(resolve => { window.__fileScope.finish = resolve; }); });
        await page.locator(".fb-action-menu summary").click();
        await page.getByRole("button", { name: "Copy download link (15 min)", exact: true }).click();
        await expect.poll(() => page.evaluate(() => window.__fileScope.calls.length)).toBe(1);
        await page.evaluate(() => { window.__noxa.state.activeTabID = "server-b"; window.__fileScope.finish(); });
        await expect.poll(() => page.evaluate(() => window.__fileScope.completed)).toBe(true);
        expect(await page.evaluate(() => window.__fileScope.copied)).toEqual([]);
    });
    test("native activation cannot redirect checksum verification", async ({ page }) => {
        await page.locator(".fb-action-menu summary").click();
        await page.evaluate(() => { window.__fileScope.nativeTab = "server-b"; });
        await page.getByRole("button", { name: "Verify checksum", exact: true }).click();
        await expect.poll(() => page.evaluate(() => window.__fileScope.calls)).toEqual([["VerifyFile", "server-a", 42, "", "report.txt", "abc"]]);
        expect(await page.evaluate(() => window.__fileScope.effects)).toEqual([]);
    });
    test("late checksum cannot render after frontend tab identity changes", async ({ page }) => {
        await page.evaluate(() => { window.__fileScope.gate = new Promise(resolve => { window.__fileScope.finish = resolve; }); });
        await page.locator(".fb-action-menu summary").click();
        await page.getByRole("button", { name: "Verify checksum", exact: true }).click();
        await expect.poll(() => page.evaluate(() => window.__fileScope.calls.length)).toBe(1);
        await page.evaluate(() => { window.__noxa.state.activeTabID = "server-b"; window.__fileScope.finish(); });
        await expect.poll(() => page.evaluate(() => window.__fileScope.completed)).toBe(true);
        expect(await page.locator(".fb-sha").textContent()).toBe("…");
    });
    for (const action of ["delete", "rename", "move"]) {
        for (const outcome of ["saved", "denied", "replaced"]) {
            test(`file ${action} waits for ${outcome} result before feedback or refresh`, async ({ page }) => {
                await page.evaluate(outcome => {
                    window.__noxa.state.channels = [{ ChannelID: 42, Name: "Source" }, { ChannelID: 43, Name: "Target" }];
                    const f = window.__fileScope;
                    f.error = outcome === "saved" ? "" : "file operation denied";
                    f.gate = new Promise(resolve => { f.finish = resolve; });
                }, outcome);
                await page.locator(".fb-action-menu summary").click();
                const title = { move: "Move to another channel", rename: "Rename / move within channel", delete: "delete" }[action];
                await page.locator(`.fb-action-list button[title="${title}"]`).click();
                const dialog = page.getByRole("dialog");
                if (action === "rename") await dialog.locator("input").fill("renamed.txt");
                await dialog.locator(".dlg-ok").click();
                await expect.poll(() => page.evaluate(() => window.__fileScope.calls.length)).toBe(1);
                expect(await page.evaluate(() => window.__fileScope.completed)).not.toBe(true);
                expect(await page.evaluate(() => window.__fileScope.toasts)).toEqual([]);
                await expect(page.locator(".fb-filename")).toHaveText(["report.txt"]);
                await page.evaluate(outcome => {
                    if (outcome === "replaced") window.__noxa.state.activeTabID = "server-b";
                    window.__fileScope.finish();
                }, outcome);
                await expect.poll(() => page.evaluate(() => window.__fileScope.completed)).toBe(true);
                if (outcome === "replaced") {
                    expect(await page.evaluate(() => window.__fileScope.calls.length)).toBe(1);
                    expect(await page.evaluate(() => window.__fileScope.toasts)).toEqual([]);
                    await expect(page.locator(".fb-filename")).toHaveText(["report.txt"]);
                } else {
                    await expect.poll(() => page.evaluate(() => window.__fileScope.calls.filter(c => c[0] === "FileList").length)).toBe(1);
                    if (outcome === "denied") {
                        expect(await page.evaluate(() => window.__fileScope.toasts.join(" "))).toContain("file operation denied");
                        await expect(page.locator(".fb-filename")).toHaveText(["report.txt"]);
                    } else {
                        await expect(page.locator(".fb-filename")).toHaveText(action === "rename" ? ["renamed.txt"] : []);
                        if (action === "move") expect(await page.evaluate(() => window.__fileScope.toasts.join(" "))).toContain("Target");
                    }
                }
            });
        }

        test(`native activation cannot redirect confirmed file ${action}`, async ({ page }) => {
            await page.evaluate(() => { window.__noxa.state.channels = [{ ChannelID: 42, Name: "Source" }, { ChannelID: 43, Name: "Target" }]; });
            await page.locator(".fb-action-menu summary").click();
            const title = { move: "Move to another channel", rename: "Rename / move within channel", delete: "delete" }[action];
            await page.locator(`.fb-action-list button[title="${title}"]`).click();
            const dialog = page.getByRole("dialog");
            await expect(dialog).toBeVisible();
            if (action === "rename") await dialog.locator("input").fill("renamed.txt");
            await page.evaluate(() => { window.__fileScope.nativeTab = "server-b"; });
            await dialog.locator(".dlg-ok").click();
            await expect.poll(() => page.evaluate(() => window.__fileScope.calls.length)).toBeGreaterThan(0);
            const call = await page.evaluate(() => window.__fileScope.calls[0]);
            expect(call.slice(0, 5)).toEqual([action === "delete" ? "FileDelete" : "FileRename", "server-a", 42, "", "report.txt"]);
            expect(await page.evaluate(() => window.__fileScope.effects)).toEqual([]);
        });
    }
});

test.describe("tab-bound file transfers", () => {
    test.beforeEach(async ({ page }) => {
        await page.evaluate(() => {
            const v = window.__noxa;
            v.showWorkspace(false);
            Object.assign(v.state, { activeTabID: "server-a", myChannelID: 42, channels: [{ ChannelID: 42, Name: "Files" }] });
            window.__fileListResponse = { entries: [{ name: "report.txt", size: 5 }], folders: [] };
            const f = window.__transferScope = { nativeTab: "server-a", paths: ["C:\\fixture\\upload.txt"], calls: [], effects: [] };
            const app = window.go.main.App;
            window.go.main.App = new Proxy(app, { get(target, key) {
                if (key === "PickUploadPaths") return async () => { if (f.pickerGate) await f.pickerGate; return f.paths; };
                if (key === "DownloadPath") return async () => { if (f.pathGate) await f.pathGate; return "C:\\fixture\\report.txt"; };
                const method = key.replace(/ForTab$/, "");
                if (!["UploadPathProgress", "UploadFileProgress", "DownloadFileProgress", "CancelTransfer"].includes(method)) return target[key];
                return async (...args) => {
                    const tab = key.endsWith("ForTab") ? args.shift() : f.nativeTab;
                    f.calls.push([method, tab, ...args]);
                    if (tab !== f.nativeTab) return "server changed";
                    f.effects.push([method, tab]);
                    if (method === "UploadPathProgress" && f.uploadGates?.[tab]) await f.uploadGates[tab];
                    return "";
                };
            } });
        });
        await page.locator("#tab-files").click();
        await expect(page.locator(".fb-filename")).toHaveText("report.txt");
    });
    for (const kind of ["upload", "download", "cancel"]) {
        test(`native activation cannot redirect ${kind}`, async ({ page }) => {
            if (kind === "cancel") {
                await page.evaluate(() => {
                    for (const cb of window.__events.ft_progress || []) cb({ id: "old", direction: "download", name: "old.txt", status: "active" });
                });
                await page.locator("#tab-transfers").click();
            }
            await page.evaluate(() => { window.__transferScope.nativeTab = "server-b"; });
            await page.locator(kind === "upload" ? ".fb-upload" : kind === "download" ? '.fb-actions button[title="Download"]' : '.tr-row button').click();
            await expect.poll(() => page.evaluate(() => window.__transferScope.calls.length)).toBe(1);
            expect(await page.evaluate(() => window.__transferScope.calls[0][1])).toBe("server-a");
            expect(await page.evaluate(() => window.__transferScope.effects)).toEqual([]);
        });
    }
    test("upload picker results cannot select a different channel", async ({ page }) => {
        await page.evaluate(() => { window.__transferScope.pickerGate = new Promise(resolve => { window.__transferScope.finishPicker = resolve; }); });
        await page.locator(".fb-upload").click();
        await page.evaluate(() => {
            window.__noxa.state.myChannelID = 43;
            window.__noxaFiles.onChannelChanged();
            window.__transferScope.finishPicker();
        });
        await expect(page.locator(".fb-filename")).toHaveText("report.txt");
        expect(await page.evaluate(() => window.__transferScope.calls)).toEqual([]);
    });
    test("late download path cannot start on another frontend tab", async ({ page }) => {
        await page.evaluate(() => { window.__transferScope.pathGate = new Promise(resolve => { window.__transferScope.finishPath = resolve; }); });
        await page.getByRole("button", { name: "Download", exact: true }).click();
        await page.evaluate(() => { window.__noxa.state.activeTabID = "server-b"; window.__transferScope.finishPath(); });
        expect(await page.evaluate(() => window.__transferScope.calls)).toEqual([]);
    });
    test("late upload initialization cannot release a new server queue", async ({ page }) => {
        await page.evaluate(() => {
            const f = window.__transferScope;
            f.uploadGates = { "server-a": new Promise(resolve => { f.finishOld = resolve; }), "server-b": new Promise(resolve => { f.finishNew = resolve; }) };
        });
        await page.locator(".fb-upload").click();
        await expect.poll(() => page.evaluate(() => window.__transferScope.calls.length)).toBe(1);
        await page.evaluate(() => {
            const f = window.__transferScope;
            f.nativeTab = "server-b";
            window.__noxa.state.activeTabID = "server-b";
            window.__noxaFiles.resetServerView();
            window.__noxaFiles.onChannelChanged();
            f.paths = ["C:\\fixture\\first.txt", "C:\\fixture\\second.txt"];
        });
        await page.locator(".fb-upload").click();
        await expect.poll(() => page.evaluate(() => window.__transferScope.calls.filter(c => c[1] === "server-b").length)).toBe(1);
        await page.evaluate(() => window.__transferScope.finishOld());
        expect(await page.evaluate(() => window.__transferScope.calls.filter(c => c[1] === "server-b").length)).toBe(1);
    });
    test("dropped bytes cannot follow native activation", async ({ page }) => {
        await page.evaluate(() => {
            window.__transferScope.nativeTab = "server-b";
            const data = new DataTransfer();
            data.items.add(new File(["data"], "drop.txt"));
            document.getElementById("files-pane").dispatchEvent(new DragEvent("drop", { dataTransfer: data, bubbles: true }));
        });
        await expect.poll(() => page.evaluate(() => window.__transferScope.calls.length)).toBe(1);
        expect(await page.evaluate(() => window.__transferScope.calls[0])).toEqual(["UploadFileProgress", "server-a", "up-1", 42, "", "drop.txt", "ZGF0YQ=="]);
        expect(await page.evaluate(() => window.__transferScope.effects)).toEqual([]);
    });
    test("late dropped-file reading cannot start on another frontend tab", async ({ page }) => {
        await page.evaluate(() => {
            const f = window.__transferScope;
            File.prototype.arrayBuffer = async function () {
                f.readStarted = true;
                await new Promise(resolve => { f.finishRead = resolve; });
                return new TextEncoder().encode("data").buffer;
            };
            const data = new DataTransfer();
            data.items.add(new File(["data"], "drop.txt"));
            document.getElementById("files-pane").dispatchEvent(new DragEvent("drop", { dataTransfer: data, bubbles: true }));
        });
        await expect.poll(() => page.evaluate(() => window.__transferScope.readStarted)).toBe(true);
        await page.evaluate(() => { window.__noxa.state.activeTabID = "server-b"; window.__transferScope.finishRead(); });
        expect(await page.evaluate(() => window.__transferScope.calls)).toEqual([]);
    });
    test("retry retains its original server and download target", async ({ page }) => {
        await page.getByRole("button", { name: "Download", exact: true }).click();
        await expect.poll(() => page.evaluate(() => window.__transferScope.calls.length)).toBe(1);
        await page.evaluate(() => {
            const id = window.__transferScope.calls[0][2];
            for (const cb of window.__events.ft_progress || []) cb({ id, direction: "download", name: "report.txt", status: "error" });
        });
        await page.locator("#tab-transfers").click();
        await page.evaluate(() => { window.__transferScope.nativeTab = "server-b"; });
        await page.locator(".tr-row button").click();
        await expect.poll(() => page.evaluate(() => window.__transferScope.calls.length)).toBe(2);
        const calls = await page.evaluate(() => window.__transferScope.calls);
        expect(calls[1]).toEqual(calls[0]);
        expect(await page.evaluate(() => window.__transferScope.effects)).toEqual([["DownloadFileProgress", "server-a"]]);
    });
    test("failed downloads can resume after returning to the original tab", async ({ page }) => {
        await page.getByRole("button", { name: "Download", exact: true }).click();
        await expect.poll(() => page.evaluate(() => window.__transferScope.calls.length)).toBe(1);
        await page.evaluate(() => {
            const id = window.__transferScope.calls[0][2];
            for (const cb of window.__events.ft_progress || []) cb({ id, direction: "download", name: "report.txt", status: "error" });
            for (const tab of ["server-b", "server-a"]) {
                window.__transferScope.nativeTab = tab;
                window.__noxa.state.activeTabID = tab;
                window.__noxaFiles.resetServerView();
                window.__noxaFiles.onChannelChanged();
            }
        });
        await page.locator("#tab-transfers").click();
        await page.locator(".tr-row button").click({ timeout: 1500 });
        await expect.poll(() => page.evaluate(() => window.__transferScope.calls.length)).toBe(2);
        const calls = await page.evaluate(() => window.__transferScope.calls);
        expect(calls[1]).toEqual(calls[0]);
    });
    test("background failure replay replaces stale active state and preserves Resume", async ({ page }) => {
        await page.getByRole("button", { name: "Download", exact: true }).click();
        await expect.poll(() => page.evaluate(() => window.__transferScope.calls.length)).toBe(1);
        await page.evaluate(() => {
            const f = window.__transferScope;
            const id = f.calls[0][2];
            for (const cb of window.__events.ft_progress || []) cb({ id, direction: "download", name: "report.txt", status: "active" });
            window.__noxa.state.activeTabID = "server-b";
            window.__noxaFiles.resetServerView();
            for (const cb of window.__events.ft_progress || []) cb({ id, direction: "download", name: "other-server.txt", status: "active" });
            window.__noxa.state.activeTabID = "server-a";
            window.__noxaFiles.resetServerView();
            for (const cb of window.__events.ft_snapshot || []) cb(JSON.stringify({ transfers: [{ id, direction: "download", name: "report.txt", status: "error", error: "connection closed" }] }));
        });
        await page.locator("#tab-transfers").click();
        await expect(page.locator(".tr-row")).toHaveCount(1);
        await expect(page.locator(".tr-row")).toHaveClass(/error/);
        await expect(page.locator(".tr-list")).not.toContainText("other-server.txt");
        await page.locator(".tr-row button").click();
        await expect.poll(() => page.evaluate(() => window.__transferScope.calls.length)).toBe(2);
        const calls = await page.evaluate(() => window.__transferScope.calls);
        expect(calls[1]).toEqual(calls[0]);
    });
    test("authoritative transfer snapshots remove expired rows only from their own tab", async ({ page }) => {
        await page.evaluate(() => {
            for (const cb of window.__events.ft_progress || []) cb({ id: "old", direction: "download", name: "expired.txt", status: "active" });
            window.__noxa.state.activeTabID = "server-b";
            window.__noxaFiles.resetServerView();
            for (const cb of window.__events.ft_progress || []) cb({ id: "old", direction: "download", name: "retained.txt", status: "active" });
            window.__noxa.state.activeTabID = "server-a";
            window.__noxaFiles.resetServerView();
            for (const cb of window.__events.ft_snapshot || []) cb({ transfers: [] });
        });
        await page.locator("#tab-transfers").click();
        await expect(page.locator(".tr-row")).toHaveCount(0);
        await page.evaluate(() => { window.__noxa.state.activeTabID = "server-b"; window.__noxaFiles.resetServerView(); });
        await expect(page.locator(".tr-name")).toHaveText("retained.txt");
    });
    test("transfer history keeps latest completions and all active rows", async ({ page }) => {
        await page.evaluate(() => {
            const send = value => { for (const cb of window.__events.ft_progress || []) cb(value); };
            send({ id: "running", direction: "upload", name: "running.txt", status: "active" });
            for (let i = 0; i < 40; i++) send({ id: `done-${i}`, direction: "download", name: `done-${i}.txt`, status: "done" });
        });
        await page.locator("#tab-transfers").click();
        await expect(page.locator(".tr-row")).toHaveCount(21);
        const names = await page.locator(".tr-name").allTextContents();
        expect(names).toEqual(expect.arrayContaining(["running.txt", ...Array.from({ length: 20 }, (_, i) => `done-${i + 20}.txt`)]));
    });
    test("queued uploads stay sequential and retain the chosen channel", async ({ page }) => {
        await page.evaluate(() => { window.__transferScope.paths = ["C:\\fixture\\first.txt", "C:\\fixture\\second.txt"]; });
        await page.locator(".fb-upload").click();
        await expect.poll(() => page.evaluate(() => window.__transferScope.calls.length)).toBe(1);
        await page.evaluate(() => {
            window.__noxa.state.myChannelID = 43;
            window.__noxaFiles.onChannelChanged();
            const id = window.__transferScope.calls[0][2];
            for (const cb of window.__events.ft_progress || []) cb({ id, direction: "upload", name: "first.txt", status: "done" });
        });
        await expect.poll(() => page.evaluate(() => window.__transferScope.calls.length)).toBe(2);
        expect(await page.evaluate(() => window.__transferScope.calls[1])).toEqual(["UploadPathProgress", "server-a", "up-2", 42, "", "C:\\fixture\\second.txt"]);
    });
    test("old upload polling cannot release the replacement queue or reveal its rows", async ({ page }) => {
        await page.evaluate(() => {
            const f = window.__transferScope;
            f.polls = [];
            const original = window.setInterval;
            window.setInterval = (callback, delay, ...args) => {
                if (delay === 300) { f.polls.push(callback); return 100000 + f.polls.length; }
                return original(callback, delay, ...args);
            };
        });
        await page.locator(".fb-upload").click();
        await expect.poll(() => page.evaluate(() => window.__transferScope.polls.length)).toBe(1);
        await page.evaluate(() => {
            const f = window.__transferScope;
            for (const cb of window.__events.ft_progress || []) cb({ id: f.calls[0][2], direction: "upload", name: "old-secret.txt", status: "active" });
            f.nativeTab = "server-b";
            window.__noxa.state.activeTabID = "server-b";
            window.__noxaFiles.resetServerView();
            window.__noxaFiles.onChannelChanged();
            f.paths = ["C:\\fixture\\first.txt", "C:\\fixture\\second.txt"];
        });
        await page.locator(".fb-upload").click();
        await expect.poll(() => page.evaluate(() => window.__transferScope.polls.length)).toBe(2);
        await page.evaluate(() => window.__transferScope.polls[0]());
        expect(await page.evaluate(() => window.__transferScope.calls.length)).toBe(2);
        await page.locator("#tab-transfers").click();
        await expect(page.locator(".tr-list")).not.toContainText("old-secret.txt");
        await page.evaluate(() => {
            const f = window.__transferScope;
            for (const cb of window.__events.ft_progress || []) cb({ id: f.calls[1][2], direction: "upload", name: "first.txt", status: "done" });
            f.polls[1]();
        });
        await expect.poll(() => page.evaluate(() => window.__transferScope.calls.length)).toBe(3);
        expect(await page.evaluate(() => window.__transferScope.calls[2][5])).toBe("C:\\fixture\\second.txt");
    });
});

test("file browser filters names and sorts columns without refetching @a11y", async ({ page }, testInfo) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        window.__noxa.state.myChannelID = 42;
        window.__fileListResponse = {
            folders: ["Reports", "Archive"],
            entries: [
                { name: "report10.txt", size: 2, uploaded_at: "2026-09-13T12:00:00Z" },
                { name: "Alpha.txt", size: 100, uploaded_at: "2026-09-15T12:00:00Z" },
                { name: "report2.txt", size: 30, uploaded_at: "2026-09-14T12:00:00Z" },
            ],
        };
    });
    await page.locator("#tab-files").click();
    const names = page.locator(".fb-filename");
    await expect(names).toHaveText(["Alpha.txt", "report2.txt", "report10.txt"]);
    const requests = await page.evaluate(() => window.__calls.FileListForTab);
    const filter = page.getByRole("searchbox", { name: "Filter files by name" });
    await filter.fill(" REPORT ");
    await expect(names).toHaveText(["report2.txt", "report10.txt"]);
    await expect(page.locator(".fb-folder-link")).toHaveText(["Reports/"]);
    await filter.fill("missing");
    await expect(page.locator(".fb-list")).toContainText("No matching files or folders");
    await filter.press("Escape");
    await expect(names).toHaveCount(3);
    const size = page.getByRole("button", { name: "Sort by size", exact: true });
    await size.click();
    await expect(names).toHaveText(["report10.txt", "report2.txt", "Alpha.txt"]);
    await expect(size).toBeFocused();
    await expect(size.locator("..")).toHaveAttribute("aria-sort", "ascending");
    await size.press("Enter");
    await expect(names).toHaveText(["Alpha.txt", "report2.txt", "report10.txt"]);
    await expect(size.locator("..")).toHaveAttribute("aria-sort", "descending");
    const date = page.getByRole("button", { name: "Sort by date", exact: true });
    await date.click();
    await expect(names).toHaveText(["report10.txt", "report2.txt", "Alpha.txt"]);
    await date.click();
    await expect(names).toHaveText(["Alpha.txt", "report2.txt", "report10.txt"]);
    await page.getByRole("button", { name: "Sort by name", exact: true }).click();
    await page.getByRole("button", { name: "Sort by name", exact: true }).click();
    await expect(names).toHaveText(["report10.txt", "report2.txt", "Alpha.txt"]);
    await expect(page.locator(".fb-folder-link")).toHaveText(["Reports/", "Archive/"]);
    expect(await page.evaluate(() => window.__calls.FileListForTab)).toBe(requests);
    await auditAccessibility(page, "file filtering and sorting");
    await page.screenshot({ path: testInfo.outputPath("file-controls.png") });
    await page.evaluate(() => {
        window.__fileListGate = new Promise(resolve => { window.__finishFileList = resolve; });
    });
    await page.getByRole("button", { name: "Refresh files", exact: true }).click();
    await filter.fill("report");
    await expect(names).toHaveCount(0);
    await expect(page.locator(".fb-list")).toContainText("Loading channel files");
    await page.evaluate(() => window.__finishFileList());
    await expect(names).toHaveText(["report10.txt", "report2.txt"]);
    await filter.fill("missing");
    await page.evaluate(() => {
        window.__fileListResponse = { entries: [], folders: [] };
    });
    await page.getByRole("button", { name: "Refresh files", exact: true }).click();
    await expect(page.locator(".fb-list")).toContainText("No matching files or folders");
    await filter.press("Escape");
    await expect(page.locator(".fb-list")).toContainText("Empty folder");
});

test("debug console preserves older entries while new frames arrive and jumps to latest", async ({ page }, testInfo) => {
    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        window.__noxaMeta.openDebugConsole();
        for (let i = 0; i < 200; i++) {
            for (const cb of window.__events.debug_frame) cb({ dir: "in", type: i % 2 ? "odd" : "even", payload: `frame ${i}` });
        }
    });
    const list = page.locator(".dbg-list");
    await expect(page.locator(".dbg-row").last()).toContainText("frame 199");
    await list.evaluate(el => { el.scrollTop = 500; });
    const anchor = await list.evaluate(el => {
        const row = [...el.children].find(row => row.getBoundingClientRect().bottom > el.getBoundingClientRect().top);
        return { text: row.querySelector(".dbg-payload").textContent, top: row.getBoundingClientRect().top };
    });
    await page.evaluate(() => {
        for (let i = 200; i < 210; i++) {
            for (const cb of window.__events.debug_frame) cb({ dir: "in", type: "sample", payload: `frame ${i}` });
        }
    });
    const row = page.locator(".dbg-row").filter({ has: page.locator(".dbg-payload", { hasText: new RegExp(`^${anchor.text}$`) }) });
    expect(Math.abs(await row.evaluate(el => el.getBoundingClientRect().top) - anchor.top)).toBeLessThan(2);
    await expect(page.locator(".dbg-row")).toHaveCount(200);
    const jump = page.getByRole("button", { name: "Jump to latest" });
    await expect(jump).toBeVisible();
    await page.screenshot({ path: testInfo.outputPath("debug-reading.png") });
    await jump.click();
    await expect(jump).toBeHidden();
    await page.evaluate(() => {
        for (const cb of window.__events.debug_frame) cb({ dir: "out", type: "sample", payload: "newest frame" });
    });
    await expect(page.locator(".dbg-row").last()).toContainText("newest frame");
    expect(await list.evaluate(el => el.scrollHeight - el.clientHeight - el.scrollTop)).toBeLessThan(3);
    await page.locator(".dbg-filter").fill("even");
    await expect(page.locator(".dbg-row b").first()).toHaveText("even");
    await page.locator(".dbg-filter").fill("");
    await expect(page.locator(".dbg-payload").first()).toHaveText("frame 11");
    await expect(page.locator(".dbg-payload").nth(1)).toHaveText("frame 12");
    await expect(page.locator(".dbg-payload").last()).toHaveText("newest frame");
    await page.keyboard.press("Escape");
    await expect(page.locator(".debug-console")).toHaveCount(0);
});

test("shows the files toolbar and opens the upload picker", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        window.__noxa.state.myChannelID = 42;
        window.__noxa.state.channels = [{ ChannelID: 42, Name: "Uploads" }];
    });

    await page.locator("#tab-files").click();
    await expect(page.locator("#files-pane")).toBeVisible();
    await expect(page.locator("#files-pane .fb-upload")).toBeVisible();
    await page.locator("#files-pane .fb-upload").click();
    await expect.poll(() => page.evaluate(() => window.__calls.PickUploadPaths || 0)).toBe(1);
});

test("saves chat attachments through the native bridge without a DOM data URL", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        const state = window.__noxa.state;
        state.myChannelID = 42;
        state.channels = [{ ChannelID: 42, Name: "Uploads" }];
        window.__saveAttachmentResult = "C:\\Downloads\\report.txt";
        let release;
        window.__saveAttachmentGate = new Promise((resolve) => { release = resolve; });
        window.__releaseSaveAttachment = release;
        for (const callback of window.__events.event || []) callback(JSON.stringify({
            type: "chat",
            data: {
                id: 99, channel_id: 42, from: "Alice", from_unique_id: "user-a",
                text: "[file:blob.vcx#dGVzdC1rZXk=#report.txt]",
            },
        }));
    });

    const chip = page.getByRole("button", { name: "📎 report.txt" });
    await expect(chip).toBeVisible();
    await chip.click();
    await expect(chip).toBeDisabled();
    await expect.poll(() => page.evaluate(() => window.__callArgs.SaveChatAttachmentForTab?.map(args => args.slice(1)))).toEqual([
        [42, "blob.vcx", "dGVzdC1rZXk=", "report.txt"],
    ]);
    await expect(page.locator('a[href^="data:application/octet-stream;base64,"]')).toHaveCount(0);

    await page.evaluate(() => {
        window.__releaseSaveAttachment();
        window.__saveAttachmentGate = null;
    });
    await expect(chip).toBeEnabled();
});

test("opens About links externally once and ignores a late rejected version lookup", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        let release;
        window.__clientVersionGate = new Promise((resolve) => { release = resolve; });
        window.__releaseClientVersion = release;
        window.__clientVersionReject = true;
        window.__browserOpenThrow = true;
        window.__unhandled = [];
        window.addEventListener("unhandledrejection", (event) => window.__unhandled.push(String(event.reason)));
    });
    const help = page.locator("#menubar > .menu-item").filter({ hasText: /^Help/ });
    await help.click();
    await page.getByRole("menuitem", { name: /About noXa/ }).click();
    const about = page.locator(".dlg-overlay", { hasText: "About noXa" });
    const project = about.getByRole("link", { name: "project" });
    await expect(project).toHaveAttribute("href", "https://github.com/arumes31/noxa");
    await expect(project).toHaveAttribute("rel", "noopener noreferrer");
    await expect(about.getByRole("link", { name: "issues" })).toHaveAttribute("href", "https://github.com/arumes31/noxa/issues");

    const before = page.url();
    await page.evaluate(() => {
        const overlay = [...document.querySelectorAll(".dlg-overlay")]
            .find((el) => el.textContent.includes("About noXa"));
        overlay.querySelector(".about-links a").click();
        overlay.querySelector(".dlg-ok").click();
        window.__releaseClientVersion();
    });
    await expect.poll(() => page.evaluate(() => window.__browserURLs)).toEqual(["https://github.com/arumes31/noxa"]);
    expect(page.url()).toBe(before);
    await expect(about).toHaveCount(0);
    await page.waitForTimeout(0);
    expect(await page.evaluate(() => window.__unhandled)).toEqual([]);
});

test("retains failed chat drafts and only retries attachments that were not sent", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        window.__noxa.state.myChannelID = 42;
        window.__noxa.state.channels = [{ ChannelID: 42, Name: "Uploads" }];
    });
    await page.evaluate(() => {
        window.__noxaChat.addChat({
            id: 701, channel_id: 42, from: "Bob", from_unique_id: "user-b", text: "reply parent",
        });
    });
    await page.locator("#chat-log .msg").hover();
    await expect(page.locator("button[title=reply]")).toBeVisible();
    await page.locator("button[title=reply]").click();
    await page.locator("#chat-file").setInputFiles({ name: "once.txt", mimeType: "text/plain", buffer: Buffer.from("once") });
    await expect(page.locator("#file-preview-row")).not.toHaveClass(/hidden/);
    await page.locator("#chat-text").fill("draft reply");

    await page.evaluate(() => { window.__sendChatReplyResult = "slow mode"; });
    await page.locator("#chat-send").click();
    await expect(page.locator("#chat-text")).toHaveValue("draft reply");
    await expect(page.locator("#reply-bar")).not.toHaveClass(/hidden/);
    await expect(page.locator("#file-preview-row")).toHaveClass(/hidden/);
    expect(await page.evaluate(() => ({ upload: window.__calls.UploadChatAttachmentForTab, send: window.__calls.SendChatForTab }))).toEqual({ upload: 1, send: 1 });

    await page.evaluate(() => {
        window.__sendChatReplyResult = "";
        window.__sendChatReplyReject = true;
    });
    await page.locator("#chat-send").click();
    await expect(page.locator("#chat-text")).toHaveValue("draft reply");
    await expect(page.locator("#reply-bar")).not.toHaveClass(/hidden/);

    await page.evaluate(() => { window.__sendChatReplyReject = false; });
    await page.locator("#chat-send").click();
    await expect(page.locator("#chat-text")).toHaveValue("");
    await expect(page.locator("#reply-bar")).toHaveClass(/hidden/);
    expect(await page.evaluate(() => ({
        upload: window.__calls.UploadChatAttachmentForTab,
        send: window.__calls.SendChatForTab,
        reply: window.__calls.SendChatReplyForTab,
    }))).toEqual({ upload: 1, send: 1, reply: 3 });
});

test("requeues only upload and attachment-token failures without resending successful attachments", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        const state = window.__noxa.state;
        state.myChannelID = 42;
        state.channels = [{ ChannelID: 42, Name: "Uploads" }];
        window.__noxaChat.addChat({ id: 704, channel_id: 42, from: "Bob", text: "reply parent" });
        window.__uploadAttempts = {};
        window.__attachmentSendAttempts = {};
        window.__uploadAttachmentHandler = (_channelID, name) => {
            const count = (window.__uploadAttempts[name] || 0) + 1;
            window.__uploadAttempts[name] = count;
            if (name.startsWith("upload-once_") && count === 1) throw new Error("upload unavailable");
            return `[file:${name}.vcx#dGVzdA==#${name}]`;
        };
        window.__sendChatHandler = (_scope, _target, token) => {
            const name = token.match(/#([^#]+)\]$/)?.[1] || token;
            const count = (window.__attachmentSendAttempts[name] || 0) + 1;
            window.__attachmentSendAttempts[name] = count;
            return name.startsWith("token-once_") && count === 1 ? "token send failed" : "";
        };
        window.__sendChatReplyResult = "reply unavailable";
    });
    await page.locator("#chat-log .msg").hover();
    await page.locator("button[title=reply]").click();
    await page.locator("#chat-file").setInputFiles([
        { name: "upload-once.txt", mimeType: "text/plain", buffer: Buffer.from("upload") },
        { name: "token-once.txt", mimeType: "text/plain", buffer: Buffer.from("token") },
        { name: "successful.txt", mimeType: "text/plain", buffer: Buffer.from("success") },
    ]);
    await expect(page.locator("#file-preview-row .file-preview")).toHaveCount(3);
    await page.locator("#chat-text").fill("keep this reply draft");

    await page.locator("#chat-send").click();
    await expect(page.locator("#chat-text")).toHaveValue("keep this reply draft");
    await expect(page.locator("#reply-bar")).not.toHaveClass(/hidden/);
    await expect(page.locator("#file-preview-row .file-preview")).toHaveCount(2);
    await expect(page.locator("#file-preview-row")).toContainText(/upload-once_/);
    await expect(page.locator("#file-preview-row")).toContainText(/token-once_/);
    await expect(page.locator("#file-preview-row")).not.toContainText(/successful_/);

    await page.locator("#chat-send").click();
    await expect(page.locator("#chat-text")).toHaveValue("keep this reply draft");
    await expect(page.locator("#reply-bar")).not.toHaveClass(/hidden/);
    await expect(page.locator("#file-preview-row")).toHaveClass(/hidden/);

    expect(await page.evaluate(() => ({
        uploads: Object.entries(window.__uploadAttempts).map(([name, count]) => [name.replace(/_[^_]+_[^.]+(?=\.txt$)/, ""), count]).sort(),
        sends: Object.entries(window.__attachmentSendAttempts).map(([name, count]) => [name.replace(/_[^_]+_[^.]+(?=\.txt$)/, ""), count]).sort(),
    }))).toEqual({
        uploads: [["successful.txt", 1], ["token-once.txt", 2], ["upload-once.txt", 2]],
        sends: [["successful.txt", 1], ["token-once.txt", 2], ["upload-once.txt", 1]],
    });

    await page.evaluate(() => { window.__sendChatReplyResult = ""; });
    await page.locator("#chat-send").click();
    await expect(page.locator("#chat-text")).toHaveValue("");
    await expect(page.locator("#reply-bar")).toHaveClass(/hidden/);
    expect(await page.evaluate(() => Object.entries(window.__uploadAttempts)
        .map(([name, count]) => [name.replace(/_[^_]+_[^.]+(?=\.txt$)/, ""), count]).sort()))
        .toEqual([["successful.txt", 1], ["token-once.txt", 2], ["upload-once.txt", 2]]);
});

test("discards stale inline attachment previews and configures lazy image and video media", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        const state = window.__noxa.state;
        state.myChannelID = 42;
        state.channels = [{ ChannelID: 42, Name: "Uploads" }];
        window.__noxaChat.onMyChannelChanged();
        let release;
        window.__attachmentGate = new Promise((resolve) => { release = resolve; });
        window.__releaseAttachment = release;
        window.__attachmentData = "aGVsbG8=";
        window.__noxaChat.addChat({ id: 702, channel_id: 42, from: "Bob", text: "[file:stale.vcx#dGVzdA==#stale.png]" });
    });
    await page.evaluate(() => {
        window.__noxa.state.serverGeneration++;
        window.__releaseAttachment();
    });
    await page.waitForTimeout(0);
    await expect(page.locator(".msg-file img, .msg-file video")).toHaveCount(0);
    await expect(page.locator("[src^='data:image/'], [src^='data:video/']")).toHaveCount(0);

    await page.evaluate(() => {
        window.__attachmentGate = null;
        window.__noxaChat.addChat({ id: 703, channel_id: 42, from: "Bob", text: "[file:photo.vcx#dGVzdA==#photo.png] [file:clip.vcx#dGVzdA==#clip.webm]" });
    });
    const image = page.locator(".msg-file img.msg-img");
    const video = page.locator(".msg-file video.msg-video");
    await expect(image).toHaveAttribute("loading", "lazy");
    await expect(image).toHaveAttribute("decoding", "async");
    await expect(video).toHaveAttribute("preload", "none");
    await expect(video).not.toHaveAttribute("loading");
    await image.click();
    const imageLightbox = page.locator(".lightbox img");
    await expect(imageLightbox).toBeVisible();
    await expect(imageLightbox).toHaveAttribute("loading", "lazy");
    await expect(imageLightbox).toHaveAttribute("decoding", "async");
    await page.locator(".lightbox").click({ position: { x: 1, y: 1 } });
    await expect(page.locator(".lightbox")).toHaveCount(0);
    await page.locator(".msg-file:has(video) .media-zoom").click();
    await expect(page.locator(".lightbox video[controls][autoplay]")).toBeVisible();
});

test("does not construct stale attachment data URLs after channel or view changes", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        const state = window.__noxa.state;
        state.myChannelID = 42;
        state.channels = [
            { ChannelID: 42, Name: "Original" },
            { ChannelID: 43, Name: "Moved" },
        ];
        window.__attachmentDataURLWrites = [];
        window.__attachmentRequests = {};
        const instrument = (prototype) => {
            const descriptor = Object.getOwnPropertyDescriptor(prototype, "src");
            Object.defineProperty(prototype, "src", {
                configurable: true,
                enumerable: descriptor.enumerable,
                get: descriptor.get,
                set(value) {
                    if (String(value).startsWith("data:")) window.__attachmentDataURLWrites.push(String(value));
                    return descriptor.set.call(this, value);
                },
            });
            return descriptor;
        };
        const imageSrc = instrument(HTMLImageElement.prototype);
        const videoSrc = instrument(HTMLMediaElement.prototype);
        window.__restoreAttachmentSrc = () => {
            Object.defineProperty(HTMLImageElement.prototype, "src", imageSrc);
            Object.defineProperty(HTMLMediaElement.prototype, "src", videoSrc);
        };
        window.__downloadAttachmentHandler = (_channelID, storage) => new Promise((resolve, reject) => {
            window.__attachmentRequests[storage] = { resolve, reject };
        });
        window.__noxaChat.addChat({
            id: 705, channel_id: 42, from: "Bob", text: "[file:stale-resolve.vcx#dGVzdA==#photo.png]",
        });
    });
    await expect.poll(() => page.evaluate(() => Object.keys(window.__attachmentRequests))).toEqual(["stale-resolve.vcx"]);
    await page.evaluate(() => {
        window.__noxa.state.myChannelID = 43;
        window.__attachmentRequests["stale-resolve.vcx"].resolve("aGVsbG8=");
    });
    await page.waitForTimeout(0);
    expect(await page.evaluate(() => window.__attachmentDataURLWrites)).toEqual([]);

    await page.evaluate(() => {
        window.__noxaChat.addChat({
            id: 706, channel_id: 43, from: "Bob", text: "[file:stale-reject.vcx#dGVzdA==#photo.png]",
        });
    });
    await expect.poll(() => page.evaluate(() => Object.keys(window.__attachmentRequests).sort())).toEqual([
        "stale-reject.vcx", "stale-resolve.vcx",
    ]);
    await page.evaluate(() => {
        window.__noxaChat.openPM("user-b", "Bob");
        window.__attachmentRequests["stale-reject.vcx"].reject(new Error("download rejected"));
    });
    await page.waitForTimeout(0);
    expect(await page.evaluate(() => window.__attachmentDataURLWrites)).toEqual([]);
    expect(await page.evaluate(() => document.querySelectorAll(".msg-file img, .msg-file video").length)).toBe(0);
    await page.evaluate(() => {
        window.__restoreAttachmentSrc();
        delete window.__downloadAttachmentHandler;
    });
});

test("contains disconnect and ICE-candidate rejections and reports ICE exhaustion once per outage", async ({ page }) => {
    await page.evaluate(async () => {
        window.__unhandled = [];
        window.addEventListener("unhandledrejection", (event) => window.__unhandled.push(String(event.reason)));
        window.__noxa.state.settings.notify_connection = true;
        document.getElementById("conn-pill").classList.add("up");
        window.__disconnectReject = true;
        await window.__noxa.disconnect();

        const originalSetTimeout = window.setTimeout;
        const originalClearTimeout = window.clearTimeout;
        const iceTimers = [];
        const delays = [];
        window.setTimeout = (callback, delay, ...args) => {
            if ([1000, 2000, 5000, 15000].includes(delay)) {
                const timer = { delay, ran: false, cancelled: false, callback: () => callback(...args) };
                delays.push(delay);
                iceTimers.push(timer);
                return timer;
            }
            return originalSetTimeout(callback, delay, ...args);
        };
        window.clearTimeout = (timer) => {
            if (timer && iceTimers.includes(timer)) {
                timer.cancelled = true;
                return;
            }
            return originalClearTimeout(timer);
        };
        window.__restoreTimeout = () => {
            window.setTimeout = originalSetTimeout;
            window.clearTimeout = originalClearTimeout;
        };
        const runNextICETimer = async () => {
            const timer = iceTimers.find((entry) => !entry.ran && !entry.cancelled);
            if (!timer) throw new Error("expected an ICE retry timer");
            timer.ran = true;
            await timer.callback();
        };
        const audio = new AudioContext();
        window.__iceAudio = audio;
        const stream = audio.createMediaStreamDestination().stream;
        navigator.mediaDevices.getUserMedia = async () => stream;
        class FakePeerConnection {
            constructor() {
                this.senders = [];
                this.transceivers = [];
                this.iceConnectionState = "connected";
            }
            addTransceiver(track, options = {}) {
                const sender = {
                    track,
                    getParameters: () => ({ encodings: [{}] }),
                    setParameters: async () => {},
                    replaceTrack: async (next) => { sender.track = next; },
                };
                const transceiver = { sender, receiver: { track: null }, direction: options.direction || "sendrecv" };
                this.senders.push(sender);
                this.transceivers.push(transceiver);
                return transceiver;
            }
            getSenders() { return this.senders; }
            getTransceivers() { return this.transceivers; }
            async createOffer() { return { type: "offer", sdp: "ice-test" }; }
            async setLocalDescription() {}
            async setRemoteDescription() {}
            close() { this.iceConnectionState = "closed"; }
        }
        window.RTCPeerConnection = FakePeerConnection;
        const state = window.__noxa.state;
        state.myClientID = "client-a";
        state.myChannelID = 42;
        state.channels = [{ ChannelID: 42, Name: "Lobby" }];
        await window.__noxa.ensureVoiceForChannel();
        const oldPC = state.pc;
        window.__noxa.resetVoiceSession();
        await window.__noxa.ensureVoiceForChannel();
        const pc = state.pc;
        window.__iceSysMessagesBeforeRetries = document.querySelectorAll("#chat-log .msg.sys").length;
        window.__sendICECandidateReject = true;
        pc.onicecandidate({ candidate: { candidate: "candidate", sdpMid: "0", sdpMLineIndex: 0 } });
        pc.iceConnectionState = "failed";
        pc.oniceconnectionstatechange();
        const pendingBeforeOldEvents = iceTimers.filter((entry) => !entry.ran && !entry.cancelled).length;
        oldPC.iceConnectionState = "connected";
        oldPC.oniceconnectionstatechange();
        oldPC.iceConnectionState = "completed";
        oldPC.oniceconnectionstatechange();
        window.__oldPeerIsolation = {
            pendingBeforeOldEvents,
            pendingAfterOldEvents: iceTimers.filter((entry) => !entry.ran && !entry.cancelled).length,
        };
        for (let i = 0; i < 4; i++) await runNextICETimer();
        window.__terminalToastsBeforeRecovery = [...document.querySelectorAll("#toasts .toast")]
            .filter((toast) => toast.textContent.includes("Voice connection unstable")).length;
        pc.iceConnectionState = "connected";
        pc.oniceconnectionstatechange();
        pc.iceConnectionState = "failed";
        pc.oniceconnectionstatechange();
        for (let i = 0; i < 4; i++) await runNextICETimer();
        window.__iceRetryDelays = delays;
    });
    expect(await page.evaluate(() => window.__calls.DisconnectTab)).toBe(1);
    expect(await page.evaluate(() => window.__calls.SendICECandidateForTab)).toBe(1);
    expect(await page.evaluate(() => window.__oldPeerIsolation)).toEqual({ pendingBeforeOldEvents: 1, pendingAfterOldEvents: 1 });
    expect(await page.evaluate(() => window.__iceRetryDelays)).toEqual([1000, 2000, 5000, 15000, 1000, 2000, 5000, 15000]);
    expect(await page.evaluate(() => window.__terminalToastsBeforeRecovery)).toBe(1);
    expect(await page.locator("#toasts .toast", { hasText: "Voice connection unstable" }).count()).toBe(2);
    expect(await page.locator("#toasts .toast", { hasText: "disconnect failed" }).count()).toBe(1);
    expect(await page.locator("#chat-log .msg.sys").count()).toBe(
        await page.evaluate(() => window.__iceSysMessagesBeforeRetries),
    );
    expect(await page.evaluate(() => window.__unhandled)).toEqual([]);
    await page.evaluate(() => {
        window.__restoreTimeout();
        window.__iceAudio?.close();
    });
});

test("does not let an old checksum restoration timer mutate a reset file view", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        window.__noxa.state.myChannelID = 42;
        window.__noxa.state.channels = [{ ChannelID: 42, Name: "Uploads" }];
        window.__fileListResponse = {
            entries: [{ name: "report.txt", size: 4, uploader: "user-a", uploaded_at: 1, sha256: "0123456789abcdef" }],
            folders: [], used_bytes: 4, quota_bytes: 100,
        };
    });
    await page.locator("#tab-files").click();
    await page.locator(".fb-action-menu > summary").click();
    const verify = page.getByRole("button", { name: "Verify checksum", exact: true });
    await expect(verify).toBeVisible();
    await verify.click();
    const oldSHA = await page.evaluate(() => {
        const sha = document.querySelector(".fb-sha");
        window.__oldChecksumCell = sha;
        return sha.textContent;
    });
    expect(oldSHA).toBe("✓ ok");
    await page.evaluate(() => window.__noxaFiles.resetServerView());
    await page.waitForTimeout(4100);
    expect(await page.evaluate(() => window.__oldChecksumCell.textContent)).toBe("✓ ok");
});

test("keeps details contextual and opens it when a user is selected", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
    });
    await expect(page.locator("body")).toHaveClass(/details-collapsed/);
    await page.evaluate(() => {
        for (const cb of window.__events.snapshot || []) cb(JSON.stringify({
            root_channels: [{
                ChannelID: 1,
                ParentID: 0,
                Name: "Lobby",
                clients: [{
                    client_id: "client-a",
                    unique_id: "user-a",
                    nickname: "Alice",
                    channel_id: 1,
                    is_speaking: false,
                }],
                children: [],
            }],
        }));
    });
    await page.locator('.client[data-clid="client-a"]').click();
    await expect(page.locator("body")).not.toHaveClass(/details-collapsed/);
    await expect(page.locator("#client-card .card-nick")).toHaveText("Alice");
    await page.getByRole("button", { name: "Close details" }).click();
    await expect(page.locator("body")).toHaveClass(/details-collapsed/);
    await expect(page.locator("#details-toggle")).toBeVisible();
});

test("nests connected members below channels and offers them as direct-message targets", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        window.__noxa.state.myClientID = "client-a";
        window.__noxa.state.myChannelID = 1;
        for (const cb of window.__events.snapshot || []) cb(JSON.stringify({
            root_channels: [
                {
                    ChannelID: 1, ParentID: 0, Name: "Lobby",
                    clients: [
                        { client_id: "client-a", unique_id: "user-a", nickname: "Alice", channel_id: 1, is_speaking: false },
                        { client_id: "client-b", unique_id: "user-b", nickname: "Bob", channel_id: 1, is_speaking: true },
                    ],
                    children: [],
                },
                {
                    ChannelID: 2, ParentID: 0, Name: "Workshop",
                    clients: [
                        { client_id: "client-c", unique_id: "user-c", nickname: "Carol", channel_id: 2, is_speaking: true },
                    ],
                    children: [],
                },
            ],
        }));
    });

    const lobby = page.locator('.channel-node:has(> .channel[data-chid="1"])');
    await expect(lobby.locator(':scope > .channel-members > .client')).toHaveCount(2);
    await expect(page.locator('.channel[data-chid="1"] .client')).toHaveCount(0);
    await expect(page.locator('.client[data-clid="client-b"] .client-voice-state')).toBeVisible();
    await expect(page.locator('.client[data-clid="client-c"] .client-voice-state')).toHaveCount(0);

    await page.locator("#chat-scope").selectOption("direct");
    await page.locator("#chat-target").focus();
    await expect(page.locator("#chat-target-options .target-option")).toHaveCount(2);
    await page.locator("#chat-target-options .target-option", { hasText: "Carol" }).click();
    await expect(page.locator("#chat-target")).toHaveValue("user-c");
});

test("uses the B3 console composition without losing responsive navigation", async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        window.__tabs = [{
            id: "tab-a", addr: "127.0.0.1:12333", nickname: "Test",
            active: true, connected: true, unread: 0, mentions: 0,
        }];
        for (const cb of window.__events.tab_update || []) cb(structuredClone(window.__tabs));
        window.__noxa.state.myClientID = "client-a";
        window.__noxa.state.myChannelID = 1;
        for (const cb of window.__events.snapshot || []) cb(JSON.stringify({
            root_channels: [{
                ChannelID: 1, ParentID: 0, Name: "Main Lounge",
                clients: [
                    { client_id: "client-a", unique_id: "user-a", nickname: "Daniel", channel_id: 1, is_speaking: false },
                    { client_id: "client-b", unique_id: "user-b", nickname: "Benedikt", channel_id: 1, is_speaking: true },
                ],
                children: [{ ChannelID: 2, ParentID: 1, Name: "Alpha Squad", clients: [], children: [] }],
            }],
        }));
    });

    const desktop = await page.evaluate(() => ({
        accent: getComputedStyle(document.documentElement).getPropertyValue("--accent").trim(),
        appColumns: getComputedStyle(document.getElementById("app")).gridTemplateColumns,
        railDirection: getComputedStyle(document.getElementById("server-tabs")).flexDirection,
        texture: getComputedStyle(document.getElementById("center"), "::before").backgroundImage,
    }));
    expect(desktop.accent).toBe("#4ad8ed");
    expect(desktop.appColumns).toBe("1280px");
    expect(desktop.railDirection).toBe("row");
    expect(desktop.texture).toBe("none");
    const mainLounge = page.locator('.channel-node:has(> .channel[data-chid="1"])');
    await expect(mainLounge.locator(":scope > .channel-children")).toHaveAttribute("role", "group");
    await expect(mainLounge.locator(":scope > .channel-children")).toHaveAttribute("aria-label", "Main Lounge subchannels");
    await expect(page.locator("#server-tabs .srv-tab.active")).toBeVisible();
    await page.locator('.client[data-clid="client-a"]').click();

    await page.setViewportSize({ width: 700, height: 800 });
    await expect.poll(() => page.evaluate(
        () => getComputedStyle(document.getElementById("server-tabs")).flexDirection,
    )).toBe("row");
    await expect(page.locator("#center")).toBeVisible();
    await page.getByRole("button", { name: "Close details", exact: true }).click();
    await page.getByRole("button", { name: "Show channels", exact: true }).click();
    await expect(page.locator('.channel[data-chid="1"]')).toBeVisible();
});

test("exposes named landmarks, controls, live regions, and a visible focus ring", async ({ page }) => {
    await expect(page.getByRole("dialog", { name: "noxa" })).toBeVisible();
    await expect(page.getByRole("textbox", { name: /^server$/i })).toBeVisible();
    await expect(page.getByRole("button", { name: "Connect" })).toBeVisible();
    await expect(page.locator("#login-error")).toHaveAttribute("role", "alert");
    await expect(page.locator("#toasts")).toHaveAttribute("aria-label", "Notifications");
    await expect(page.locator("#voice-status")).toHaveAttribute("role", "status");
    await expect(page.locator("#chat-log")).toHaveAttribute("aria-live", "off");
    await expect(page.locator("#chat-announcer")).toHaveAttribute("aria-live", "polite");
    await expect(page.locator("#alert-announcer")).toHaveAttribute("aria-live", "assertive");
    await expect(page.locator("#conn-pill")).not.toHaveAttribute("aria-live", /.+/);

    await page.locator("#login-addr").focus();
    await expect.poll(() => page.locator("#login-addr").evaluate((el) => {
        const style = getComputedStyle(el);
        return `${style.outlineStyle} ${style.outlineWidth}`;
    })).toBe("solid 2px");

    await expect(page.locator(".skip-link")).toBeHidden();
    await page.evaluate(() => window.__noxa.showWorkspace(false));
    await page.locator(".skip-link").focus();
    await expect(page.locator(".skip-link")).toBeVisible();
    await page.evaluate(() => {
        const message = JSON.stringify({
            type: "chat",
            data: { id: 101, from: "Bob", from_unique_id: "user-b", text: "hello from Bob", channel_id: 0 },
        });
        for (const cb of window.__events.event || []) cb(message);
    });
    await expect(page.locator("#chat-announcer")).toHaveText("Bob: hello from Bob");
    await expect(page.locator("#alert-announcer")).toBeEmpty();
    await expect(page.locator("#toasts [role=status], #toasts [role=alert]")).toHaveCount(0);
    await expect(page.locator("#toasts .toast").first()).toHaveAttribute("aria-hidden", "true");
    await page.evaluate(() => { document.getElementById("chat-log").innerHTML = "<p>rerendered history</p>"; });
    await expect(page.locator("#chat-announcer")).toHaveText("Bob: hello from Bob");
});

test("@a11y audits primary login, workspace, settings, and role-dialog states", async ({ page }) => {
    await auditAccessibility(page, "login");

    await page.evaluate(() => window.__noxa.showWorkspace(false));
    await auditAccessibility(page, "connected workspace");

    await page.evaluate(() => window.__noxa.openSettings("application"));
    await expect(page.getByRole("dialog", { name: "Settings" })).toBeVisible();
    await auditAccessibility(page, "settings dialog");
    await page.keyboard.press("Escape");

    await page.evaluate(async () => {
        const state = window.__noxa.state;
        state.activeTabID = "server-a";
        state.authorizationModel = "roles-v1";
        const app = window.go.main.App;
        window.go.main.App = new Proxy(app, { get(target, key) {
            if (key === "RoleStateForTab") return async () => ({
                actor_id: 1,
                policy: { revision: 1, owner_id: 1, everyone_id: 10, roles: [
                    { id: 10, name: "@everyone", position: 0, permissions: [] },
                ], members: [], channels: [] },
                capabilities: [{ key: "view_channel", group: "access", en: "View channels", de: "Kanäle anzeigen" }],
                manageable_role_ids: [10], grantable_capabilities: ["view_channel"],
            });
            return target[key];
        } });
        const { openRolesManager } = await import("/src/roles-ui.js");
        openRolesManager();
    });
    await expect(page.getByRole("dialog", { name: "Roles", exact: true })).toBeVisible();
    await expect(page.locator(".role-form")).toBeVisible();
    await auditAccessibility(page, "roles dialog");
});

test("does not delete a same-named file in a new channel after user_moved during confirmation", async ({ page }) => {
    await page.evaluate(() => {
        const state = window.__noxa.state;
        window.__noxa.showWorkspace(false);
        state.myClientID = "client-a";
        state.myChannelID = 1;
        state.channels = [
            { ChannelID: 1, ParentID: 0, Name: "Original" },
            { ChannelID: 2, ParentID: 0, Name: "New channel" },
        ];
        state.clients = [{ client_id: "client-a", unique_id: "user-a", nickname: "Alice", channel_id: 1 }];
        // Both channels deliberately contain this name. A stale confirmation
        // must not turn the old row into a delete for the new channel.
        window.__fileListResponse = {
            entries: [{ name: "same-name.txt", size: 1, uploaded_at: 0, uploader: "user-a", sha256: "abc" }],
            folders: [], used_bytes: 1, quota_bytes: 0,
        };
    });
    await page.locator("#tab-files").click();
    await expect(page.locator("#files-pane .fb-filename")).toHaveText("same-name.txt");
    await page.locator(".fb-action-menu > summary").click();
    await page.locator('#files-pane .fb-actions button[title="delete"]').click();
    await expect(page.getByRole("dialog", { name: "Delete file?" })).toBeVisible();

    await page.evaluate(() => {
        const moved = JSON.stringify({ type: "user_moved", data: { client_id: "client-a", channel_id: 2 } });
        for (const callback of window.__events.event || []) callback(moved);
    });
    await page.getByRole("button", { name: "Delete file" }).click();
    await expect.poll(() => page.evaluate(() => window.__calls.FileDeleteForTab || 0)).toBe(0);
});

test("does not export a different channel after its passphrase dialog is left open", async ({ page }) => {
    await page.evaluate(() => {
        const state = window.__noxa.state;
        window.__noxa.showWorkspace(false);
        state.myClientID = "client-a";
        state.myChannelID = 1;
        state.channels = [
            { ChannelID: 1, ParentID: 0, Name: "Original" },
            { ChannelID: 2, ParentID: 0, Name: "New channel" },
        ];
        state.clients = [{ client_id: "client-a", unique_id: "user-a", nickname: "Alice", channel_id: 1 }];
    });
    await page.getByLabel("More channel actions", { exact: true }).click();
    await page.getByRole("button", { name: "Export history" }).click();
    await expect(page.getByRole("dialog", { name: "Export chat" })).toBeVisible();
    await page.locator('input[placeholder^="passphrase"]').fill("encrypted-export");
    await page.evaluate(() => {
        const moved = JSON.stringify({ type: "user_moved", data: { client_id: "client-a", channel_id: 2 } });
        for (const callback of window.__events.event || []) callback(moved);
    });
    await page.getByRole("button", { name: "Export", exact: true }).click();
    await expect.poll(() => page.evaluate(() => window.__calls.ChatExportHistoryForTab || 0)).toBe(0);
});

test("runtime boundaries ignore malformed payloads and never answer a stale offer", async ({ page }) => {
    const result = await page.evaluate(async () => {
        const errors = [];
        const onUnhandled = (event) => {
            errors.push(String(event.reason));
            event.preventDefault();
        };
        window.addEventListener("unhandledrejection", onUnhandled);
        const state = window.__noxa.state;
        state.myClientID = "client-a";
        state.clients = [];
        state.channels = [];
        state.serverGeneration = 40;
        const offerSDP = "a=ice-ufrag:current\r\na=ice-pwd:current-secret";
        let releaseOffer;
        const oldPeer = {
            remoteDescription: { sdp: offerSDP },
            ice: 0, remote: 0, answers: 0, local: 0,
            addIceCandidate: async () => { oldPeer.ice++; },
            setRemoteDescription: async () => {
                oldPeer.remote++;
                await new Promise((resolve) => { releaseOffer = resolve; });
            },
            createAnswer: async () => { oldPeer.answers++; return { type: "answer", sdp: "old-answer" }; },
            setLocalDescription: async () => { oldPeer.local++; },
        };
        state.pc = oldPeer;
        const emit = (name, payload) => {
            for (const callback of window.__events[name] || []) callback(payload);
        };
        for (const name of ["snapshot", "event", "ice", "offer"]) {
            emit(name, "{");
            emit(name, "null");
            emit(name, "[]");
        }
        emit("snapshot", JSON.stringify({ root_channels: [{ ChannelID: 7, ParentID: 0, Name: "Valid channel", clients: [], children: [] }] }));
        emit("event", JSON.stringify({ type: "user_joined", data: {
            client_id: "client-b", unique_id: "user-b", nickname: "Bob", channel_id: 7,
        } }));
        emit("ice", JSON.stringify({ candidate: "candidate", sdp_mid: "0", sdp_mline_index: 0 }));
        emit("offer", JSON.stringify({ sdp: offerSDP }));
        await new Promise((resolve) => setTimeout(resolve, 0));
        state.serverGeneration++;
        state.pc = { replacement: true };
        releaseOffer();
        await new Promise((resolve) => setTimeout(resolve, 0));
        const staleAnswers = window.__calls.WebRTCAnswerForTab || 0;
        for (const stage of ["remote", "answer", "local", "bridge"]) {
            state.serverGeneration++;
            window.__webRTCAnswerReject = stage === "bridge";
            state.pc = {
                remoteDescription: { sdp: offerSDP },
                addIceCandidate: async () => {},
                setRemoteDescription: async () => {
                    if (stage === "remote") throw new Error("remote rejected");
                },
                createAnswer: async () => {
                    if (stage === "answer") throw new Error("answer rejected");
                    return { type: "answer", sdp: "answer" };
                },
                setLocalDescription: async () => {
                    if (stage === "local") throw new Error("local rejected");
                },
            };
            emit("offer", JSON.stringify({ sdp: offerSDP }));
            await new Promise((resolve) => setTimeout(resolve, 0));
        }
        window.__webRTCAnswerReject = false;
        window.removeEventListener("unhandledrejection", onUnhandled);
        return {
            hasValidChannel: state.channels.some((channel) => channel.ChannelID === 7),
            hasValidEvent: state.clients.some((client) => client.client_id === "client-b"),
            ice: oldPeer.ice,
            remote: oldPeer.remote,
            local: oldPeer.local,
            staleAnswers,
            answers: window.__calls.WebRTCAnswerForTab || 0,
            errors,
        };
    });
    expect(result).toEqual({
        hasValidChannel: true,
        hasValidEvent: true,
        ice: 1,
        remote: 1,
        local: 0,
        staleAnswers: 0,
        answers: 1,
        errors: [],
    });
});

test("serializes live-region bursts without coalescing identical messages", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        window.__spokenAnnouncements = [];
        for (const id of ["chat-announcer", "alert-announcer"]) {
            const region = document.getElementById(id);
            new MutationObserver(() => {
                if (region.textContent) window.__spokenAnnouncements.push([id, region.textContent]);
            }).observe(region, { childList: true, characterData: true, subtree: true });
        }
        window.__noxa.announceLive("repeated update");
        window.__noxa.announceLive("repeated update");
        window.__noxa.announceLive("urgent update", "assertive");
    });

    await expect.poll(() => page.evaluate(() => window.__spokenAnnouncements)).toEqual([
        ["chat-announcer", "repeated update"],
        ["alert-announcer", "urgent update"],
        ["chat-announcer", "repeated update"],
    ]);
});

test("announces only eligible chat in the visible scope", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        const state = window.__noxa.state;
        state.myClientID = "client-a";
        state.myUniqueID = "user-a";
        state.myNickname = "Alice";
        state.myChannelID = 1;
        state.lastConnect = { addr: "voice.example:12333" };
        state.settings.chat_notification_level = "all";
        state.settings.notify_matrix = {};
        state.settings.channel_notify = {};
        window.__spokenChat = [];
        const region = document.getElementById("chat-announcer");
        new MutationObserver(() => {
            if (region.textContent) window.__spokenChat.push(region.textContent);
        }).observe(region, { childList: true, characterData: true, subtree: true });
        for (const cb of window.__events.snapshot || []) cb(JSON.stringify({
            root_channels: [
                { ChannelID: 1, ParentID: 0, Name: "Lobby", clients: [], children: [] },
                { ChannelID: 2, ParentID: 0, Name: "Elsewhere", clients: [], children: [] },
            ],
        }));
        const dispatch = (data) => {
            const message = JSON.stringify({ type: "chat", data });
            for (const callback of window.__events.event || []) callback(message);
        };
        dispatch({ id: 201, from: "Bob", from_unique_id: "user-b", text: "inactive scope", channel_id: 2 });
        state.settings.channel_notify["voice.example:12333#1"] = { muted: true };
        dispatch({ id: 202, from: "Bob", from_unique_id: "user-b", text: "muted", channel_id: 1 });
        delete state.settings.channel_notify["voice.example:12333#1"];
        state.settings.dnd_enabled = true;
        dispatch({ id: 203, from: "Bob", from_unique_id: "user-b", text: "dnd", channel_id: 1 });
        state.settings.dnd_enabled = false;
        state.settings.chat_notification_level = "direct";
        dispatch({ id: 204, from: "Bob", from_unique_id: "user-b", text: "category filtered", channel_id: 1 });
        state.settings.chat_notification_level = "all";
        state.settings.notify_matrix.channel_message = { toast: false, sound: false, flash: false, native: false };
        dispatch({ id: 205, from: "Bob", from_unique_id: "user-b", text: "matrix filtered", channel_id: 1 });
    });
    await page.waitForTimeout(450);
    expect(await page.evaluate(() => window.__spokenChat)).toEqual([]);

    await page.evaluate(() => {
        window.__noxa.state.settings.notify_matrix = {};
        const message = JSON.stringify({
            type: "chat",
            data: { id: 206, from: "Bob", from_unique_id: "user-b", text: "visible message", channel_id: 1 },
        });
        for (const callback of window.__events.event || []) callback(message);
    });
    await expect.poll(() => page.evaluate(() => window.__spokenChat)).toEqual(["Bob: visible message"]);
});

test("summarizes visible offline replay instead of announcing every message", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        window.__noxa.state.myUniqueID = "user-a";
        window.__noxa.state.myNickname = "Alice";
        window.__noxa.state.settings.notify_matrix = {};
        window.__noxa.state.settings.dnd_enabled = false;
        window.__noxaChat.openPM("user-b", "Bob");
        window.__offlineSpoken = [];
        const region = document.getElementById("chat-announcer");
        new MutationObserver(() => {
            if (region.textContent) window.__offlineSpoken.push(region.textContent);
        }).observe(region, { childList: true, characterData: true, subtree: true });
        const dispatch = (id, uid, from, text) => {
            const message = JSON.stringify({
                type: "chat",
                data: { id, from, from_unique_id: uid, text, e2e: true, offline: true, client_msg_id: `offline-${id}` },
            });
            for (const callback of window.__events.event || []) callback(message);
        };
        dispatch(301, "user-c", "Carol", "inactive offline message");
        dispatch(302, "user-b", "Bob", "one");
        dispatch(303, "user-b", "Bob", "two");
        dispatch(304, "user-b", "Bob", "three");
    });

    await expect.poll(() => page.evaluate(() => window.__offlineSpoken)).toEqual([
        "3 offline messages from Bob",
    ]);
});

test("summarizes a visible reconnect burst instead of announcing every message", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        const state = window.__noxa.state;
        state.myClientID = "client-a";
        state.myUniqueID = "user-a";
        state.myNickname = "Alice";
        state.myChannelID = 1;
        state.settings.chat_notification_level = "all";
        state.settings.notify_matrix = {};
        state.settings.channel_notify = {};
        state.settings.dnd_enabled = false;
        window.__reconnectSpoken = [];
        const region = document.getElementById("chat-announcer");
        new MutationObserver(() => {
            if (region.textContent) window.__reconnectSpoken.push(region.textContent);
        }).observe(region, { childList: true, characterData: true, subtree: true });
        for (const callback of window.__events.snapshot || []) callback(JSON.stringify({
            root_channels: [
                { ChannelID: 1, ParentID: 0, Name: "Lobby", clients: [], children: [] },
            ],
        }));
        window.__noxaChat.beginReconnectAnnouncementBatch(2000);
        for (let id = 401; id <= 403; id++) {
            const message = JSON.stringify({
                type: "chat",
                data: {
                    id,
                    from: "Bob",
                    from_unique_id: "user-b",
                    text: `replayed ${id}`,
                    channel_id: 1,
                },
            });
            for (const callback of window.__events.event || []) callback(message);
        }
    });

    await expect.poll(() => page.evaluate(() => window.__reconnectSpoken)).toEqual([
        "3 messages from Bob received after reconnect",
    ]);
});

test("cancels stale reconnect batches when switching server tabs", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        const state = window.__noxa.state;
        state.myClientID = "client-a";
        state.myUniqueID = "user-a";
        state.myNickname = "Alice";
        state.myChannelID = 1;
        state.settings.chat_notification_level = "all";
        state.settings.notify_matrix = {};
        state.settings.channel_notify = {};
        state.settings.dnd_enabled = false;
        window.__resetSpoken = [];
        const region = document.getElementById("chat-announcer");
        new MutationObserver(() => {
            if (region.textContent) window.__resetSpoken.push(region.textContent);
        }).observe(region, { childList: true, characterData: true, subtree: true });
        const snapshot = () => {
            for (const callback of window.__events.snapshot || []) callback(JSON.stringify({
                root_channels: [
                    { ChannelID: 1, ParentID: 0, Name: "Lobby", clients: [], children: [] },
                ],
            }));
        };
        const dispatch = (id, text) => {
            const message = JSON.stringify({
                type: "chat",
                data: { id, from: "Bob", from_unique_id: "user-b", text, channel_id: 1 },
            });
            for (const callback of window.__events.event || []) callback(message);
        };
        snapshot();
        window.__noxaChat.beginReconnectAnnouncementBatch(3000);
        dispatch(451, "old server replay");
        for (const callback of window.__events.tab_reset || []) callback("manual-switch");
        state.myClientID = "client-a";
        state.myChannelID = 1;
        snapshot();
        dispatch(452, "new server message");
    });

    await expect.poll(() => page.evaluate(() => window.__resetSpoken)).toEqual([
        "Bob: new server message",
    ]);
    await page.waitForTimeout(850);
    expect(await page.evaluate(() => window.__resetSpoken.some((text) => text.includes("after reconnect")))).toBe(false);
});

test("preserves reconnect batching across the tab created by a real reconnect", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        const state = window.__noxa.state;
        state.settings.chat_notification_level = "all";
        state.settings.notify_matrix = {};
        state.settings.channel_notify = {};
        state.settings.dnd_enabled = false;
        window.__preservedReconnectSpoken = [];
        const region = document.getElementById("chat-announcer");
        new MutationObserver(() => {
            if (region.textContent) window.__preservedReconnectSpoken.push(region.textContent);
        }).observe(region, { childList: true, characterData: true, subtree: true });
        window.__noxaChat.beginReconnectAnnouncementBatch(3000);
        state.reconnectInFlight = true;
        for (const callback of window.__events.tab_reset || []) callback("reconnected-tab");
        state.reconnectInFlight = false;
        state.myClientID = "client-a";
        state.myUniqueID = "user-a";
        state.myNickname = "Alice";
        state.myChannelID = 1;
        for (const callback of window.__events.snapshot || []) callback(JSON.stringify({
            root_channels: [
                { ChannelID: 1, ParentID: 0, Name: "Lobby", clients: [], children: [] },
            ],
        }));
        for (let id = 461; id <= 462; id++) {
            const message = JSON.stringify({
                type: "chat",
                data: { id, from: "Bob", from_unique_id: "user-b", text: `replayed ${id}`, channel_id: 1 },
            });
            for (const callback of window.__events.event || []) callback(message);
        }
    });

    await expect.poll(() => page.evaluate(() => window.__preservedReconnectSpoken)).toEqual([
        "2 messages from Bob received after reconnect",
    ]);
});

test("keeps reconnect countdown changes visual and announces the failure once", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        delete window.__noxa.state.settings.reconnect_on_loss;
        window.__noxa.state.settings.notify_connection = true;
        window.__noxa.state.lastConnect = { addr: "voice.example:12333", nick: "Alice", pw: "", spw: "" };
        for (const callback of window.__events.disconnected || []) callback();
    });
    await expect(page.locator("#conn-pill")).toContainText("retry 1/5 in 5s");
    await expect(page.locator("#alert-announcer")).toHaveText("Connection lost");
    await expect(page.locator("#conn-pill")).not.toHaveAttribute("aria-live", /.+/);
    await page.waitForTimeout(1100);
    await expect(page.locator("#conn-pill")).toContainText("retry 1/5 in 4s");
    await expect(page.locator("#alert-announcer")).toHaveText("Connection lost");
});

test("dispatches one DM notification only for actual E2EE direct messages", async ({ page }) => {
    await page.evaluate(() => {
        const originalNotify = window.__noxaNotify.notify;
        window.__notificationDispatches = [];
        window.__noxaNotify.notify = (event, text, context) => {
            window.__notificationDispatches.push(event);
            return originalNotify(event, text, context);
        };
        Object.defineProperty(document, "hasFocus", { configurable: true, value: () => false });
        window.__noxa.state.myNickname = "Alice";
        window.__noxa.state.myUniqueID = "user-a";
        window.__noxa.state.clients = [
            { client_id: "client-b", unique_id: "user-b", nickname: "Bob", channel_id: 0 },
            { client_id: "client-c", unique_id: "user-c", nickname: "Carol", channel_id: 0 },
        ];

        const direct = JSON.stringify({
            type: "chat",
            data: {
                id: 801, from: "Bob", from_client_id: "client-b", from_unique_id: "user-b",
                text: "private hello", e2e: true, client_msg_id: "dm-801",
            },
        });
        const global = JSON.stringify({
            type: "chat",
            data: {
                id: 802, from: "Carol", from_client_id: "client-c", from_unique_id: "user-c",
                text: "global hello", channel_id: 0, e2e: false,
            },
        });
        for (const callback of window.__events.event || []) callback(direct);
        for (const callback of window.__events.event || []) callback(global);
    });

    await expect.poll(() => page.evaluate(() => window.__notificationDispatches)).toEqual([
        "dm",
        "channel_message",
    ]);
    expect(await page.evaluate(() => window.__notificationDispatches.filter((event) => event === "dm").length)).toBe(1);
    expect(await page.evaluate(() => window.__calls.TrayMention || 0)).toBe(1);
    expect(await page.evaluate(() => window.__noxa.state.lastWhispererUID)).toBe("user-b");
});

test("renders hostile update and image metadata as inert data", async ({ page }) => {
    const attack = `<img src=x onerror="document.body.dataset.remoteXss='yes'">`;
    await page.evaluate(async (payload) => {
        window.__updateInfo = { available: true, version: payload, size: 1048576 };
        await window.__noxa.checkForUpdatesInteractive();
    }, attack);

    expect(await page.evaluate(() => document.body.dataset.remoteXss || "")).toBe("");
    await expect(page.locator(".upd-status")).toHaveText(`update available: ${attack} (1.0 MiB)`);

    await page.evaluate(async (payload) => {
        const host = document.createElement("span");
        host.className = "avatar hostile-avatar";
        host.dataset.uid = "hostile-user";
        document.body.appendChild(host);
        window.__avatarResponse = {
            content_type: `image/png\" onerror=\"document.body.dataset.remoteXss='image'`,
            data_base64: "AAAA",
        };
        await window.__noxa.fetchAvatar("hostile-user");
        window.__noxa.state.avatars.delete("valid-user");
        window.__noxa.state.avatarPending.delete("valid-user");
        const validHost = document.createElement("span");
        validHost.className = "avatar valid-avatar";
        validHost.dataset.uid = "valid-user";
        document.body.appendChild(validHost);
        window.__avatarResponse = { content_type: "image/png", data_base64: "AAAA" };
        await window.__noxa.fetchAvatar("valid-user");
        void payload;
    }, attack);

    expect(await page.evaluate(() => document.body.dataset.remoteXss || "")).toBe("");
    await expect(page.locator(".hostile-avatar img")).toHaveCount(0);
    expect(await page.evaluate(() => window.__noxa.state.avatars.get("hostile-user"))).toBe(null);
    await expect(page.locator(".valid-avatar img")).toHaveAttribute("src", "data:image/png;base64,AAAA");
});

test("cancels server-bound image actions across active-tab resets", async ({ page }) => {
    const image = {
        name: "one-pixel.png",
        mimeType: "image/png",
        buffer: Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=", "base64"),
    };
    const prompts = [];
    page.on("dialog", async (dialog) => {
        prompts.push(dialog.message());
        await dialog.accept("late-emoji");
    });
    const connect = () => page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        window.__noxa.state.myClientID = "client-a";
        window.__noxa.state.isAdmin = true;
    });
    const reset = (tabID) => page.evaluate((id) => {
        for (const callback of window.__events.tab_reset || []) callback(id);
    }, tabID);
    const selfMenu = page.locator("#menubar > .menu-item > span").filter({ hasText: /^Self$/ }).locator("..");
    const chooseFromSelf = async (name) => {
        await selfMenu.click();
        const pending = page.waitForEvent("filechooser");
        await page.getByRole("menuitem", { name }).click();
        return pending;
    };

    // A crop dialog that already exists is scoped to the old server and closes
    // as part of the reset. Closing resolves the picker as cancelled.
    await connect();
    const cropChooser = await chooseFromSelf(/^Set avatar/);
    await cropChooser.setFiles(image);
    await expect(page.getByRole("dialog", { name: "Set avatar" })).toBeVisible();
    await reset("avatar-dialog-reset");
    await expect(page.getByRole("dialog", { name: "Set avatar" })).toHaveCount(0);
    await expect.poll(() => page.evaluate(() => window.__calls.SetAvatarForTab || 0)).toBe(0);

    // A reset while the native picker is open must not allow the crop dialog
    // to mount late under the new generation.
    await connect();
    const lateCropChooser = await chooseFromSelf(/^Set avatar/);
    await reset("avatar-picker-reset");
    await lateCropChooser.setFiles(image);
    await page.waitForTimeout(300);
    await expect(page.getByRole("dialog", { name: "Set avatar" })).toHaveCount(0);
    expect(await page.evaluate(() => window.__calls.SetAvatarForTab || 0)).toBe(0);

    // Server icon compression and quick-emoji upload have no DOM dialog after
    // file selection, so their caller-owned generation tokens block the write.
    await connect();
    const iconChooser = await chooseFromSelf(/^Set server icon/);
    await reset("server-icon-reset");
    await iconChooser.setFiles(image);
    await page.waitForTimeout(300);
    expect(await page.evaluate(() => window.__calls.ServerIconSetForTab || 0)).toBe(0);

    await connect();
    await page.locator("#chat-emoji").click();
    const emojiChooserPromise = page.waitForEvent("filechooser");
    await page.locator(".emoji-upload").click();
    const emojiChooser = await emojiChooserPromise;
    await reset("emoji-picker-reset");
    await emojiChooser.setFiles(image);
    await page.waitForTimeout(300);
    expect(prompts).toEqual([]);
    expect(await page.evaluate(() => window.__calls.EmojiUploadForTab || 0)).toBe(0);
});

test("moves focus explicitly between login and the connected workspace", async ({ page }, testInfo) => {
    await expect(page.locator("#login-addr")).toBeFocused();
    await expect(page.locator(".skip-link")).toBeHidden();
    await expect(page.locator("#login-serverpw")).toHaveAttribute("autocomplete", "off");
    await expect(page.locator(".login-card input[type=password]")).toHaveCount(2);
    await expect(page.locator("#login-accountpw")).toHaveAccessibleName("Account password (optional)");
    await expect(page.locator("#login-serverpw")).toHaveAccessibleName("Server password (optional)");
    await page.locator(".login-card").screenshot({ path: testInfo.outputPath("login.png") });

    await page.locator("#login-nick").fill("Alice");
    await page.locator("#login-serverpw").fill("server-secret");
    await page.getByRole("button", { name: "Connect" }).click();
    await expect(page.locator("#center")).toBeFocused();
    expect(await page.evaluate(() => window.__callArgs.ConnectBookmarkTabWithID[0])).toEqual([
        "", "127.0.0.1:12333", "Alice", "", "server-secret",
    ]);
    await expect(page.locator("#app")).toHaveAttribute("aria-hidden", "false");
    await expect(page.locator(".skip-link")).toBeAttached();

    await page.evaluate(() => window.__noxa.showLogin());
    await expect(page.locator("#login-addr")).toBeFocused();
    await expect(page.locator("#app")).toHaveAttribute("aria-hidden", "true");

    await page.evaluate(() => {
        window.__tabs = [{
            id: "auto-tab", addr: "auto.example:12333", nickname: "Alice",
            active: true, connected: true, unread: 0, mentions: 0,
        }];
        for (const callback of window.__events.tab_update || []) callback(structuredClone(window.__tabs));
    });
    await expect(page.locator("#center")).toBeFocused();
    await expect(page.locator("#app")).toHaveAttribute("aria-hidden", "false");
});

test("computes names for settings and generated dialog controls", async ({ page }) => {
    await page.evaluate(() => window.__noxa.openSettings("application"));
    await expect(page.locator('#settings-content input[type="number"]').first()).toHaveAccessibleName("Chat max lines");
    await expect(page.locator("#settings-content select").first()).toHaveAccessibleName("Language");
    await expect(page.getByRole("combobox", { name: "Theme", exact: true })).toBeVisible();
    await expect(page.locator('#settings-content input[type="range"]').first()).toHaveAccessibleName("UI font size");
    await page.keyboard.press("Escape");

    await page.evaluate(() => {
        const app = window.go.main.App;
        window.go.main.App = new Proxy(app, { get(target, key) {
            if (key === "RoleChannelStateForTab") return async () => ({
                revision: 8, everyone_id: 1, name: "Top level", can_create_permanent: true,
                can_create_temporary: true, can_manage_access: false, destinations: [],
                settings: { name: "", topic: "", description: "", max_clients: 0,
                    slow_mode_seconds: 0, order_index: 0, opus_bitrate: 32000,
                    opus_fec: true, opus_dtx: true, opus_stereo: false },
            });
            return target[key];
        } });
        window.__noxa.showWorkspace();
    });
    await page.locator("#channel-create-btn").click();
    const create = page.getByRole("dialog", { name: "Create channel" });
    await expect(create.getByRole("textbox", { name: "Channel name" })).toBeVisible();
    await expect(create.getByRole("combobox", { name: "Channel lifetime" })).toBeVisible();
    await create.getByText("Limits and audio").click();
    await expect(create.getByRole("spinbutton", { name: "Participant limit (0 = unlimited)" })).toBeVisible();
    await page.keyboard.press("Escape");
});

test("keeps long channel dialogs within a small window and scrolls to their actions", async ({ page }) => {
    await page.setViewportSize({ width: 1024, height: 730 });
    await page.evaluate(() => {
        const app = window.go.main.App;
        window.go.main.App = new Proxy(app, { get(target, key) {
            if (key === "RoleChannelStateForTab") return async () => ({
                revision: 8, everyone_id: 1, name: "Top level", can_create_permanent: true,
                can_create_temporary: true, can_manage_access: false, destinations: [],
                settings: { name: "", topic: "", description: "", max_clients: 0,
                    slow_mode_seconds: 0, order_index: 0, opus_bitrate: 32000,
                    opus_fec: true, opus_dtx: true, opus_stereo: false },
            });
            return target[key];
        } });
        window.__noxa.showWorkspace();
    });
    await page.getByRole("button", { name: "Create channel", exact: true }).click();
    const dialog = page.getByRole("dialog", { name: "Create channel" });
    const panel = dialog.locator(".dlg");
    await panel.evaluate(async (element) => {
        await document.fonts.ready;
        await Promise.all(element.getAnimations().map((animation) => animation.finished));
    });
    const bounds = await panel.boundingBox();
    expect(bounds.y).toBeGreaterThanOrEqual(0);
    expect(bounds.y + bounds.height).toBeLessThanOrEqual(730);
    await expect(dialog.getByRole("heading", { name: "Create channel" })).toBeInViewport({ ratio: 1 });
    const name = dialog.getByRole("textbox", { name: "Channel name", exact: true });
    await name.fill("Small window room");
    const create = dialog.getByRole("button", { name: "Create", exact: true });
    const close = dialog.getByRole("button", { name: "Close", exact: true });
    await expect(create).toBeInViewport({ ratio: 1 });
    await expect(close).toBeInViewport({ ratio: 1 });
    await dialog.getByText("Limits and audio").click();
    await expect(dialog.getByRole("spinbutton", { name: "Participant limit (0 = unlimited)" })).toBeVisible();
    await expect(create).toBeInViewport({ ratio: 1 });
    await close.click();
    await page.getByRole("button", { name: "Apply", exact: true }).click();
    await expect(dialog).toHaveCount(0);
});

async function openChannelEditor(page) {
    await page.evaluate(() => {
        const app = window.go.main.App;
        window.go.main.App = new Proxy(app, {
            get(target, key) {
                if (key === "RoleChannelStateForTab") return async () => ({
                    revision: 8,
                    channel_id: 2,
                    name: "Public",
                    destinations: [{ id: 0, name: "Root", can_sync: true }],
                    settings: {
                        name: "Public",
                        topic: "Everyone welcome",
                        description: "",
                        order_index: 1,
                        max_clients: 0,
                        slow_mode_seconds: 0,
                        opus_bitrate: 32000,
                        opus_fec: true,
                        opus_dtx: true,
                        opus_stereo: false,
                    },
                });
                return target[key];
            },
        });
        window.__noxa.showWorkspace(false);
        for (const cb of window.__events.snapshot || []) cb(JSON.stringify({
            root_channels: [{ ChannelID: 2, Name: "Public", Topic: "Everyone welcome", ParentID: 0,
                OpusBitrate: 32000, OpusFEC: true, OpusDTX: true, OrderIndex: 1, clients: [], children: [] }],
        }));
    });
    if (!(await page.locator("#sidebar").isVisible())) await page.getByRole("button", { name: "Show channels", exact: true }).click();
    await page.locator('.channel[data-chid="2"]').click({ button: "right" });
    await page.getByText("Edit channel", { exact: true }).click();
    return page.getByRole("dialog", { name: "Edit channel", exact: true });
}

test("channel editor keeps its title and actions visible at small window sizes @a11y", async ({ page }) => {
    for (const viewport of [{ width: 1000, height: 730 }, { width: 640, height: 480 }]) {
        await page.setViewportSize(viewport);
        const dialog = await openChannelEditor(page);
        await expect(dialog.getByRole("heading", { name: "Edit channel" })).toBeInViewport({ ratio: 1 });
        await expect(dialog.getByRole("button", { name: "Save changes" })).toBeInViewport({ ratio: 1 });
        await expect(dialog.getByRole("button", { name: "Close", exact: true })).toBeInViewport({ ratio: 1 });
        for (const summary of await dialog.locator("summary").all()) await summary.click();
        await expect(dialog.getByLabel("Sort order", { exact: true })).toBeVisible();
        await expect(dialog.getByRole("heading", { name: "Edit channel" })).toBeInViewport({ ratio: 1 });
        await expect(dialog.getByRole("button", { name: "Save changes" })).toBeInViewport({ ratio: 1 });
        const dimensions = await dialog.evaluate((el) => ({ width: el.clientWidth, scrollWidth: el.scrollWidth }));
        expect(dimensions.scrollWidth).toBeLessThanOrEqual(dimensions.width);
        await auditAccessibility(page, "channel editor");
        await page.keyboard.press("Escape");
        await expect(dialog).toHaveCount(0);
    }
});

test("closes menus when keyboard focus exits and keeps expansion state in sync", async ({ page }) => {
    await page.evaluate(() => window.__noxa.showWorkspace(false));
    const tools = page.locator("#menubar > .menu-item").filter({ hasText: /^Tools/ });
    await tools.focus();
    await page.keyboard.press("Enter");
    await expect(tools).toHaveAttribute("aria-expanded", "true");
    await expect(page.getByRole("menuitem", { name: /^Settings/ })).toBeFocused();

    await page.keyboard.press("Tab");
    await expect(tools).toHaveAttribute("aria-expanded", "false");
    await expect(tools.locator(".menu-dropdown")).not.toHaveClass(/open/);
});

test("uses standard Left and Right behavior in the channel tree", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        window.__noxa.state.myClientID = "client-a";
        for (const cb of window.__events.snapshot || []) cb(JSON.stringify({
            root_channels: [{
                ChannelID: 1, ParentID: 0, Name: "Parent", clients: [],
                children: [{
                    ChannelID: 2, ParentID: 1, Name: "Child",
                    clients: [{ client_id: "client-a", unique_id: "user-a", nickname: "Alice", channel_id: 2 }],
                    children: [],
                }],
            }],
        }));
    });

    const parent = page.locator('.channel[data-chid="1"]');
    const child = page.locator('.channel[data-chid="2"]');
    await parent.focus();
    await page.keyboard.press("ArrowLeft");
    await expect(parent).toHaveAttribute("aria-expanded", "false");
    await expect(child).toHaveCount(0);

    await page.keyboard.press("ArrowRight");
    await expect(parent).toHaveAttribute("aria-expanded", "true");
    await page.keyboard.press("ArrowRight");
    await expect(child).toBeFocused();
    await page.keyboard.press("ArrowLeft");
    await expect(child).toHaveAttribute("aria-expanded", "false");
    await page.keyboard.press("ArrowLeft");
    await expect(parent).toBeFocused();
});

test("maintains a nested dialog stack across media, rerenders, and zero-control dialogs", async ({ page }) => {
    await page.evaluate(async () => {
        window.__noxa.showWorkspace(false);
        const { mountDialog } = await import("/src/modal.js");
        const launcher = document.getElementById("chat-info-btn");
        launcher.closest("details").open = true;
        launcher.classList.remove("hidden");
        launcher.focus();

        const outer = document.createElement("div");
        outer.className = "dlg-overlay";
        const renderOuter = (step) => {
            outer.innerHTML = `<div class="dlg"><h3>Outer step ${step}</h3><button class="open-media">Open media</button><button class="next-step">Next</button></div>`;
            outer.querySelector(".open-media").onclick = () => {
                const inner = document.createElement("div");
                inner.className = "dlg-overlay";
                inner.innerHTML = '<div class="dlg"><h3>Media preview</h3><video controls aria-label="Preview media"></video></div>';
                mountDialog(inner);
            };
            outer.querySelector(".next-step").onclick = () => renderOuter(step + 1);
        };
        renderOuter(1);
        mountDialog(outer);
    });

    const outer = page.locator('.dlg-overlay[aria-labelledby]:has-text("Outer step")');
    await expect(page.getByRole("dialog", { name: "Outer step 1" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Open media" })).toBeFocused();
    await page.getByRole("button", { name: "Open media" }).click();
    await expect(page.getByRole("dialog", { name: "Media preview" })).toBeVisible();
    await expect(page.getByLabel("Preview media")).toBeFocused();
    await expect(outer).toHaveJSProperty("inert", true);

    await page.keyboard.press("Escape");
    await expect(page.getByRole("dialog", { name: "Media preview" })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Open media" })).toBeFocused();
    await page.getByRole("button", { name: "Next" }).click();
    await expect(page.getByRole("dialog", { name: "Outer step 2" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Open media" })).toBeFocused();

    await page.keyboard.press("Escape");
    await expect(page.locator("#chat-info-btn")).toBeFocused();
    await page.evaluate(async () => {
        const { mountDialog } = await import("/src/modal.js");
        const empty = document.createElement("div");
        empty.className = "dlg-overlay";
        empty.innerHTML = '<div class="dlg"><h3>Working</h3><p>Please wait.</p></div>';
        mountDialog(empty);
    });
    const zero = page.getByRole("dialog", { name: "Working" });
    await expect(zero).toBeFocused();
    await page.keyboard.press("Escape");
    await expect(zero).toHaveCount(0);
    await expect(page.locator("#chat-info-btn")).toBeFocused();
});

test("falls back from invalid modal focus and restores login after a workspace transition", async ({ page }) => {
    await page.evaluate(async () => {
        window.__noxa.showWorkspace(false);
        const { mountDialog } = await import("/src/modal.js");
        const launcher = document.getElementById("chat-info-btn");
        launcher.closest("details").open = true;
        launcher.classList.remove("hidden");
        launcher.focus();
        const overlay = document.createElement("div");
        overlay.className = "dlg-overlay transition-dialog";
        overlay.innerHTML = `
            <div class="dlg">
                <h3>Transition focus</h3>
                <div hidden><button class="hidden-target">Hidden target</button></div>
                <button class="visible-target">Visible target</button>
            </div>`;
        mountDialog(overlay, { launcher, initialFocus: ".hidden-target" });
    });
    await expect(page.locator(".visible-target")).toBeFocused();

    await page.evaluate(() => window.__noxa.showLogin());
    await page.keyboard.press("Escape");
    await expect(page.locator(".transition-dialog")).toHaveCount(0);
    await expect(page.locator("#login-addr")).toBeFocused();
});

test("finalizes a dialog removed inside an ancestor subtree exactly once", async ({ page }) => {
    await page.evaluate(async () => {
        window.__noxa.showWorkspace(false);
        const { mountDialog } = await import("/src/modal.js");
        window.__subtreeDialogCloses = 0;
        const wrapper = document.createElement("section");
        wrapper.id = "dialog-wrapper";
        document.body.appendChild(wrapper);
        const overlay = document.createElement("div");
        overlay.className = "dlg-overlay subtree-dialog";
        overlay.innerHTML = '<div class="dlg"><h3>Subtree dialog</h3><button>Ready</button></div>';
        mountDialog(overlay, { onClose: () => { window.__subtreeDialogCloses++; } });
        wrapper.appendChild(overlay);
    });
    await expect(page.getByRole("dialog", { name: "Subtree dialog" })).toBeVisible();
    await page.evaluate(() => document.getElementById("dialog-wrapper").remove());
    await expect(page.locator(".subtree-dialog")).toHaveCount(0);
    await expect.poll(() => page.evaluate(() => window.__subtreeDialogCloses)).toBe(1);
    await page.waitForTimeout(100);
    expect(await page.evaluate(() => window.__subtreeDialogCloses)).toBe(1);
});

test("keeps a blocking gate visually and semantically above deferred dialogs", async ({ page }) => {
    await page.evaluate(async () => {
        window.__noxa.showWorkspace(false);
        for (const callback of window.__events.server_rules || []) {
            callback(JSON.stringify({ hash: "rules-v1", text: "Be excellent to each other." }));
        }
        const { mountDialog } = await import("/src/modal.js");
        const deferred = document.createElement("div");
        deferred.className = "dlg-overlay deferred-dialog";
        deferred.innerHTML = '<div class="dlg"><h3>Deferred reminder</h3><button>Continue</button></div>';
        mountDialog(deferred);
    });

    const gate = page.locator(".server-rules-gate");
    const deferred = page.locator(".deferred-dialog");
    await expect(gate).toHaveAttribute("aria-modal", "true");
    await expect(gate).not.toHaveAttribute("aria-hidden", "true");
    await expect(deferred).toHaveAttribute("aria-hidden", "true");
    await expect.poll(() => gate.evaluate((element) => element.inert)).toBe(false);
    await expect.poll(() => deferred.evaluate((element) => element.inert)).toBe(true);
    const [gateZ, deferredZ, skipLinkZ] = await page.evaluate(() => [
        Number(getComputedStyle(document.querySelector(".server-rules-gate")).zIndex),
        Number(getComputedStyle(document.querySelector(".deferred-dialog")).zIndex),
        Number(getComputedStyle(document.querySelector(".skip-link")).zIndex),
    ]);
    expect(gateZ).toBeGreaterThan(deferredZ);
    expect(gateZ).toBeGreaterThan(skipLinkZ);
    await expect(page.getByRole("button", { name: "Decline and disconnect" })).toBeFocused();

    await page.evaluate(() => window.__noxaNotify.resetServerRules());
    await expect(gate).toHaveCount(0);
    await expect(deferred).toHaveAttribute("aria-modal", "true");
    await expect(page.getByRole("button", { name: "Continue" })).toBeFocused();
    await page.keyboard.press("Escape");
});

test("Escape runs polling-dialog cleanup and allows stateful dialogs to reopen", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        window.__noxa.state.myClientID = "client-a";
        window.__noxa.openClientInfo({ client_id: "client-a", unique_id: "user-a", nickname: "Alice" });
    });
    await expect(page.getByRole("dialog", { name: "Connection Info" })).toBeVisible();
    await expect.poll(() => page.evaluate(() => window.__calls.GetClientInfoForTab || 0)).toBeGreaterThan(0);
    await page.keyboard.press("Escape");
    const clientInfoCalls = await page.evaluate(() => window.__calls.GetClientInfoForTab || 0);
    await page.waitForTimeout(2200);
    expect(await page.evaluate(() => window.__calls.GetClientInfoForTab || 0)).toBe(clientInfoCalls);

    await page.evaluate(() => window.__noxaFiles.openTransfers());
    await expect(page.getByRole("dialog", { name: "Transfers" })).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(page.getByRole("dialog", { name: "Transfers" })).toHaveCount(0);
    await page.evaluate(() => window.__noxaFiles.openTransfers());
    await expect(page.getByRole("dialog", { name: "Transfers" })).toBeVisible();
    await page.keyboard.press("Escape");

    await page.evaluate(() => window.__noxaMeta.openStatsPage());
    await expect(page.getByRole("dialog", { name: "Server information" })).toBeVisible();
    await page.keyboard.press("Escape");
    const statsCalls = await page.evaluate(() => window.__calls.GetClientInfoForTab || 0);
    await page.waitForTimeout(1200);
    expect(await page.evaluate(() => window.__calls.GetClientInfoForTab || 0)).toBe(statsCalls);
    await page.evaluate(() => window.__noxaMeta.openStatsPage());
    await expect(page.getByRole("dialog", { name: "Server information" })).toBeVisible();
});

test("keeps onboarding semantics and focus when each step rerenders", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.state.settings.onboarding_done = false;
        window.__noxaMeta.maybeOnboard();
        window.__noxaMeta.maybeOnboard();
    });
    await expect(page.locator(".onboarding")).toHaveCount(1);
    await expect(page.getByRole("dialog", { name: "Welcome to noXa" })).toBeVisible();
    await expect(page.locator(".ob-nick")).toBeFocused();
    await page.getByRole("button", { name: "Next" }).click();
    await expect(page.getByRole("dialog", { name: "Microphone check" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Open capture settings" })).toBeFocused();
    await page.keyboard.press("Escape");
    await expect(page.locator(".onboarding")).toHaveCount(0);
    await expect.poll(() => page.evaluate(() => window.__noxa.state.settings.onboarding_done)).toBe(true);
});

test("debounces keyboard pane persistence and refreshes separator values", async ({ page }) => {
    await page.evaluate(() => window.__noxa.showWorkspace(false));
    const handle = page.getByRole("separator", { name: "Resize channels pane" });
    await handle.focus();
    const before = await page.evaluate(() => window.__calls.SaveSettings || 0);
    await page.keyboard.press("ArrowRight");
    await page.keyboard.press("ArrowRight");
    await page.keyboard.press("ArrowRight");
    await expect.poll(() => page.evaluate(() => window.__calls.SaveSettings || 0)).toBe(before + 1);
    await expect.poll(() => handle.evaluate((element) =>
        Number(element.getAttribute("aria-valuenow")) - Math.round(element.parentElement.getBoundingClientRect().width),
    )).toBe(0);
});

test("uses grouped, distinct action sounds without replaying historical tab activity", async ({ page }) => {
    const result = await page.evaluate(async () => {
        const tones = [];
        const media = [];
        const { soundEngine } = window.__noxa;
        await soundEngine.preload();
        await soundEngine.resume();
        if (soundEngine.buffers.size !== 51 || soundEngine.ctx.state !== "running") throw new Error(JSON.stringify({ buffers: soundEngine.buffers.size, state: soundEngine.ctx.state, warnings: [...soundEngine.warnings] }));
        let clock = 0;
        soundEngine.now = () => clock += 1000;
        const originalSource = soundEngine.ctx.createBufferSource.bind(soundEngine.ctx);
        soundEngine.ctx.createBufferSource = () => {
            const source = originalSource();
            const start = source.start.bind(source);
            source.start = (...args) => {
                const name = [...soundEngine.buffers].find(([, buffer]) => buffer === source.buffer)?.[0];
                if (name === "own_channel_join") media.push(name);
                else tones.push(name);
                start(...args);
            };
            return source;
        };
        const state = window.__noxa.state;
        state.settings = {
            ...state.settings,
            activation_mode: "ptt",
            ptt_release_delay_ms: 0,
            event_sounds: {},
            notify_matrix: {},
            custom_sounds: {},
        };
        state.myClientID = "client-a";
        state.myChannelID = 1;
        state.clients = [
            { client_id: "client-a", unique_id: "user-a", nickname: "Alice", channel_id: 1 },
            { client_id: "client-b", unique_id: "user-b", nickname: "Bob", channel_id: 2 },
        ];
        const emit = (name, payload) => {
            for (const callback of window.__events[name] || []) callback(payload);
        };
        const move = (channelID) => emit("event", JSON.stringify({
            type: "user_moved", data: { client_id: "client-b", channel_id: channelID },
        }));
        const collect = (fn) => {
            for (const entry of soundEngine.active) soundEngine.release(entry);
            tones.length = 0;
            fn();
            return [...tones];
        };

        const moveIn = collect(() => move(1));
        const moveOut = collect(() => move(2));
        // Legacy custom beeps no longer override the authored action cue.
        state.settings.notify_matrix.join_leave = { toast: true, sound: false, flash: false, native: false };
        const matrixOff = collect(() => move(1));
        state.settings.notify_matrix.join_leave.sound = true;
        const custom = collect(() => move(2));
        state.replayingTabID = "tab-a";
        const replay = collect(() => move(1));
        emit("tab_replay_done", "tab-a");
        state.settings.event_sounds.user_move_out = false;
        const disabledSpecific = collect(() => move(2));
        state.settings.event_sounds.user_move_out = true;
        const afterReplay = collect(() => move(1));
        state.myUniqueID = "user-a";
        state.lastConnect = { addr: "sound.example:12333" };
        state.settings.chat_notification_level = "all";
        state.settings.keywords = { "sound.example:12333": ["urgent"] };
        const chat = (id, text, role_mentions = []) => emit("event", JSON.stringify({
            type: "chat", data: {
                id, from: "Bob", from_unique_id: "user-b", text, channel_id: 1, role_mentions,
            },
        }));
        const keywordChat = collect(() => chat(901, "urgent request"));
        const roleChat = collect(() => chat(902, "<@&1> urgent request", ["user-a"]));
        const ordinaryChat = collect(() => chat(903, "ordinary request"));
        state.myChannelID = 0;
        state.clients = [{ client_id: "client-a", unique_id: "user-a", nickname: "Alice", channel_id: 7 }];
        const mediaBeforeOwnJoin = media.length;
        const ownJoin = collect(() => window.__noxa.syncOwnChannel());
        const ownJoinMedia = media.length - mediaBeforeOwnJoin;
        state.clients[0].channel_id = 8;
        const ownSwitch = collect(() => window.__noxa.syncOwnChannel());
        state.myChannelID = 9;
        state.clients = [{ client_id: "client-a", unique_id: "user-a", nickname: "Alice", channel_id: 9 }];
        state.channels = [{ ChannelID: 9, ParentID: 0, Name: "Deleted" }];
        const channelDeletion = collect(() => {
            emit("event", JSON.stringify({ type: "channel_deleted", data: { channel_id: 9 } }));
            emit("event", JSON.stringify({ type: "user_moved", data: { client_id: "client-a", channel_id: 0 } }));
        });
        state.settings.activation_mode = "vad";
        const vadPTT = collect(() => window.__noxa.setPTT(true));
        window.__noxa.setPTT(false);
        state.settings.activation_mode = "ptt";
        const ptt = collect(() => window.__noxa.setPTT(true));
        window.__noxa.setPTT(false);
        const deafen = collect(() => window.__noxa.setDeafened(true));
        state.settings.bookmarks = [{ name: "Guest", addr: "guest.example:12333", nickname: "Guest" }];
        window.__tabs = [{
            id: "guest-tab", addr: "guest.example:12333", nickname: "Guest",
            active: true, connected: true, unread: 0, mentions: 0,
        }];
        window.__guestConnectHandler = async () => {
            // Emulate Go's connect/activate ordering: reset, replayed state,
            // then replay completion, all before the bridge resolves.
            emit("tab_reset", "guest-tab");
            emit("snapshot", JSON.stringify({ root_channels: [{
                ChannelID: 15, ParentID: 0, Name: "Guest channel", clients: [{
                    client_id: "client-a", unique_id: "user-a", nickname: "Alice", channel_id: 15,
                }], children: [],
            }] }));
            // Let ClientID resolve and record the replayed channel while cues
            // remain suppressed, then finish the replay.
            await Promise.resolve();
            await Promise.resolve();
            emit("tab_replay_done", "guest-tab");
            return { tab_id: "guest-tab", error: "" };
        };
        const mediaBeforeGuest = media.length;
        tones.length = 0;
        await window.__noxaTabs.quickConnectLast();
        const guestConnect = [...tones];
        const guestInitialJoinMedia = media.length - mediaBeforeGuest;
        const guestInitialCueCleared = state.pendingInitialChannelCueTabID === "";

        // Exercise the opposite race too: replay completes before ClientID.
        // syncOwnChannel must then play the pending initial join when identity
        // arrives, without replay history getting its own cue.
        let releaseReplayFirstIdentity;
        window.__clientIDGate = new Promise((resolve) => { releaseReplayFirstIdentity = resolve; });
        window.__tabs = [{
            id: "replay-first-tab", addr: "replay.example:12333", nickname: "Replay",
            active: true, connected: true, unread: 0, mentions: 0,
        }];
        emit("tab_reset", "replay-first-tab");
        emit("snapshot", JSON.stringify({ root_channels: [{
            ChannelID: 16, ParentID: 0, Name: "Replay channel", clients: [{
                client_id: "client-a", unique_id: "user-a", nickname: "Alice", channel_id: 16,
            }], children: [],
        }] }));
        emit("tab_replay_done", "replay-first-tab");
        const mediaBeforeReplayFirstIdentity = media.length;
        releaseReplayFirstIdentity();
        await new Promise((resolve) => setTimeout(resolve, 0));
        window.__clientIDGate = null;
        const replayFirstIdentityMedia = media.length - mediaBeforeReplayFirstIdentity;
        const replayFirstCueCleared = state.pendingInitialChannelCueTabID === "";

        // A channel-0 replay leaves its initial marker armed. Its first live
        // self-move must consume that marker, so the following equality sync
        // cannot duplicate the channel cue.
        window.__tabs = [{
            id: "live-move-tab", addr: "live.example:12333", nickname: "Live",
            active: true, connected: true, unread: 0, mentions: 0,
        }];
        emit("tab_reset", "live-move-tab");
        emit("snapshot", JSON.stringify({ root_channels: [{
            ChannelID: 18, ParentID: 0, Name: "No channel", clients: [{
                client_id: "client-a", unique_id: "user-a", nickname: "Alice", channel_id: 0,
            }], children: [],
        }] }));
        await new Promise((resolve) => setTimeout(resolve, 0));
        emit("tab_replay_done", "live-move-tab");
        const mediaBeforeLiveMove = media.length;
        emit("event", JSON.stringify({
            type: "user_moved", data: { client_id: "client-a", channel_id: 17 },
        }));
        window.__noxa.syncOwnChannel();
        const liveMoveInitialMedia = media.length - mediaBeforeLiveMove;
        const liveMoveCueCleared = state.pendingInitialChannelCueTabID === "";
        window.__noxa.openSettings("notifications");
        const groups = [...document.querySelectorAll("#settings-content .set-subhead")].map((element) => element.querySelector("span")?.textContent || element.textContent);
        return { moveIn, moveOut, matrixOff, custom, replay, disabledSpecific, afterReplay,
            keywordChat, roleChat, ordinaryChat,
            ownJoin, ownJoinMedia, ownSwitch, channelDeletion, vadPTT, ptt, deafen, guestConnect,
            guestInitialJoinMedia, guestInitialCueCleared, replayFirstIdentityMedia,
            replayFirstCueCleared, liveMoveInitialMedia, liveMoveCueCleared, groups };
    });

    expect(result.moveIn).toEqual(["user_move_in"]);
    expect(result.moveOut).toEqual(["user_move_out"]);
    expect(result.matrixOff).toEqual([]);
    expect(result.custom).toEqual(["user_move_out"]);
    expect(result.replay).toEqual([]);
    expect(result.disabledSpecific).toEqual([]);
    expect(result.afterReplay).toEqual(["user_move_in"]);
    expect(result.keywordChat).toEqual(["keyword"]);
    expect(result.roleChat).toEqual(["mention"]);
    expect(result.ordinaryChat).toEqual(["channel_message"]);
    expect(result.ownJoin).toEqual([]);
    expect(result.ownJoinMedia).toBe(1);
    expect(result.ownSwitch).toEqual(["own_channel_switch"]);
    expect(result.channelDeletion).toEqual(["own_channel_leave"]);
    expect(result.vadPTT).toEqual([]);
    expect(result.ptt).toEqual(["ptt_on"]);
    expect(result.deafen).toEqual(["deafen_on"]);
    expect(result.guestConnect).toEqual(["connection_connected"]);
    expect(result.guestInitialJoinMedia).toBe(1);
    expect(result.guestInitialCueCleared).toBe(true);
    expect(result.replayFirstIdentityMedia).toBe(1);
    expect(result.replayFirstCueCleared).toBe(true);
    expect(result.liveMoveInitialMedia).toBe(1);
    expect(result.liveMoveCueCleared).toBe(true);
    expect(result.groups).toEqual(expect.arrayContaining([
        "Connection", "Your channel", "Other users", "Voice controls", "Notifications",
    ]));
    await expect(page.getByText("Channel message", { exact: true })).toBeVisible();
});

test("scopes connection failures and active-tab close sounds", async ({ page }) => {
    const result = await page.evaluate(async () => {
        const tones = [];
        const { soundEngine } = window.__noxa;
        await soundEngine.preload();
        await soundEngine.resume();
        if (soundEngine.buffers.size !== 51 || soundEngine.ctx.state !== "running") throw new Error(JSON.stringify({ buffers: soundEngine.buffers.size, state: soundEngine.ctx.state, warnings: [...soundEngine.warnings] }));
        let clock = 0;
        soundEngine.now = () => clock += 1000;
        const originalSource = soundEngine.ctx.createBufferSource.bind(soundEngine.ctx);
        soundEngine.ctx.createBufferSource = () => {
            const source = originalSource();
            const start = source.start.bind(source);
            source.start = (...args) => {
                const name = [...soundEngine.buffers].find(([, buffer]) => buffer === source.buffer)?.[0];
                tones.push(name);
                start(...args);
            };
            return source;
        };
        const state = window.__noxa.state;
        state.settings = { ...state.settings, event_sounds: {}, notify_matrix: {} };
        const emit = (name, payload) => {
            for (const callback of window.__events[name] || []) callback(payload);
        };
        window.__tabs = [
            { id: "tab-a", addr: "a.example:12333", nickname: "Alice", active: true, connected: true, unread: 0, mentions: 0 },
            { id: "tab-b", addr: "b.example:12333", nickname: "Bob", active: false, connected: true, unread: 0, mentions: 0 },
        ];
        emit("tab_reset", "tab-a");
        emit("tab_replay_done", "tab-a");
        document.getElementById("login-addr").value = "slow.example:12333";
        document.getElementById("login-nick").value = "Alice";
        window.__connectBookmarkGate = new Promise((resolve) => { window.__releaseSlowConnect = resolve; });
        const slowLogin = window.__noxa.connectFromLogin();
        await Promise.resolve();
        emit("tab_reset", "tab-b");
        emit("tab_replay_done", "tab-b");
        window.__connectBookmarkResult = "server unavailable";
        window.__releaseSlowConnect();
        await slowLogin;
        const staleFailure = [...tones];

        tones.length = 0;
        window.__connectBookmarkGate = null;
        window.__connectBookmarkResult = "still unavailable";
        await window.__noxa.connectFromLogin();
        const currentFailure = [...tones];
        window.__connectBookmarkResult = "";

        tones.length = 0;
        window.__disconnectHandler = async () => {
            // Match App.DisconnectTab -> closeTab(true): the Go-owned edge is
            // emitted before replacement tab replay and bridge resolution.
            emit("intentional_disconnect", "tab-b");
            window.__tabs = [
                { id: "tab-a", addr: "a.example:12333", nickname: "Alice", active: true, connected: true, unread: 0, mentions: 0 },
                { id: "tab-b", addr: "b.example:12333", nickname: "Bob", active: false, connected: true, unread: 0, mentions: 0 },
            ];
            emit("tab_reset", "tab-a");
            emit("tab_replay_done", "tab-a");
            return "";
        };
        emit("tray_disconnect");
        await new Promise((resolve) => setTimeout(resolve, 0));
        const menuDisconnect = [...tones];

        emit("tab_update", structuredClone(window.__tabs));
        tones.length = 0;
        document.querySelector('.srv-tab[data-tab-id="tab-b"] .srv-tab-x').click();
        await new Promise((resolve) => setTimeout(resolve, 0));
        const backgroundClose = [...tones];

        window.__disconnectTabHandler = async (tabID) => {
            if (tabID === "tab-a") {
                emit("intentional_disconnect", "tab-a");
                window.__tabs = [{
                    id: "tab-b", addr: "b.example:12333", nickname: "Bob",
                    active: true, connected: true, unread: 0, mentions: 0,
                }];
                emit("tab_reset", "tab-b");
                emit("tab_replay_done", "tab-b");
            }
            return "";
        };
        tones.length = 0;
        document.querySelector('.srv-tab[data-tab-id="tab-a"] .srv-tab-x').click();
        await new Promise((resolve) => setTimeout(resolve, 0));
        const activeClose = [...tones];

        window.__tabs = [{
            id: "tab-b", addr: "b.example:12333", nickname: "Bob",
            active: true, connected: false, unread: 0, mentions: 0,
        }];
        emit("tab_update", structuredClone(window.__tabs));
        tones.length = 0;
        document.querySelector('.srv-tab[data-tab-id="tab-b"] .srv-tab-x').click();
        await new Promise((resolve) => setTimeout(resolve, 0));
        const activeOfflineClose = [...tones];

        tones.length = 0;
        window.__disconnectHandler = null;
        window.__disconnectTabHandler = null;
        emit("tray_disconnect");
        await new Promise((resolve) => setTimeout(resolve, 0));
        const offlineMenuDisconnect = [...tones];
        return {
            staleFailure, currentFailure, menuDisconnect, backgroundClose, activeClose,
            activeOfflineClose, offlineMenuDisconnect,
        };
    });

    expect(result.staleFailure).toEqual([]);
    expect(result.currentFailure).toEqual(["connection_failed"]);
    expect(result.menuDisconnect).toEqual(["connection_disconnected"]);
    expect(result.backgroundClose).toEqual([]);
    expect(result.activeClose).toEqual(["connection_disconnected"]);
    expect(result.activeOfflineClose).toEqual([]);
    expect(result.offlineMenuDisconnect).toEqual([]);
});


test("viewer-start sound belongs to the current publication and obeys sound settings", async ({ page }) => {
    const result = await page.evaluate(async () => {
        const { state, soundEngine } = window.__noxa;
        state.pc = {}; state.myClientID = "publisher"; state.myChannelID = 1;
        state.replayingTabID = "";
        state.settings = { ...state.settings, play_sounds: true, effects_enabled: true, sound_volume: 100,
            event_sounds: {}, notify_matrix: {}, dnd_enabled: false, dnd_from: "", dnd_to: "" };
        window.go.main.App = new Proxy(window.go.main.App, { get: (target, key) => key === "VideoStreamControlForTab"
            ? async (_tab, msg) => ({ ...msg, generation: "90" }) : target[key] });
        const p = await import("/src/stream-publication.js");
        const track = document.createElement("canvas").captureStream(1).getVideoTracks()[0];
        if (!await p.startPublication("cam", track)) throw new Error("fixture publication rejected");
        await soundEngine.preload(); await soundEngine.resume();
        let clock = 0; soundEngine.now = () => clock += 1000;
        const heard = []; const original = soundEngine.play.bind(soundEngine);
        soundEngine.play = (name, options) => { const ok = original(name, options); if (ok) heard.push(name); return ok; };
        const emit = (data = {}) => {
            for (const entry of soundEngine.active) soundEngine.release(entry);
            for (const cb of window.__events.event) cb(JSON.stringify({ type: "stream_watch_started", data: { publisher_id: "publisher", slot: "cam", generation: "90", ...data } }));
            return heard.length;
        };
        const counts = [emit(), emit({ generation: "89" }), emit({ publisher_id: "someone-else" }), emit({ slot: "screen" })];
        state.settings.play_sounds = false; counts.push(emit()); state.settings.play_sounds = true;
        state.settings.event_sounds.stream_watch_started = false; counts.push(emit()); state.settings.event_sounds.stream_watch_started = true;
        state.settings.effects_enabled = false; counts.push(emit()); state.settings.effects_enabled = true;
        state.settings.dnd_enabled = true; counts.push(emit()); state.settings.dnd_enabled = false;
        state.replayingTabID = "past"; counts.push(emit()); state.replayingTabID = "";
        counts.push(emit());
        await p.stopPublication("cam"); counts.push(emit()); track.stop();
        return { counts, heard };
    });
    expect(result).toEqual({ counts: [1,1,1,1,1,1,1,1,1,2,2], heard: ["stream_watch_started", "stream_watch_started"] });
});
