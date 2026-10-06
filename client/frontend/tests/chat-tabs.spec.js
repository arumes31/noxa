import { test, expect } from "./fixtures.js";

const initialItems = [
    { key: "ch:1", name: "# Lobby", active: true },
    { key: "ch:2", name: "# Public", closable: true },
    { key: "dm:alice", name: "Alice", closable: true, unread: 12, arrivedAt: -60_000 },
    { key: "dm:bene", name: "Bene", closable: true },
    { key: "dm:cari", name: "Cari", closable: true },
    { key: "dm:daniel", name: "Daniel", closable: true },
    { key: "dm:david", name: "David", closable: true },
    { key: "dm:last", name: "Last conversation", closable: true },
];

const tab = (page, key) => page.locator(`.pm-tab[data-chat-key="${key}"]`);
const select = (page, key) => tab(page, key).locator(".pm-tab-select");
const order = page => page.locator(".pm-tab[data-chat-key]").evaluateAll(tabs => tabs.map(item => item.dataset.chatKey));

async function mount(page, { items = initialItems, layout = { order: [], pinned: [] }, ready = true } = {}) {
    await page.route("**/__chat_tabs__", route => route.fulfill({ contentType: "text/html", body: `
        <!doctype html><html><head><title>Chat tab navigation</title></head>
        <body><main style="width:440px;padding-top:36px"><div id="pm-tabs"></div><section id="chat-wrap" role="tabpanel"></section></main></body></html>` }));
    await page.goto("/__chat_tabs__");
    await page.evaluate(async ({ items, layout, ready }) => {
        await import("/src/style.css");
        const { createChatTabs } = await import("/src/chat-tabs.js");
        const { createUnreadTabEffects } = await import("/src/chat-unread.js");
        const root = document.getElementById("pm-tabs");
        const unread = createUnreadTabEffects(root);
        const state = { items, layout, ready, scope: "server-a/identity-a/session-a", opened: [], closed: [], saved: [], history: [] };
        const controller = createChatTabs(root, {
            onLayoutChange(next) {
                state.layout = structuredClone(next);
                state.saved.push(structuredClone(next));
                render();
            },
            onRefresh: () => unread.refresh(),
        });
        for (const item of items) if (item.arrivedAt < 0) item.arrivedAt += Date.now();
        function render() {
            controller.render(state.items.map(item => ({
                ...item,
                title: item.name,
                open() {
                    state.opened.push(item.key);
                    for (const candidate of state.items) candidate.active = candidate.key === item.key;
                    item.unread = 0;
                    render();
                },
                close: item.closable ? () => {
                    state.closed.push(item.key);
                    state.items = state.items.filter(candidate => candidate.key !== item.key);
                    render();
                } : undefined,
                clearHistory: item.closable ? () => state.history.push(item.key) : undefined,
            })), state.layout, { scope: state.scope, ready: state.ready });
        }
        window.__tabsFixture = { state, render, dispose: () => { controller.dispose(); unread.dispose(); } };
        render();
    }, { items, layout, ready });
}

test("tabs stay on one row and All chats opens a conversation beyond the viewport", async ({ page }) => {
    await mount(page);
    const list = page.locator(".pm-tab-list");
    expect(await list.evaluate(element => element.scrollWidth > element.clientWidth)).toBe(true);
    const tops = await page.locator(".pm-tab").evaluateAll(tabs => tabs.map(item => Math.round(item.getBoundingClientRect().top)));
    expect(new Set(tops).size).toBe(1);
    await page.getByRole("button", { name: /all chats/i }).click();
    await page.getByRole("menuitem", { name: /Last conversation/ }).click();
    await expect(select(page, "dm:last")).toHaveAttribute("aria-selected", "true");
    const bounds = await select(page, "dm:last").evaluate(element => {
        const item = element.getBoundingClientRect(), view = element.closest(".pm-tab-list").getBoundingClientRect();
        return { left: item.left, right: item.right, viewLeft: view.left, viewRight: view.right };
    });
    expect(bounds.left).toBeGreaterThanOrEqual(bounds.viewLeft - 1);
    expect(bounds.right).toBeLessThanOrEqual(bounds.viewRight + 1);
    const screenshot = test.info().outputPath("chat-tabs-overflow.png");
    await page.screenshot({ path: screenshot });
    await test.info().attach("Chat tabs with overflow", { path: screenshot, contentType: "image/png" });
});

