import { test, expect } from "./fixtures.js";

// Design 45 fullscreen HUD: the real stream session, video grid, stage and
// HUD with canvas receivers. Only the native bridge and the peer connection
// are fakes. Fullscreen is entered through the app's own buttons (a user
// gesture), exactly like in the client.
test.beforeEach(async ({ page }) => {
    await page.route("**/fullscreen-fixture", route => route.fulfill({ contentType: "text/html", body: `<!doctype html><html lang="en" data-theme="arctic"><head>
        <link rel="stylesheet" href="/src/style.css"><link rel="stylesheet" href="/src/streams.css"><link rel="stylesheet" href="/src/workspace.css"><link rel="stylesheet" href="/src/arctic-night.css">
        <link rel="stylesheet" href="/src/arctic-stage.css"><link rel="stylesheet" href="/src/speaking-avatar.css"><link rel="stylesheet" href="/src/arctic-fullscreen.css">
        <style>body{margin:0;height:100vh}#center{height:100vh;display:flex;flex-direction:column}</style></head>
        <body><main id="center"><section id="chat-pane"><div id="video-grid" class="hidden"></div><div id="chat-wrap"><div id="chat-log"></div></div><div id="chat-input-row"><input id="chat-text" aria-label="Message"></div></section>
        <button id="voice-mute" aria-label="Mute microphone"></button><button id="voice-deafen" aria-label="Deafen"></button></main></body></html>` }));
    await page.setViewportSize({ width: 1400, height: 900 });
    await page.goto("/fullscreen-fixture");
    await page.evaluate(async () => {
        const bindings = [], started = performance.now();
        const pc = {
            connectionState: "connected", signalingState: "stable", getTransceivers: () => bindings,
            getStats: async () => {
                const report = new Map(), now = performance.now(), seconds = (now - started) / 1000;
                bindings.forEach((binding, i) => report.set(`in-${i}`, { id: `in-${i}`, type: "inbound-rtp", kind: "video", trackIdentifier: binding.receiver.track.id,
                    mid: binding.mid, timestamp: now, framesDecoded: Math.round(seconds * 60), framesReceived: Math.round(seconds * 60),
                    bytesReceived: Math.round(seconds * 6_200_000 / 8), frameWidth: 1920, frameHeight: 1080, ssrc: 100 + i }));
                return report;
            },
        };
        const streams = ["alice", "bob", "carol"].map((publisher_id, i) => ({ publisher_id, slot: "screen", generation: String(i + 1), preview_at: 0, watch_revision: "0" }));
        const calls = [], sent = [], gains = [], audio = new Map(streams.map(stream => [stream.publisher_id, { muted: false, volume: 100 }]));
        window.__fs = { calls, sent, gains, audio };
        const state = {
            pc, activeTabID: "one", serverGeneration: 1, sessionGeneration: 1, myChannelID: 1, myClientID: "me", myUniqueID: "u-me",
            settings: { gaming_overlay_game_ui: "mmo" }, avatars: new Map(), muted: false, deafened: false,
            channels: [{ ChannelID: 1, Name: "raid-night" }],
            clients: [["me", "Me"], ["alice", "Alice"], ["bob", "Bob"], ["carol", "Carol"], ["dave", "Dave"]]
                .map(([client_id, nickname]) => ({ client_id, unique_id: `u-${client_id}`, nickname, channel_id: 1, is_speaking: false })),
        };
        document.getElementById("voice-mute").onclick = () => { state.muted = !state.muted; };
        document.getElementById("voice-deafen").onclick = () => { state.deafened = !state.deafened; };
        window.__noxa = {
            state, $: id => document.getElementById(id), sysMsg: () => {}, toast: () => {}, announceLive: () => {}, initials: name => name.slice(0, 1), fetchAvatar: () => {},
            shareAudioCtl: {
                get: id => audio.get(String(id)) || null,
                setMuted: (id, muted) => { audio.get(String(id)).muted = muted; },
                setVolume: (id, volume) => { audio.get(String(id)).volume = volume; },
                setStageGains: map => gains.push(Object.fromEntries(map)),
            },
        };
        window.go = { main: { App: {
            SupportsStreamVideoQualityForTab: async () => true,
            SetStreamVideoQualityForTab: async () => "",
            SystemCPUPercent: async () => 10,
            GetClientInfoForTab: async () => ({ ping_ms: 24 }),
            SendChatForTab: async (tab, scope, target, text) => { sent.push([tab, scope, target, text]); return ""; },
            VideoStreamControlForTab: async (tab, msg) => {
                calls.push({ tab, ...msg });
                return { ...msg, streams: structuredClone(streams), session: msg.action === "list" ? "10" : msg.session };
            },
        } } };
        const controls = await import("/src/stream-controls.js");
        const video = await import("/src/video.js");
        window.__fs.controls = controls;
        window.__fs.video = video;
        controls.startStreamSession(pc, video.videoTrackAdded, video.videoTrackRemoved);
        streams.forEach((stream, i) => {
            const canvas = document.createElement("canvas");
            canvas.getContext("2d").fillRect(0, 0, 100, 100);
            const track = canvas.captureStream(5).getVideoTracks()[0];
            bindings.push({ mid: String(i), currentDirection: "recvonly", receiver: { track } });
            controls.receiveStreamTrack(track, { client_id: stream.publisher_id }, `${stream.publisher_id}|screen`);
        });
        window.__fs.speak = (id, speaking, whisper = false) => {
            const client = state.clients.find(c => c.client_id === id);
            client.is_speaking = speaking;
            client.is_whispering = speaking && whisper;
            video.videoSpeaking(id, speaking);
        };
        window.__fs.say = (from, text, id) => window.__noxaFullscreen.chatMessage({ id, channel_id: 1, from: from[0].toUpperCase() + from.slice(1),
            from_client_id: from, from_unique_id: `u-${from}`, text, enc: true, enc_mode: "server", enc_verified: true }, { added: true });
        window.__fs.dm = (from, text) => window.__noxaFullscreen.chatMessage({ from: from[0].toUpperCase() + from.slice(1), from_client_id: from,
            from_unique_id: `u-${from}`, to_unique_id: "u-me", text, direct: true, e2e: true, enc: true, enc_mode: "e2e", enc_verified: true }, { added: true, incomingDM: true });
    });
    await expect(page.getByRole("button", { name: "Watch", exact: true })).toHaveCount(3);
});

