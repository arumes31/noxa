import { expect, test } from "./fixtures.js";

// (601) Long logs render only the rows near the viewport. These tests load
// large histories through the normal history path and check that the window
// keeps every chat behaviour working.

const rows = page => page.locator("#chat-log .msg.rich");

test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
        window.__events = {};
        window.runtime = { EventsOn: (name, fn) => { (window.__events[name] ||= []).push(fn); return () => {}; }, EventsEmit() {}, WindowIsFullscreen: async () => false };
        window.go = { main: { App: new Proxy({}, { get(_target, method) { return async () => {
            if (method === "GetSettings") return { language: "en", onboarding_done: true, alpha_dismissed: "test", effects_enabled: false, bookmarks: [], chat_max_lines: 200 };
            if (["ListTabs", "GetPermissions", "ListStreamsForTab"].includes(method)) return [];
            if (["IdentityInfo", "GetAvatar", "GetMediaLimitsForTab"].includes(method)) return {};
            if (["Connected", "IsGuest", "GamingOverlayAvailable"].includes(method)) return false;
            if (["ClientVersion", "ClientVersionShort"].includes(method)) return "test";
            if (method === "ChatPinsForTab") return { pins: [] };
            return "";
        }; } }) } };
    });
    await page.goto("/");
    await page.waitForFunction(() => !!window.__noxaShell && !!window.__noxaChat);
});

async function setupHistory(page, { latest, firstPage = 0, around = null, after = null }) {
    await page.setViewportSize({ width: 1280, height: 900 });
    await page.evaluate(({ latest, firstPage, around, after }) => {
        const v = window.__noxa;
        v.showWorkspace(false);
        Object.assign(v.state, { activeTabID: "server-a", myClientID: "member-a", myChannelID: 1, channels: [{ ChannelID: 1, Name: "Room" }] });
        v.state.settings.chat_max_lines = 5000;
        const message = id => ({ id, from_unique_id: id % 3 ? "bene" : "joshi", from_nickname: id % 3 ? "Bene" : "Joshi", body: (id % 1000 === 7 ? "needle " : "") + `history ${id}` + (id % 10 === 0 ? "\nsecond line" : ""), sent_at: 1700000000 + id * 60, enc_verified: true, enc_mode: "server" });
        const range = (from, to) => { const out = []; for (let id = to; id >= from; id--) out.push(message(id)); return out; }; // newest first
        const f = window.__virtual = { calls: [], pins: { pins: [] } };
        const app = window.go.main.App;
        window.go.main.App = new Proxy(app, { get(target, key) {
            if (key === "ChatHistoryForTab") return async (tab, ch, before, limit) => {
                f.calls.push(["history", before, limit]);
                if (!before) return { messages: range(firstPage ? Math.max(latest[0], latest[1] - firstPage + 1) : latest[0], latest[1]) };
                if (before <= latest[0]) return { messages: [] };
                const to = Math.min(before - 1, latest[1]);
                return { messages: range(Math.max(latest[0], to - limit + 1), to) };
            };
            if (key === "ChatHistoryAroundForTab" && around) return async (tab, ch, id, limit) => {
                f.calls.push(["around", id, limit]);
                return { around_id: id, messages: range(around[0], around[1]) };
            };
            if (key === "ChatHistoryAfterForTab" && after) return async (tab, ch, id, limit) => {
                f.calls.push(["after", id, limit]);
                const from = id + 1, to = Math.min(after, id + limit);
                return { after_id: id, messages: from > to ? [] : range(from, to) };
            };
            if (key === "ChatPinsForTab") return async () => structuredClone(f.pins);
            return target[key];
        } });
    }, { latest, firstPage, around, after });
}

async function openChannel(page) {
    await page.evaluate(() => window.__noxaChat.onMyChannelChanged());
}

const frame = page => page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));