test("arrow keys move focus without opening or marking unread chats as read", async ({ page }) => {
    await mount(page);
    await select(page, "ch:1").focus();
    await page.keyboard.press("ArrowRight");
    await expect(select(page, "ch:2")).toBeFocused();
    await page.keyboard.press("ArrowRight");
    await expect(select(page, "dm:alice")).toBeFocused();
    await expect(tab(page, "dm:alice").locator(".pm-unread")).toHaveText("12");
    await expect(select(page, "ch:1")).toHaveAttribute("aria-selected", "true");
    expect(await page.evaluate(() => window.__tabsFixture.state.opened)).toEqual([]);
    await page.keyboard.press("End");
    await expect(select(page, "dm:last")).toBeFocused();
    await page.keyboard.press("Home");
    await expect(select(page, "ch:1")).toBeFocused();
    await select(page, "dm:alice").focus();
    await page.keyboard.press("Enter");
    await expect(select(page, "dm:alice")).toHaveAttribute("aria-selected", "true");
    await expect(tab(page, "dm:alice").locator(".pm-unread")).toHaveCount(0);
    await select(page, "ch:2").focus();
    await page.keyboard.press("Space");
    await expect(select(page, "ch:2")).toHaveAttribute("aria-selected", "true");
    expect(await page.evaluate(() => window.__tabsFixture.state.opened)).toEqual(["dm:alice", "ch:2"]);
});

test("close controls are separate buttons and Delete cannot close the joined channel", async ({ page }) => {
    await mount(page);
    await expect(tab(page, "ch:1").locator(".pm-close")).toHaveCount(0);
    await select(page, "ch:1").focus();
    await page.keyboard.press("Delete");
    await expect(tab(page, "ch:1")).toHaveCount(1);
    const close = tab(page, "dm:alice").locator(".pm-close");
    expect(await close.evaluate(element => element.tagName)).toBe("BUTTON");
    expect(await close.evaluate(element => Boolean(element.closest(".pm-tab-select")))).toBe(false);
    await close.click();
    await expect(tab(page, "dm:alice")).toHaveCount(0);
    await select(page, "ch:2").focus();
    await page.keyboard.press("Delete");
    await expect(tab(page, "ch:2")).toHaveCount(0);
    await expect(select(page, "dm:bene")).toBeFocused();
    await expect(page.locator('.pm-tab-select[tabindex="0"]')).toHaveCount(1);
    expect(await page.evaluate(() => window.__tabsFixture.state.closed)).toEqual(["dm:alice", "ch:2"]);
    expect(await page.evaluate(() => window.__tabsFixture.state.opened)).toEqual([]);
});

test("pin and keyboard reorder preserve pinned and ordinary chat partitions", async ({ page }) => {
    await mount(page, { layout: { order: initialItems.map(item => item.key), pinned: ["dm:alice"] } });
    expect((await order(page))[0]).toBe("dm:alice");
    await select(page, "dm:bene").click({ button: "right" });
    await page.getByRole("menuitem", { name: /^Pin/ }).click();
    expect((await order(page)).slice(0, 2)).toEqual(["dm:alice", "dm:bene"]);
    const screenshot = test.info().outputPath("chat-tabs-pinned.png");
    await page.screenshot({ path: screenshot });
    await test.info().attach("Pinned chats and unread echo", { path: screenshot, contentType: "image/png" });
    await select(page, "dm:bene").focus();
    await page.keyboard.press("Control+Shift+ArrowLeft");
    expect((await order(page)).slice(0, 2)).toEqual(["dm:bene", "dm:alice"]);
    await page.keyboard.press("Control+Shift+ArrowLeft");
    expect((await order(page)).slice(0, 2)).toEqual(["dm:bene", "dm:alice"]);
    await select(page, "ch:1").focus();
    await page.keyboard.press("Control+Shift+ArrowLeft");
    expect((await order(page)).slice(0, 3)).toEqual(["dm:bene", "dm:alice", "ch:1"]);
    await page.keyboard.press("Control+Shift+ArrowRight");
    expect((await order(page)).slice(2, 4)).toEqual(["ch:2", "ch:1"]);
    await select(page, "dm:bene").click({ button: "right" });
    await page.getByRole("menuitem", { name: /^Unpin/ }).click();
    expect((await order(page))[0]).toBe("dm:alice");
    expect(await page.evaluate(() => window.__tabsFixture.state.layout.pinned)).toEqual(["dm:alice"]);
    expect(await page.evaluate(() => window.__tabsFixture.state.opened)).toEqual([]);
});

test("dragging reorders ordinary chats without moving a pinned chat into their partition", async ({ page }) => {
    await mount(page, { items: initialItems.slice(0, 5), layout: { order: initialItems.slice(0, 5).map(item => item.key), pinned: ["dm:alice"] } });
    await page.locator("main").evaluate(element => { element.style.width = "1000px"; });
    const before = await order(page);
    await tab(page, "dm:alice").dragTo(tab(page, "dm:cari"));
    expect(await order(page)).toEqual(before);
    await tab(page, "dm:cari").dragTo(tab(page, "ch:1"), { targetPosition: { x: 4, y: 12 } });
    expect(await order(page)).toEqual(["dm:alice", "dm:cari", "ch:1", "ch:2", "dm:bene"]);
    expect(await page.evaluate(() => window.__tabsFixture.state.layout.pinned)).toEqual(["dm:alice"]);
});