async function watch(page, ...publishers) {
    await page.locator(`[data-publisher="${publishers[0]}"] .stream-watch`).click();
    for (const publisher of publishers.slice(1)) {
        await page.evaluate(id => window.__fs.controls.availableStreams().find(stream => stream.publisherID === id).watch(), publisher);
        await expect(page.locator(`.vtile[data-clid="${publisher}"]`)).toBeVisible();
    }
}

const fullscreenClass = page => page.evaluate(() => document.fullscreenElement?.className || "");

async function enterSingle(page) {
    await watch(page, "alice");
    await page.locator(".stage-info").getByRole("button", { name: /^Full screen/ }).click();
    await expect.poll(() => fullscreenClass(page)).toContain("fs-hud-host");
    await page.mouse.move(700, 450);
}

test("a fullscreen stream shows telemetry, speakers and the dock; Esc exits", async ({ page }) => {
    await enterSingle(page);
    const hud = page.getByRole("region", { name: "Fullscreen stream controls" });
    await expect(hud).toBeVisible();
    expect(await fullscreenClass(page)).toContain("vtile");
    const telemetry = page.getByRole("group", { name: "Stream telemetry" });
    await expect(telemetry).toContainText("Alice");
    await expect(telemetry).toContainText("LIVE");
    await expect(telemetry).toContainText("1080p · 60 FPS", { timeout: 12000 });
    await expect(telemetry).toContainText("6.2 Mbps");
    await expect(telemetry.getByLabel("Ping 24 ms")).toHaveAttribute("data-quality", "good");

    // Speakers reuse the native speaking avatar (canvas + .speaking row).
    await page.evaluate(() => window.__fs.speak("bob", true));
    await page.mouse.move(650, 420);
    const speakers = page.getByRole("list", { name: "Voice · raid-night" });
    await expect(speakers.locator(".fs-speaker")).toHaveText([/Bob/, /Alice/, /Carol/, /Dave/]);
    await expect(speakers.locator(".fs-speaker.speaking .speaking-avatar canvas")).toHaveCount(1);
    await expect(speakers.locator(".fs-speaker", { hasText: "Me" })).toHaveCount(0);
    // The speakers sit at the overlay anchor (centre-left) clear of the MMO zones.
    const box = await page.locator(".fs-side").boundingBox();
    expect(box.x).toBeLessThan(40);
    expect(box.y).toBeGreaterThan(900 * 0.12);
    expect(box.y + box.height).toBeLessThan(900 * 0.7);

    const dock = page.getByRole("toolbar", { name: "Stream controls" });
    // Idle: only who is talking stays on screen.
    await expect(speakers.locator(".fs-speaker")).toHaveText([/Bob/], { timeout: 4000 });
    await page.mouse.move(700, 450);
    await dock.getByRole("button", { name: "Mute microphone" }).click();
    expect(await page.evaluate(() => window.__noxa.state.muted)).toBe(true);
    await expect(dock.getByRole("button", { name: "Unmute microphone" })).toHaveAttribute("aria-pressed", "true");
    const volume = dock.getByRole("slider", { name: "Stream volume" });
    await volume.fill("60");
    expect(await page.evaluate(() => window.__fs.audio.get("alice").volume)).toBe(60);
    await expect(dock.getByRole("radiogroup")).toBeHidden();

    await page.keyboard.press("Escape");
    await expect.poll(() => page.evaluate(() => document.fullscreenElement)).toBeNull();
    await expect(page.locator(".fs-hud")).toHaveCount(0);
    await expect(page.locator(".vtile.fs-hud-host")).toHaveCount(0);
});

