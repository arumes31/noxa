import { test, expect } from "./fixtures.js";

// Design 45 stream stage: the real stream session, video grid and stage with
// canvas receivers. Only the native bridge and the peer connection are fakes.
test.beforeEach(async ({ page }) => {
    await page.route("**/stage-fixture", route => route.fulfill({ contentType: "text/html", body: `<!doctype html><html lang="en" data-theme="arctic"><head>
        <link rel="stylesheet" href="/src/style.css"><link rel="stylesheet" href="/src/streams.css"><link rel="stylesheet" href="/src/workspace.css"><link rel="stylesheet" href="/src/arctic-night.css">
        <style>body{margin:0;height:100vh}#center{height:100vh;display:flex;flex-direction:column}</style></head>
        <body><main id="center"><section id="chat-pane"><div id="video-grid" class="hidden"></div><div id="chat-wrap"><div id="chat-log"></div></div><div id="chat-input-row"><input id="chat-text" aria-label="Message"></div></section></main></body></html>` }));
    await page.setViewportSize({ width: 1400, height: 900 });
    await page.goto("/stage-fixture");
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
        const calls = [], gains = [], audio = new Map(streams.map(stream => [stream.publisher_id, { muted: false, volume: 100 }]));
        window.__stage = { calls, gains, audio };
        window.__noxa = {
            state: { pc, activeTabID: "one", serverGeneration: 1, sessionGeneration: 1, myChannelID: 1, myClientID: "me", settings: {}, avatars: new Map(),
                channels: [{ ChannelID: 1, Name: "raid-night" }],
                clients: [["me", "Me"], ["alice", "Alice"], ["bob", "Bob"], ["carol", "Carol"]].map(([client_id, nickname]) => ({ client_id, nickname, channel_id: 1 })) },
            $: id => document.getElementById(id), sysMsg: () => {}, toast: () => {}, announceLive: () => {}, initials: name => name.slice(0, 1),
            shareAudioCtl: {
                get: id => audio.get(String(id)) || null,
                setMuted: (id, muted) => { audio.get(String(id)).muted = muted; },
                setVolume: (id, volume) => { audio.get(String(id)).volume = volume; },
                setStageGains: map => gains.push(Object.fromEntries(map)),
            },
        };
        window.go = { main: { App: {
            SupportsStreamVideoQualityForTab: async () => true,
            SetStreamVideoQualityForTab: async (tab, publisher, slot, generation, session, quality) => { calls.push({ action: "quality", publisher, quality }); return ""; },
            SystemCPUPercent: async () => 10,
            VideoStreamControlForTab: async (tab, msg) => {
                calls.push({ tab, ...msg });
                return { ...msg, streams: structuredClone(streams), session: msg.action === "list" ? "10" : msg.session };
            },
        } } };
        const controls = await import("/src/stream-controls.js");
        const video = await import("/src/video.js");
        window.__stage.controls = controls;
        controls.startStreamSession(pc, video.videoTrackAdded, video.videoTrackRemoved);
        streams.forEach((stream, i) => {
            const canvas = document.createElement("canvas");
            canvas.getContext("2d").fillRect(0, 0, 100, 100);
            const track = canvas.captureStream(5).getVideoTracks()[0];
            bindings.push({ mid: String(i), currentDirection: "recvonly", receiver: { track } });
            controls.receiveStreamTrack(track, { client_id: stream.publisher_id }, `${stream.publisher_id}|screen`);
        });
    });
    await expect(page.getByRole("button", { name: "Watch", exact: true })).toHaveCount(3);
});

// The first stream starts from its catalog card; once a stage is shown the
// catalog gives way and further streams arrive through the "+ Add" source.
async function watch(page, first, ...more) {
    await page.locator(`[data-publisher="${first}"] .stream-watch`).click();
    for (const publisher of more) {
        await page.evaluate(id => window.__stage.controls.availableStreams().find(stream => stream.publisherID === id).watch(), publisher);
        await expect(page.locator(`.vtile[data-clid="${publisher}"]`)).toBeVisible();
    }
}