test("refresh preserves focused tab, manual scroll position and the unread arrival timestamp", async ({ page }) => {
    await mount(page);
    await select(page, "ch:1").focus();
    const list = page.locator(".pm-tab-list");
    await list.evaluate(element => { element.scrollLeft = element.scrollWidth; });
    const scroll = await list.evaluate(element => element.scrollLeft);
    expect(scroll).toBeGreaterThan(100);
    const arrivedAt = await page.evaluate(() => window.__tabsFixture.state.items.find(item => item.key === "dm:alice").arrivedAt);
    await page.evaluate(() => window.__tabsFixture.render());
    await expect(select(page, "ch:1")).toBeFocused();
    expect(await list.evaluate(element => element.scrollLeft)).toBeCloseTo(scroll, 0);
    await expect(tab(page, "dm:alice").locator(".pm-unread")).toHaveText("12");
    expect(await page.evaluate(() => window.__tabsFixture.state.items.find(item => item.key === "dm:alice").arrivedAt)).toBe(arrivedAt);
    const elapsed = await tab(page, "dm:alice").evaluate(element => parseFloat(element.style.getPropertyValue("--pm-unread-elapsed")));
    expect(elapsed).toBeLessThanOrEqual(-60_000);
});

test("layout cannot be changed before persistence loads and stale menus close on scope changes", async ({ page }) => {
    await mount(page, { ready: false });
    await select(page, "dm:alice").focus();
    await page.keyboard.press("Control+Shift+ArrowLeft");
    await select(page, "dm:alice").click({ button: "right" });
    await expect(page.getByRole("menuitem", { name: /^Pin/ })).toBeDisabled();
    expect(await page.evaluate(() => window.__tabsFixture.state.saved)).toEqual([]);
    await page.keyboard.press("Escape");
    await page.evaluate(() => { window.__tabsFixture.state.ready = true; window.__tabsFixture.render(); });
    await select(page, "dm:alice").click({ button: "right" });
    await expect(page.getByRole("menuitem", { name: /^Pin/ })).toBeEnabled();
    const stalePin = await page.getByRole("menuitem", { name: /^Pin/ }).elementHandle();
    await page.evaluate(() => {
        window.__tabsFixture.state.scope = "server-b/identity-b/session-b";
        window.__tabsFixture.render();
    });
    await expect(page.getByRole("menu")).toHaveCount(0);
    await page.getByRole("button", { name: /^All chats/ }).click();
    const currentMenu = await page.getByRole("menu").elementHandle();
    await stalePin.evaluate(element => element.click());
    expect(await currentMenu.evaluate(element => element.isConnected)).toBe(true);
    await expect(page.getByRole("menu")).toHaveCount(1);
    expect(await page.evaluate(() => window.__tabsFixture.state.saved)).toEqual([]);
});

test("names are rendered as text and opening menus does not clear unread messages", async ({ page }) => {
    const name = '<img src=x onerror="window.__nameExecuted=true">';
    await mount(page, { items: [{ key: "ch:1", name: "# Lobby", active: true }, { key: "dm:unsafe", name, closable: true, unread: 3 }] });
    await expect(select(page, "dm:unsafe")).toContainText(name);
    await expect(tab(page, "dm:unsafe").locator("img")).toHaveCount(0);
    await page.getByRole("button", { name: /all chats/i }).click();
    await expect(page.getByRole("menuitem", { name: new RegExp("img src=x") })).toContainText(name);
    await expect(page.getByRole("menu").locator("img")).toHaveCount(0);
    await page.keyboard.press("Escape");
    await expect(tab(page, "dm:unsafe").locator(".pm-unread")).toHaveText("3");
    expect(await page.evaluate(() => window.__nameExecuted)).toBeUndefined();
    expect(await page.evaluate(() => window.__tabsFixture.state.opened)).toEqual([]);
});