test("the HUD auto-hides after 2.5 s without movement and returns on mouse or keyboard", async ({ page }) => {
    await enterSingle(page);
    const hud = page.locator(".fs-hud");
    await expect(hud).toHaveAttribute("data-hud", "shown");
    await page.waitForTimeout(1500);
    await page.mouse.move(720, 460);
    await page.waitForTimeout(1500);
    await expect(hud).toHaveAttribute("data-hud", "shown");
    await expect(hud).toHaveAttribute("data-hud", "hidden", { timeout: 2000 });
    await expect(page.locator(".vtile.fs-hud-host")).toHaveClass(/fs-hud-idle/);
    expect(await page.locator(".fs-dock").evaluate(node => getComputedStyle(node).opacity)).toBe("0");
    await page.mouse.move(600, 300);
    await expect(hud).toHaveAttribute("data-hud", "shown");
    await expect(hud).toHaveAttribute("data-hud", "hidden", { timeout: 4000 });

    // Keyboard: Tab reaches the dock and keeps the HUD up while focused.
    await page.keyboard.press("Tab");
    await expect(hud).toHaveAttribute("data-hud", "shown");
    const exit = page.getByRole("button", { name: "Exit fullscreen" });
    await exit.focus();
    await page.waitForTimeout(3000);
    await expect(hud).toHaveAttribute("data-hud", "shown");
    await page.keyboard.press("Enter");
    await expect.poll(() => page.evaluate(() => document.fullscreenElement)).toBeNull();
});

test("new channel messages peek under the speakers for about six seconds", async ({ page }) => {
    await enterSingle(page);
    await page.evaluate(() => window.__fs.say("bob", "nice pull, 4% → next one is the kill", 9001));
    const peek = page.getByRole("list", { name: "New messages" });
    await expect(peek.locator(".fs-peek-item")).toHaveText([/Bob\s*nice pull, 4% → next one is the kill/]);
    await expect(page.locator(".fs-chat .fs-badge")).toHaveText("1");
    await page.evaluate(() => window.__fs.say("bob", "nice pull, 4% → next one is the kill", 9001));
    await expect(peek.locator(".fs-peek-item")).toHaveCount(1);
    await page.waitForTimeout(4500);
    await expect(peek.locator(".fs-peek-item")).toHaveCount(1);
    await expect(peek.locator(".fs-peek-item")).toHaveCount(0, { timeout: 3000 });

    // C opens the chat drawer on the voice channel and sends there.
    await page.mouse.move(500, 500);
    await page.keyboard.press("c");
    const drawer = page.getByRole("region", { name: "Chat · raid-night" });
    await expect(drawer).toBeVisible();
    await expect(drawer.locator(".fs-bubble")).toHaveText([/Bob\s*nice pull/]);
    await expect(drawer.getByRole("textbox", { name: "Message #raid-night…" })).toBeFocused();
    await page.keyboard.type("go go go");
    await page.keyboard.press("Enter");
    expect(await page.evaluate(() => window.__fs.sent)).toEqual([["one", "channel", "1", "go go go"]]);
});