test("100,000 messages render a bounded window and stay smooth while scrolling", async ({ page }) => {
    await setupHistory(page, { latest: [1, 100_000] });
    const started = Date.now();
    await openChannel(page);
    await expect(page.locator('#chat-log .msg[data-msg-id="100000"]')).toBeVisible();
    const openMs = Date.now() - started;
    const rendered = await rows(page).count();
    expect(rendered).toBeGreaterThan(5);
    expect(rendered).toBeLessThan(200);
    expect(openMs).toBeLessThan(8000);

    // Jumping through the whole log keeps the window small and each step fast.
    const timings = await page.evaluate(async () => {
        const log = document.getElementById("chat-log");
        const out = [];
        for (let i = 1; i <= 20; i++) {
            const t0 = performance.now();
            log.scrollTop = log.scrollHeight * (i / 21);
            log.dispatchEvent(new Event("scroll"));
            await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)));
            out.push({ ms: performance.now() - t0, rows: log.querySelectorAll(".msg.rich").length, first: Number(log.querySelector(".msg.rich")?.dataset.msgId) });
        }
        return out;
    });
    for (const step of timings) expect(step.rows).toBeLessThan(200);
    const firsts = timings.map(step => step.first);
    expect(firsts).toEqual([...firsts].sort((a, b) => a - b));
    expect(firsts[0]).toBeGreaterThan(1000);
    expect(firsts.at(-1)).toBeLessThan(99_000);
    const average = timings.reduce((sum, step) => sum + step.ms, 0) / timings.length;
    expect(average).toBeLessThan(250);

    // The rows on screen are the rows the scroll position names.
    const visible = await page.evaluate(() => {
        const log = document.getElementById("chat-log");
        const top = log.getBoundingClientRect().top, bottom = log.getBoundingClientRect().bottom;
        return [...log.querySelectorAll(".msg.rich")].filter(row => { const r = row.getBoundingClientRect(); return r.bottom > top && r.top < bottom; }).length;
    });
    expect(visible).toBeGreaterThan(3);
    await expect(page.locator(".chat-virtual-spacer").first()).toHaveAttribute("aria-hidden", "true");
});