test("one watched stream is a framed stage with measured stats over a resizable chat", async ({ page }) => {
    await watch(page, "alice");
    const stage = page.locator(".stream-stage");
    await expect(stage).toHaveAttribute("data-stage", "single");
    await expect(page.locator("#stream-catalog")).toBeHidden();
    const strip = page.locator(".stage-info");
    await expect(strip.locator(".stage-info-name")).toHaveText("Alice");
    await expect(strip.locator(".stage-live")).toHaveText("LIVE");
    await expect(strip.locator(".stage-info-stats")).toHaveText(/^1080p · 60 FPS · 6\.2 Mbps$/, { timeout: 12000 });
    const tile = await page.locator(".vtile").boundingBox();
    expect(Math.abs(tile.width / tile.height - 16 / 9)).toBeLessThan(0.02);
    await expect(page.locator(".stage-chat-head")).toContainText("raid-night");
    await expect(page.locator(".stage-chat-head")).toContainText("4 in voice");

    const splitter = page.getByRole("separator", { name: "Resize stream and chat" });
    await expect(splitter).toHaveAttribute("aria-valuenow", "62");
    await splitter.focus();
    await page.keyboard.press("ArrowUp");
    await page.keyboard.press("ArrowUp");
    await expect(splitter).toHaveAttribute("aria-valuenow", "54");
    expect(await page.evaluate(() => localStorage.getItem("noxa.stage.split"))).toBe("0.54");
    const box = await splitter.boundingBox();
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
    await page.mouse.down();
    await page.mouse.move(box.x + box.width / 2, 450, { steps: 4 });
    await page.mouse.up();
    expect(Number(await splitter.getAttribute("aria-valuenow"))).toBe(50);
    await splitter.dblclick();
    await expect(splitter).toHaveAttribute("aria-valuenow", "62");
    expect(await page.evaluate(() => localStorage.getItem("noxa.stage.split"))).toBeNull();

    await strip.getByRole("button", { name: "Theater mode" }).click();
    await expect(page.locator("#chat-pane")).toHaveClass(/stage-side/);
    await expect(splitter).toBeHidden();
    await page.getByRole("button", { name: "Leave theater mode" }).click();
    await expect(page.locator("#chat-pane")).toHaveClass(/stage-split/);
    expect(await page.evaluate(() => window.__stage.gains.at(-1) ?? {})).toEqual({});
});

test("+ Add watches through the normal watch path and opens multiview", async ({ page }) => {
    await watch(page, "alice");
    await page.locator(".stage-info").getByRole("button", { name: "Watch another active stream" }).click();
    const menu = page.getByRole("menu");
    await expect(menu.getByRole("menuitem")).toHaveText(["Bob · Screen share", "Carol · Screen share"]);
    await menu.getByRole("menuitem", { name: "Bob · Screen share" }).click();
    await expect(page.locator(".stream-stage")).toHaveAttribute("data-stage", "multi");
    expect(await page.evaluate(() => window.__stage.calls.filter(call => call.action === "watch").map(call => [call.publisher_id, call.active]))).toEqual([["alice", true], ["bob", true]]);
    await expect(page.getByRole("heading", { name: "Multiview" })).toBeVisible();
    await expect(page.locator(".stage-count")).toHaveText("2 streams");
    await page.locator(".stage-head .stage-add").click();
    await page.getByRole("menuitem", { name: "Carol · Screen share" }).click();
    await expect(page.locator(".stage-count")).toHaveText("3 streams");
    await expect(page.locator(".stage-head .stage-add")).toBeDisabled();
});

test("layout modes persist per session, 1-5 switch focus and stream audio follows it", async ({ page }) => {
    await watch(page, "alice", "bob", "carol");
    const stage = page.locator(".stream-stage");
    await expect(stage).toHaveAttribute("data-stage", "multi");
    await expect(stage).toHaveAttribute("data-layout", "grid");
    await expect(page.getByRole("radio", { name: "Grid" })).toHaveAttribute("aria-checked", "true");
    await page.getByRole("radio", { name: "Focus" }).click();
    await expect(stage).toHaveAttribute("data-layout", "focus");
    expect(await page.evaluate(() => sessionStorage.getItem("noxa.stage.mode:one"))).toBe("focus");
    await page.getByRole("radio", { name: "Focus" }).press("ArrowRight");
    await expect(stage).toHaveAttribute("data-layout", "stack");
    await expect(page.getByRole("radio", { name: "Stack" })).toBeFocused();
    await page.getByRole("radio", { name: "Grid" }).click();
    await page.getByRole("radio", { name: "Focus" }).click();

    await expect(page.locator(".stage-foot")).toContainText("Audio follows focus · others ducked to 15%");
    await expect(page.locator(".stage-foot kbd")).toHaveText("1–3");
    const focused = () => page.locator(".vtile[aria-current=true]").getAttribute("data-clid");
    expect(await focused()).toBe("alice");
    expect(await page.evaluate(() => window.__stage.gains.at(-1))).toEqual({ alice: 1, bob: 0.15, carol: 0.15 });
    await page.locator("body").press("2");
    expect(await focused()).toBe("bob");
    expect(await page.evaluate(() => window.__stage.gains.at(-1))).toEqual({ alice: 0.15, bob: 1, carol: 0.15 });
    await expect(page.locator(".stage-head .stage-audio")).toContainText("Audio: Bob");
    // Focused large view asks for the high layer through the existing allocation.
    await expect.poll(() => page.evaluate(() => window.__stage.calls.some(call => call.action === "quality" && call.publisher === "bob" && call.quality === "high"))).toBe(true);
    await page.locator("#chat-text").press("3");
    expect(await focused()).toBe("bob", "typing a digit in the composer never switches focus");
    await page.locator("body").press("Control+3");
    expect(await focused()).toBe("bob");
    await page.locator('.vtile[data-clid="carol"]').click({ position: { x: 300, y: 125 } });
    expect(await focused()).toBe("carol");

    await page.locator(".stage-head .stage-audio").click();
    await expect(page.getByRole("menuitemradio", { name: "Follow focus" })).toHaveAttribute("aria-checked", "true");
    await page.keyboard.press("ArrowDown");
    await expect(page.getByRole("menuitemradio", { name: "Alice · Screen share" })).toBeFocused();
    await page.keyboard.press("Enter");
    await expect(page.locator(".stage-foot")).toContainText("Audio: Alice · others ducked to 15%");
    await page.locator("body").press("2");
    expect(await page.evaluate(() => window.__stage.gains.at(-1))).toEqual({ alice: 1, bob: 0.15, carol: 0.15 });

    await page.reload();
    expect(await page.evaluate(() => sessionStorage.getItem("noxa.stage.mode:one"))).toBe("focus");
});