test("quick reply answers a private message without leaving fullscreen", async ({ page }) => {
    await enterSingle(page);
    await page.evaluate(() => window.__fs.speak("bob", true, true));
    await expect(page.locator(".fs-speaker.whispering .fs-whisper-tag")).toHaveText("Whisper");
    await page.evaluate(() => { window.__fs.dm("bob", "heads up, swapping at 3 stacks"); window.__fs.dm("bob", "taunt right after"); });
    const bubble = page.getByRole("region", { name: "Private message from Bob" });
    await expect(bubble).toBeVisible();
    await expect(bubble.locator(".fs-quick-sub")).toHaveText("private message");
    await expect(bubble.locator(".fs-bubble")).toHaveText(["heads up, swapping at 3 stacks", "taunt right after"]);
    const reply = bubble.getByRole("textbox", { name: "Reply to Bob" });
    await expect(reply).not.toBeFocused();

    // A second sender waits as unread; Alt+R answers the newest one.
    await reply.focus();
    await page.evaluate(() => window.__fs.dm("carol", "port after this?"));
    await expect(page.getByRole("button", { name: /1 unread · Carol/ })).toBeVisible();
    await page.keyboard.type("on it");
    await page.keyboard.press("Enter");
    expect(await page.evaluate(() => window.__fs.sent)).toEqual([["one", "direct", "u-bob", "on it"]]);
    await expect(bubble.locator(".fs-bubble.self")).toContainText("on it");
    await expect(reply).toHaveValue("");

    await page.keyboard.press("Alt+r");
    const carol = page.getByRole("region", { name: "Private message from Carol" });
    await expect(carol).toBeVisible();
    await expect(carol.getByRole("textbox", { name: "Reply to Carol" })).toBeFocused();
    await expect(page.locator(".fs-unread")).toBeHidden();

    // Esc in the reply field closes the bubble only; the next Esc exits.
    await page.keyboard.press("Escape");
    await expect(carol).toBeHidden();
    expect(await fullscreenClass(page)).toContain("fs-hud-host");
    await page.keyboard.press("Escape");
    await expect.poll(() => page.evaluate(() => document.fullscreenElement)).toBeNull();
});

test("multiview fullscreen switches grid, focus and stack from the dock", async ({ page }) => {
    await watch(page, "alice", "bob", "carol");
    await page.locator(".stage-head .stage-fullscreen").click();
    await expect.poll(() => fullscreenClass(page)).toContain("stream-stage");
    await page.mouse.move(700, 450);
    const stage = page.locator(".stream-stage");
    await expect(page.locator(".stage-head")).toBeHidden();
    const layouts = page.getByRole("toolbar", { name: "Stream controls" }).getByRole("radiogroup", { name: "Layout" });
    await expect(layouts.getByRole("radio", { name: "Grid" })).toHaveAttribute("aria-checked", "true");
    await layouts.getByRole("radio", { name: "Focus" }).click();
    await expect(stage).toHaveAttribute("data-layout", "focus");
    await layouts.getByRole("radio", { name: "Focus" }).press("ArrowRight");
    await expect(stage).toHaveAttribute("data-layout", "stack");
    await expect(layouts.getByRole("radio", { name: "Stack" })).toBeFocused();
    // Telemetry follows the focused tile; 1-5 still switch focus.
    await page.locator(".fs-layout[data-mode=grid]").click();
    await page.locator("body").press("2");
    await expect(page.getByRole("group", { name: "Stream telemetry" })).toContainText("Bob");
    // Multiview ducking only touches stream audio, never voice.
    expect(await page.evaluate(() => window.__fs.gains.at(-1))).toEqual({ alice: 0.15, bob: 1, carol: 0.15 });
    await page.getByRole("button", { name: "Exit fullscreen" }).click();
    await expect.poll(() => page.evaluate(() => document.fullscreenElement)).toBeNull();
    await expect(page.locator(".stage-head")).toBeVisible();
});

test("reduced motion removes the HUD slide", async ({ page }) => {
    await page.evaluate(() => { document.documentElement.dataset.reduceMotion = "1"; });
    await enterSingle(page);
    const transition = await page.locator(".fs-dock").evaluate(node => getComputedStyle(node).transitionDuration);
    expect(transition.split(",").every(value => parseFloat(value) === 0)).toBe(true);
});