test("search, jump-to and print cover rows outside the rendered window", async ({ page }) => {
    await setupHistory(page, { latest: [1, 5000] });
    await openChannel(page);
    await expect(page.locator('#chat-log .msg[data-msg-id="5000"]')).toBeVisible();
    await expect(page.locator('#chat-log .msg[data-msg-id="1500"]')).toHaveCount(0);

    // Search filters the data, so off-screen matches count and render.
    await page.evaluate(() => {
        document.getElementById("chat-search-btn").click();
        const search = document.getElementById("chat-search");
        search.value = "needle";
        search.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await expect(page.locator("#chat-search-count")).toHaveText(/5/);
    await expect(rows(page)).toHaveCount(5);
    await expect(page.locator("#chat-log .msg.rich mark").first()).toHaveText("needle");

    // Jumping to a message hidden by the search clears it and reveals the row.
    await page.evaluate(() => window.__noxaChat.jumpMessageReference({ kind: "channel", channel_id: 1, message_id: 1500 }));
    await expect(page.locator('#chat-log .msg[data-msg-id="1500"]')).toHaveClass(/flash/);
    await expect(page.locator('#chat-log .msg[data-msg-id="1500"]')).toBeInViewport();
    await expect(page.locator("#chat-search")).toHaveValue("");
    expect(await rows(page).count()).toBeLessThan(200);

    // Printing renders every row, then restores the window.
    await page.evaluate(() => window.dispatchEvent(new Event("beforeprint")));
    await expect(rows(page)).toHaveCount(5000);
    await page.evaluate(() => window.dispatchEvent(new Event("afterprint")));
    await frame(page);
    expect(await rows(page).count()).toBeLessThan(200);
    await expect(page.locator('#chat-log .msg[data-msg-id="1500"]')).toBeInViewport();
});

test("a live message keeps a long log at the bottom; reading scrollback shows the pill", async ({ page }) => {
    await setupHistory(page, { latest: [1, 1000] });
    await openChannel(page);
    await expect(page.locator('#chat-log .msg[data-msg-id="1000"]')).toBeVisible();
    await page.evaluate(() => window.__noxaChat.addChat({ id: 1001, channel_id: 1, from_unique_id: "bene", from: "Bene", text: "live one", enc_verified: true, enc_mode: "server" }));
    await expect(page.locator('#chat-log .msg[data-msg-id="1001"]')).toBeInViewport();
    expect(await rows(page).count()).toBeLessThan(200);

    await page.evaluate(() => { const log = document.getElementById("chat-log"); log.scrollTop = log.scrollHeight / 2; log.dispatchEvent(new Event("scroll")); });
    await frame(page);
    const firstVisible = () => page.evaluate(() => {
        const log = document.getElementById("chat-log");
        const top = log.getBoundingClientRect().top;
        const row = [...log.querySelectorAll(".msg.rich")].find(el => el.getBoundingClientRect().bottom > top + 1);
        return row ? { id: row.dataset.msgId, offset: Math.round(row.getBoundingClientRect().top - top) } : null;
    });
    const before = await firstVisible();
    await page.evaluate(() => window.__noxaChat.addChat({ id: 1002, channel_id: 1, from_unique_id: "bene", from: "Bene", text: "live two", enc_verified: true, enc_mode: "server" }));
    await expect(page.locator("#chat-newpill")).toBeVisible();
    await expect(page.locator('#chat-log .msg[data-msg-id="1002"]')).toHaveCount(0);
    // The reading position held, and the loaded scrollback was not trimmed.
    const after = await firstVisible();
    expect(after.id).toBe(before.id);
    expect(Math.abs(after.offset - before.offset)).toBeLessThanOrEqual(2);
    await page.locator("#chat-newpill").click();
    await expect(page.locator('#chat-log .msg[data-msg-id="1002"]')).toBeInViewport();
});

test("a jump far back opens a window around the message and pages back to the present", async ({ page }) => {
    await setupHistory(page, { latest: [151, 200], around: [50, 149], after: 200 });
    await openChannel(page);
    await expect(page.locator('#chat-log .msg[data-msg-id="200"]')).toBeVisible();
    await jumpViaPin(page, 100);
    await expect(page.locator('#chat-log .msg[data-msg-id="100"]')).toHaveClass(/flash/);
    expect(await page.evaluate(() => window.__virtual.calls.filter(call => call[0] === "around"))).toEqual([["around", 100, 100]]);
    // One request instead of paging: no older pages were fetched.
    expect(await page.evaluate(() => window.__virtual.calls.filter(call => call[0] === "history").map(call => call[1]))).toEqual([0]);
    await expect(page.locator('#chat-log .msg[data-msg-id="200"]')).toHaveCount(0);
    await expect(page.locator("#chat-newpill")).toHaveText(/Jump to present/);

    // A live message waits while the window is detached.
    await page.evaluate(() => window.__noxaChat.addChat({ id: 201, channel_id: 1, from_unique_id: "bene", from: "Bene", text: "while away", enc_verified: true, enc_mode: "server" }));
    await expect(page.locator('#chat-log .msg[data-msg-id="201"]')).toHaveCount(0);
    await expect(page.locator("#chat-newpill")).toHaveText(/1 new/i);

    // Scrolling to the bottom pages forward; reaching the present merges it.
    for (let i = 0; i < 4; i++) {
        await page.evaluate(() => { const log = document.getElementById("chat-log"); log.scrollTop = log.scrollHeight; log.dispatchEvent(new Event("scroll")); });
        await frame(page);
    }
    await expect(page.locator('#chat-log .msg[data-msg-id="201"]')).toHaveCount(1);
    await expect.poll(() => page.evaluate(() => window.__virtual.calls.filter(call => call[0] === "after").map(call => call[1]))).toEqual([149, 199]);
    const ids = await page.evaluate(() => [...document.querySelectorAll("#chat-log .msg[data-msg-id]")].map(row => Number(row.dataset.msgId)));
    expect(ids).toEqual([...ids].sort((a, b) => a - b));
    expect(new Set(ids).size).toBe(ids.length);
});

async function jumpViaPin(page, id) {
    await page.evaluate(id => { window.__virtual.pins = { pins: [{ message_id: id, message: { body: `history ${id}`, from_nickname: "Bene" } }] }; }, id);
    await page.locator("#chat-pins-btn").evaluate(button => button.click());
    await page.locator('.pin-row button[title="jump to message"]').click();
}

test("jump to present drops a detached window and reloads the newest page", async ({ page }) => {
    await setupHistory(page, { latest: [151, 200], around: [50, 149], after: 200 });
    await openChannel(page);
    await expect(page.locator('#chat-log .msg[data-msg-id="200"]')).toBeVisible();
    await jumpViaPin(page, 100);
    await expect(page.locator('#chat-log .msg[data-msg-id="100"]')).toHaveClass(/flash/);
    await page.locator("#chat-newpill").click();
    await expect(page.locator('#chat-log .msg[data-msg-id="200"]')).toBeInViewport();
    await expect(page.locator('#chat-log .msg[data-msg-id="100"]')).toHaveCount(0);
    await expect(page.locator("#chat-newpill")).toBeHidden();
});

test("servers without window support fall back to paging older history", async ({ page }) => {
    await setupHistory(page, { latest: [1, 200], firstPage: 50 });
    await page.evaluate(() => {
        // An old server answers an around request with the latest page.
        const app = window.go.main.App;
        window.go.main.App = new Proxy(app, { get(target, key) {
            if (key === "ChatHistoryAroundForTab") return async () => { window.__virtual.calls.push(["around-old"]); return { messages: [] }; };
            return target[key];
        } });
    });
    await openChannel(page);
    await expect(page.locator('#chat-log .msg[data-msg-id="200"]')).toBeVisible();
    await jumpViaPin(page, 120);
    await expect(page.locator('#chat-log .msg[data-msg-id="120"]')).toHaveClass(/flash/);
    expect(await page.evaluate(() => window.__virtual.calls.map(call => call[0] === "history" ? call[1] : call[0]))).toEqual([0, "around-old", 151]);
});