test("tile tools pin, mute and swap; leaving multiview restores full stream audio", async ({ page }) => {
    await watch(page, "alice", "bob");
    const bob = page.locator('.vtile[data-clid="bob"]');
    await bob.hover();
    await bob.getByRole("button", { name: "Mute shared audio · Bob" }).click();
    expect(await page.evaluate(() => window.__stage.audio.get("bob").muted)).toBe(true);
    await bob.getByRole("button", { name: "Pin to front · Bob" }).click();
    await expect(page.locator(".stream-stage")).toHaveAttribute("data-order", "bob|screen,alice|screen");
    await expect(bob.getByRole("button", { name: "Unpin · Bob" })).toHaveAttribute("aria-pressed", "true");
    const alice = page.locator('.vtile[data-clid="alice"]');
    await expect(alice).toHaveAttribute("aria-current", "true");
    await expect(alice.getByRole("button", { name: "Swap with focus · Alice" })).toBeHidden();
    await bob.hover();
    await bob.getByRole("button", { name: "Swap with focus · Bob" }).click();
    await expect(bob).toHaveAttribute("aria-current", "true");
    expect(await page.evaluate(() => window.__stage.gains.at(-1))).toEqual({ alice: 0.15, bob: 1 });
    await alice.hover();
    await alice.getByRole("button", { name: "Stop watching" }).click();
    await expect(page.locator(".stream-stage")).toHaveAttribute("data-stage", "single");
    expect(await page.evaluate(() => window.__stage.gains.at(-1))).toEqual({});
});

test("the side panel switches Chat and Viewers and collapses to a rail", async ({ page }) => {
    await watch(page, "alice", "bob");
    const pane = page.locator("#chat-pane");
    await expect(page.getByRole("tab", { name: "Chat" })).toHaveAttribute("aria-selected", "true");
    await page.getByRole("tab", { name: "Viewers · 4" }).click();
    await expect(pane).toHaveClass(/stage-panel-viewers/);
    await expect(page.locator("#chat-wrap")).toBeHidden();
    await expect(page.getByRole("tabpanel").getByRole("listitem")).toHaveText([/Me/, /Alice\s*LIVE/, /Bob\s*LIVE/, /Carol/]);
    await page.getByRole("tab", { name: "Viewers · 4" }).press("ArrowLeft");
    await expect(page.getByRole("tab", { name: "Chat" })).toBeFocused();
    await expect(page.locator("#chat-wrap")).toBeVisible();
    await page.getByRole("button", { name: "Collapse side panel" }).click();
    await expect(pane).toHaveClass(/stage-panel-collapsed/);
    await expect(page.locator("#chat-input-row")).toBeHidden();
    await page.locator(".stage-rail").getByRole("button", { name: "Viewers · 4" }).click();
    await expect(pane).not.toHaveClass(/stage-panel-collapsed/);
    await expect(page.getByRole("tab", { name: "Viewers · 4" })).toHaveAttribute("aria-selected", "true");
});

test("stage labels follow the German language", async ({ page }) => {
    await watch(page, "alice", "bob");
    await page.evaluate(async () => { (await import("/src/i18n.js")).setLanguage("de"); window.dispatchEvent(new Event("noxa-language-changed")); });
    await expect(page.getByRole("radio", { name: "Raster" })).toBeVisible();
    await expect(page.locator(".stage-head .stage-fullscreen")).toContainText("Vollbild");
    await expect(page.getByRole("tab", { name: "Zuschauer · 4" })).toBeVisible();
    await page.getByRole("radio", { name: "Fokus" }).click();
    await expect(page.locator(".stage-foot")).toContainText("Audio folgt dem Fokus · andere auf 15 % abgesenkt");
});