test("compact tabs retain accessible names, panel references and keyboard menu focus @a11y", async ({ page }) => {
    await page.setViewportSize({ width: 320, height: 280 });
    await mount(page, { layout: { order: initialItems.map(item => item.key), pinned: ["dm:alice"] } });
    await page.locator("main").evaluate(element => { element.style.width = "100%"; });
    const list = page.getByRole("tablist");
    await expect(list).toHaveAccessibleName(/.+/);
    await expect(page.getByRole("tab", { selected: true })).toHaveCount(1);
    await expect(page.locator('.pm-tab-select[tabindex="0"]')).toHaveCount(1);
    for (const button of await page.locator("#pm-tabs button").all()) await expect(button).toHaveAccessibleName(/.+/);
    for (const button of await page.getByRole("tab").all()) {
        const panel = await button.getAttribute("aria-controls");
        await expect(page.locator(`#${panel}`)).toHaveCount(1);
    }
    const more = page.getByRole("button", { name: /^All chats/ });
    const right = await more.evaluate(element => element.getBoundingClientRect().right);
    expect(right).toBeLessThanOrEqual(320);
    const tops = await page.locator(".pm-tab").evaluateAll(tabs => tabs.map(item => Math.round(item.getBoundingClientRect().top)));
    expect(new Set(tops).size).toBe(1);
    await more.focus();
    await page.keyboard.press("Enter");
    await expect(page.getByRole("menuitem").first()).toBeFocused();
    await page.keyboard.press("End");
    await expect(page.getByRole("menuitem").last()).toBeFocused();
    await page.keyboard.press("Escape");
    await expect(more).toBeFocused();
    await expect(more).toHaveAttribute("aria-expanded", "false");
    const screenshot = test.info().outputPath("chat-tabs-compact.png");
    await page.screenshot({ path: screenshot });
    await test.info().attach("Compact chat navigation", { path: screenshot, contentType: "image/png" });
});

test("detached tab callbacks cannot open, close or reorder another server's matching chat", async ({ page }) => {
    await mount(page);
    const oldSelect = await select(page, "dm:alice").elementHandle();
    const oldClose = await tab(page, "dm:alice").locator(".pm-close").elementHandle();
    const oldTab = await tab(page, "ch:2").elementHandle();
    const oldMore = await page.getByRole("button", { name: /^All chats/ }).elementHandle();
    await page.evaluate(() => {
        window.__tabsFixture.state.scope = "server-b/identity-b/session-b";
        window.__tabsFixture.render();
    });
    await oldMore.evaluate(element => element.click());
    await expect(page.getByRole("menu")).toHaveCount(0);
    await oldSelect.evaluate(element => element.click());
    await oldClose.evaluate(element => element.click());
    await oldTab.evaluate(element => element.dispatchEvent(new KeyboardEvent("keydown", {
        key: "ArrowLeft", ctrlKey: true, shiftKey: true, bubbles: true,
    })));
    expect(await page.evaluate(() => window.__tabsFixture.state.opened)).toEqual([]);
    expect(await page.evaluate(() => window.__tabsFixture.state.closed)).toEqual([]);
    expect(await page.evaluate(() => window.__tabsFixture.state.saved)).toEqual([]);
    await expect(tab(page, "dm:alice")).toHaveCount(1);
});

test("incoming updates keep the open All chats menu announced as expanded", async ({ page }) => {
    await mount(page);
    await page.getByRole("button", { name: /^All chats/ }).click();
    await expect(page.getByRole("menu")).toBeVisible();
    await page.evaluate(() => {
        window.__tabsFixture.state.items.find(item => item.key === "dm:alice").unread++;
        window.__tabsFixture.render();
    });
    await expect(page.getByRole("menu")).toBeVisible();
    await expect(page.getByRole("button", { name: /^All chats/ })).toHaveAttribute("aria-expanded", "true");
    await page.keyboard.press("Escape");
    await expect(page.getByRole("button", { name: /^All chats/ })).toBeFocused();
    await expect(page.getByRole("button", { name: /^All chats/ })).toHaveAttribute("aria-expanded", "false");
});

for (const operation of ["scope change", "disposal"]) {
    test(`tab ${operation} leaves an unrelated message context menu open`, async ({ page }) => {
        await mount(page);
        await page.evaluate(async () => {
            const { mountContextMenu } = await import("/src/context-menu.js");
            const menu = document.createElement("div");
            menu.className = "ctx-menu";
            const react = document.createElement("button");
            react.textContent = "React to message";
            react.onclick = () => { window.__messageReacted = true; };
            menu.append(react);
            mountContextMenu(menu, { x: 20, y: 120 });
        });
        await expect(page.getByRole("menuitem", { name: "React to message" })).toBeFocused();
        await page.evaluate(operation => {
            if (operation === "disposal") window.__tabsFixture.dispose();
            else {
                window.__tabsFixture.state.scope = "server-b/identity-b/session-b";
                window.__tabsFixture.render();
            }
        }, operation);
        await expect(page.getByRole("menuitem", { name: "React to message" })).toBeVisible();
        await expect(page.getByRole("menuitem", { name: "React to message" })).toBeFocused();
        await page.keyboard.press("Enter");
        expect(await page.evaluate(() => window.__messageReacted)).toBe(true);
    });
}
