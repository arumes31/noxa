import { expect, test } from "./fixtures.js";

test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
        window.__events = {};
        window.runtime = { EventsOn: (name, fn) => { (window.__events[name] ||= []).push(fn); return () => {}; }, EventsEmit() {}, WindowIsFullscreen: async () => false };
        const settings = { language: "en", onboarding_done: true, alpha_dismissed: "test", bookmarks: [], chat_max_lines: 200 };
        window.go = { main: { App: new Proxy({}, { get(_target, method) { return async (...args) => {
            if (method === "GetSettings") return structuredClone(settings);
            if (method === "SaveSettings") { Object.assign(settings, args[0]); window.__overlaySaved = args[0]; return ""; }
            if (method === "GamingOverlayAvailable") return window.__overlayAvailable !== false;
            if (method === "UpdateGamingOverlay") { (window.__overlayUpdates ||= []).push(structuredClone(args[0])); return ""; }
            if (method === "GetGamingOverlayMonitors") return [{ id: "primary", name: "Display 1", width: 1920, height: 1080, primary: true }, { id: "left", name: "Display 2", width: 1280, height: 720, primary: false }];
            if (method === "PreviewGamingOverlay") { window.__overlayPreview = args[0]; return ""; }
            if (["ListTabs", "GetPermissions"].includes(method)) return [];
            if (["IdentityInfo", "GetAvatar"].includes(method)) return {};
            if (["Connected", "IsGuest"].includes(method)) return false;
            if (["ClientVersion", "ClientVersionShort"].includes(method)) return "test";
            return "";
        }; } }) } };
    });
    await page.goto("/");
    await page.waitForFunction(() => !!window.__noxa?.openSettings);
});

test("voice overlay shows only active speakers and hides immediately on silence", async ({ page }) => {
    await page.evaluate(() => {
        Object.assign(window.__noxa.state, { myClientID: 1, myChannelID: 1, pc: {}, channels: [{ ChannelID: 1, Name: "Lobby" }], clients: [
            { client_id: 1, channel_id: 1, nickname: "Self", unique_id: "self-uid", is_speaking: false },
            { client_id: 2, channel_id: 1, nickname: "Peer", unique_id: "peer-uid", is_speaking: false },
        ] });
    });
    await expect.poll(() => page.evaluate(() => window.__overlayUpdates?.at(-1)?.active)).toBe(false);
    await page.evaluate(() => { window.__noxa.state.clients[0].is_speaking = true; });
    await expect.poll(() => page.evaluate(() => window.__overlayUpdates.at(-1).speakers?.[0])).toMatchObject({ name: "Self", speaking: true });
    // Continuous speech must survive the old five-second visibility limit.
    await page.waitForTimeout(5500);
    await expect.poll(() => page.evaluate(() => window.__overlayUpdates.at(-1).active)).toBe(true);
    await page.evaluate(() => { window.__noxa.state.muted = true; });
    await expect.poll(() => page.evaluate(() => window.__overlayUpdates.at(-1).active)).toBe(false);
    await page.evaluate(() => { window.__noxa.state.clients[1].is_speaking = true; });
    await page.waitForTimeout(600);
    expect(await page.evaluate(() => window.__overlayUpdates.at(-1))).toEqual({ active: false });
    await page.evaluate(() => { window.__noxa.state.muted = false; window.__noxa.state.clients[0].is_speaking = false; });
    await expect.poll(() => page.evaluate(() => window.__overlayUpdates.at(-1).speakers?.[0])).toMatchObject({ name: "Peer", speaking: true });
    await page.evaluate(() => { window.__noxa.state.deafened = true; });
    await expect.poll(() => page.evaluate(() => window.__overlayUpdates.at(-1))).toEqual({ active: false });
    await page.evaluate(() => { window.__noxa.state.deafened = false; });
    await expect.poll(() => page.evaluate(() => window.__overlayUpdates.at(-1).speakers?.[0])).toMatchObject({ name: "Peer", speaking: true });
    await page.evaluate(() => { window.__noxa.state.pc = null; });
    await expect.poll(() => page.evaluate(() => window.__overlayUpdates.at(-1).active)).toBe(false);
    await page.evaluate(() => { window.__noxa.state.pc = {}; window.__noxa.state.sessionGeneration++; });
    await expect.poll(() => page.evaluate(() => window.__overlayUpdates.at(-1).active)).toBe(true);
});

test("global and private-call mute controls hide the overlay until cleared", async ({ page }) => {
    await page.evaluate(() => {
        window.__overlayCall = { active: true, label: "Private call", speakers: [{ id: "peer", name: "Peer", speaking: true }] };
        window.__noxaPrivateCalls.overlaySnapshot = () => window.__overlayCall;
    });
    for (const target of ["global", "private-call"]) {
        for (const flag of ["muted", "deafened"]) {
            await expect.poll(() => page.evaluate(() => window.__overlayUpdates.at(-1).speakers?.[0])).toMatchObject({ name: "Peer", speaking: true });
            await page.evaluate(({ target, flag }) => {
                (target === "global" ? window.__noxa.state : window.__overlayCall)[flag] = true;
            }, { target, flag });
            await expect.poll(() => page.evaluate(() => window.__overlayUpdates.at(-1))).toEqual({ active: false });
            await page.evaluate(({ target, flag }) => {
                (target === "global" ? window.__noxa.state : window.__overlayCall)[flag] = false;
            }, { target, flag });
        }
    }
    await expect.poll(() => page.evaluate(() => window.__overlayUpdates.at(-1).active)).toBe(true);
});

test("private-call colors also distinguish the local identity from every other participant", async ({page}) => {
    await page.evaluate(()=>{
        window.__noxaPrivateCalls.overlaySnapshot=()=>({active:true,selfID:"local-account",speakers:[
            {id:"local-account",name:"Self",speaking:true},{id:"remote-account",name:"Peer",speaking:true},
        ]});
    });
    await expect.poll(()=>page.evaluate(()=>window.__overlayUpdates.at(-1).speakers?.[0]?.color)).toBe("#43c798");
    const other=await page.evaluate(()=>window.__overlayUpdates.at(-1).speakers[1].color);
    const [r,g,b]=other.slice(1).match(/../g).map(value=>parseInt(value,16));
    expect(b).toBeGreaterThanOrEqual(g); expect(g).toBeGreaterThan(r);
});

test("channel colors stay distinct and stable when quiet members speak or new members join", async ({ page }) => {
    await page.evaluate(() => Object.assign(window.__noxa.state,{myClientID:1,myChannelID:1,pc:{},channels:[{ChannelID:1,Name:"Lobby"}],clients:[
        {client_id:1,channel_id:1,unique_id:"alpha",nickname:"Alice",is_speaking:true},
        {client_id:2,channel_id:1,unique_id:"beta",nickname:"Bob",is_speaking:true},
        {client_id:3,channel_id:1,unique_id:"gamma",nickname:"Quiet",is_speaking:false},
    ]}));
    await expect.poll(() => page.evaluate(() => window.__overlayUpdates.at(-1).speakers?.length)).toBe(2);
    const first = await page.evaluate(() => window.__overlayUpdates.at(-1).speakers.map(member=>[member.id,member.color]));
    expect(new Map(first).get("alpha")).toBe("#43c798");
    expect(first[0][1]).not.toBe(first[1][1]);
    await page.evaluate(() => {
        const state=window.__noxa.state;state.clients[2].is_speaking=true;
        state.clients.push({client_id:4,channel_id:1,unique_id:"new",nickname:"New",is_speaking:true});
        state.clients.reverse();
    });
    await expect.poll(() => page.evaluate(() => window.__overlayUpdates.at(-1).speakers?.length)).toBe(4);
    const next = await page.evaluate(() => window.__overlayUpdates.at(-1).speakers.map(member=>[member.id,member.color]));
    for (const [id,color] of first) expect(new Map(next).get(id)).toBe(color);
    expect(new Set(next.map(([,color])=>color)).size).toBe(4);
    for (const [,color] of next) expect(color).toMatch(/^#[0-9a-f]{6}$/);
    for (const [id,color] of next) if (id !== "alpha") {
        const [r,g,b]=color.slice(1).match(/../g).map(value=>parseInt(value,16));
        expect(b).toBeGreaterThanOrEqual(g); expect(g).toBeGreaterThan(r);
    }
});

test("overlay color settings preview drafts, Cancel discards them and Apply updates live colors", async ({page}) => {
    await page.evaluate(() => Object.assign(window.__noxa.state,{myClientID:1,myChannelID:1,pc:{},clients:[
        {client_id:1,channel_id:1,unique_id:"me",nickname:"Self",is_speaking:true},
        {client_id:2,channel_id:1,unique_id:"peer",nickname:"Peer",is_speaking:true},
    ]}));
    await expect.poll(()=>page.evaluate(()=>window.__overlayUpdates.at(-1).speakers?.[0]?.color)).toBe("#43c798");
    await page.evaluate(()=>window.__noxa.openSettings("overlay"));
    await expect(page.getByLabel("Your overlay color",{exact:true})).toHaveValue("#43c798");
    await page.getByLabel("Your overlay color",{exact:true}).fill("#ff8800");
    await page.getByLabel("Other members: first blue",{exact:true}).fill("180");
    await page.getByLabel("Other members: last blue",{exact:true}).fill("200");
    await page.getByRole("button",{name:"Preview in game",exact:true}).click();
    expect(await page.evaluate(()=>window.__overlayPreview)).toMatchObject({gaming_overlay_self_color:"#ff8800",gaming_overlay_blue_start:180,gaming_overlay_blue_end:200});
    expect(await page.locator(".overlay-sample-person").first().evaluate(el=>el.style.getPropertyValue("--overlay-color"))).toBe("#ff8800");
    expect(await page.evaluate(()=>window.__overlayUpdates.at(-1).speakers[0].color)).toBe("#43c798");
    await page.getByRole("button",{name:"Cancel",exact:true}).click();
    await page.evaluate(()=>window.__noxa.openSettings("overlay"));
    await expect(page.getByLabel("Your overlay color",{exact:true})).toHaveValue("#43c798");
    await page.getByLabel("Your overlay color",{exact:true}).fill("#ff8800");
    await page.getByLabel("Other members: first blue",{exact:true}).fill("180");
    await page.getByLabel("Other members: last blue",{exact:true}).fill("200");
    await page.getByRole("button",{name:"Apply",exact:true}).click();
    await expect.poll(()=>page.evaluate(()=>window.__overlayUpdates.at(-1).speakers[0].color)).toBe("#ff8800");
    expect(await page.evaluate(()=>window.__overlaySaved)).toMatchObject({gaming_overlay_self_color:"#ff8800",gaming_overlay_blue_start:180,gaming_overlay_blue_end:200});
});

test("overlay preferences stay drafts until Apply, support keyboard positioning and native preview", async ({ page }, testInfo) => {
    await page.evaluate(() => window.__noxa.openSettings("application"));
    await expect(page.getByLabel("Voice overlay", { exact: true })).toHaveCount(0);
    await page.getByRole("tab", { name: "Fullscreen overlay", exact: true }).click();
    await expect(page.getByRole("tabpanel", { name: "Fullscreen overlay", exact: true })).toBeVisible();
    await page.screenshot({ path: testInfo.outputPath("overlay-settings-section.png") });
    await expect(page.getByLabel("Voice overlay", { exact: true })).toBeChecked();
    await expect(page.getByLabel("Overlay monitor", { exact: true })).toHaveValue("");
    await page.getByLabel("Overlay monitor", { exact: true }).selectOption("left");
    await page.getByLabel("Overlay size", { exact: true }).fill("150");
    await page.getByLabel("Overlay opacity", { exact: true }).fill("40");
    // Speaking-only defaults on; chat peek and auto-hide default off.
    await expect(page.getByLabel("Only show who's speaking", { exact: true })).toBeChecked();
    await expect(page.getByLabel("Only show who's speaking", { exact: true })).toBeEnabled();
    await expect(page.getByLabel("Chat peek", { exact: true })).not.toBeChecked();
    await expect(page.getByLabel("Auto-hide when idle", { exact: true })).not.toBeChecked();
    const sample = page.getByRole("button", { name: "Move overlay preview" });
    await sample.focus(); await page.keyboard.press("Shift+ArrowRight"); await page.keyboard.press("ArrowDown");
    await expect(page.locator('.ovl-anchor[aria-checked="true"]')).toHaveCount(0);
    await expect(page.locator(".ovl-anchor-name")).toHaveText("Custom position");
    await expect(page.locator(".overlay-position-readout")).toContainText("10% horizontal, 46% vertical");
    await expect(sample).toContainText("Sam");
    expect(await page.evaluate(() => window.__overlaySaved)).toBeUndefined();
    await page.getByRole("button", { name: "Preview in game", exact: true }).click();
    const draft = await page.evaluate(() => window.__overlayPreview);
    expect(draft).toMatchObject({ gaming_overlay_monitor: "left", gaming_overlay_scale: 150, gaming_overlay_opacity: 40, gaming_overlay_position: "custom", gaming_overlay_x: 10, gaming_overlay_y: 46 });
    expect(Object.keys(draft).every(key => key.startsWith("gaming_overlay"))).toBe(true);
    expect(await page.evaluate(() => window.__overlaySaved)).toBeUndefined();
    await page.getByRole("button", { name: "Apply", exact: true }).click();
    expect(await page.evaluate(() => window.__overlaySaved)).toMatchObject(draft);
    await page.getByRole("tab", { name: "General", exact: true }).click();
    await page.locator("#settings-search").fill("Overlay opacity");
    const hit = page.locator(".set-search-hit");
    await expect(hit).toHaveCount(1);
    await expect(hit.locator(".set-search-page")).toHaveText("Fullscreen overlay");
    await hit.click();
    await expect(page.getByRole("tab", { name: "Fullscreen overlay", exact: true })).toHaveAttribute("aria-selected", "true");
    await expect(page.getByLabel("Overlay opacity", { exact: true })).toBeFocused();
    await expect(page.getByLabel("Overlay opacity", { exact: true })).toHaveValue("40");
});

test("speaker avatars reach the native overlay as bounded thumbnails and clear across sessions", async ({ page }) => {
    await page.evaluate(() => {
        const canvas = document.createElement("canvas"); canvas.width = 128; canvas.height = 256;
        const ctx = canvas.getContext("2d"); ctx.fillStyle = "#dc0000"; ctx.fillRect(0, 0, 128, 256);
        const state = window.__noxa.state;
        state.avatars.set("peer-uid", canvas.toDataURL("image/png"));
        Object.assign(state, { myClientID: 1, myChannelID: 1, pc: {}, clients: [
            { client_id: 2, channel_id: 1, unique_id: "peer-uid", nickname: "Peer", is_speaking: true },
            { client_id: 3, channel_id: 1, unique_id: "quiet-uid", nickname: "Quiet", is_speaking: false },
        ] });
    });
    await expect.poll(() => page.evaluate(() => window.__overlayUpdates.at(-1).speakers?.[0]?.avatar)).toMatch(/^data:image\/png;base64,/);
    const avatar = await page.evaluate(async () => {
        const snapshot = window.__overlayUpdates.at(-1);
        const source = snapshot.speakers[0].avatar;
        const img = new Image(); img.src = source; await img.decode();
        return { count: snapshot.speakers.length, width: img.naturalWidth, height: img.naturalHeight, bytes: source.length };
    });
    expect(avatar).toMatchObject({ count: 1, width: 64, height: 64 }); expect(avatar.bytes).toBeLessThanOrEqual(32768);
    await page.evaluate(() => {
        const state = window.__noxa.state;
        state.serverGeneration++; state.avatars.clear();
        state.avatars.set("peer-uid", "https://invalid.example/avatar.png");
    });
    await expect.poll(() => page.evaluate(() => window.__overlayUpdates.at(-1).speakers?.[0]?.avatar)).toBe("");
    await page.evaluate(() => { window.__noxa.state.clients[0].is_speaking = false; });
    // The native overlay fades a member who stopped, so they stay listed
    // (not speaking) for a few seconds before the snapshot empties.
    await expect.poll(() => page.evaluate(() => window.__overlayUpdates.at(-1).speakers?.[0]?.speaking)).toBe(false);
    await expect.poll(() => page.evaluate(() => window.__overlayUpdates.at(-1)), { timeout: 8000 }).toEqual({ active: false });
});

test("overlay animation overrides system motion settings and has its own saved toggle", async ({ page }) => {
    await page.emulateMedia({ reducedMotion: "no-preference" });
    await page.evaluate(() => window.__noxa.openSettings("overlay"));
    const canvas = page.locator(".overlay-animation-canvas").first();
    await expect.poll(() => canvas.evaluate(el => el.width)).toBeGreaterThan(0);
    const first = await canvas.evaluate(el => el.toDataURL());
    await expect.poll(() => canvas.evaluate(el => el.toDataURL())).not.toBe(first);
    await page.emulateMedia({ reducedMotion: "reduce" });
    const reduced = await canvas.evaluate(el => el.toDataURL());
    await expect.poll(() => canvas.evaluate(el => el.toDataURL())).not.toBe(reduced);
    const animation = page.getByLabel("Talk animation", { exact: true });
    await expect(animation).toHaveValue("halo");
    await animation.selectOption("still");
    await canvas.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    const frozen = await canvas.evaluate(el => el.toDataURL());
    await page.waitForTimeout(150);
    expect(await canvas.evaluate(el => el.toDataURL())).toBe(frozen);
    await page.getByRole("button", { name: "Preview in game", exact: true }).click();
    expect(await page.evaluate(() => window.__overlayPreview.gaming_overlay_animate)).toBe(false);
    expect(await page.evaluate(() => window.__overlaySaved)).toBeUndefined();
    await page.getByRole("button", { name: "Apply", exact: true }).click();
    expect(await page.evaluate(() => window.__overlaySaved.gaming_overlay_animate)).toBe(false);
    await animation.selectOption("halo");
    await expect.poll(() => canvas.evaluate(el => el.toDataURL())).not.toBe(frozen);
});

test("overlay preview drag remains bounded and Cancel discards draft position", async ({ page }) => {
    await page.setViewportSize({ width: 800, height: 900 });
    await page.evaluate(() => window.__noxa.openSettings("overlay"));
    const sample = page.getByRole("button", { name: "Move overlay preview" });
    await sample.scrollIntoViewIfNeeded();
    const tile = await sample.boundingBox(), area = await page.locator(".overlay-position-preview").boundingBox();
    await page.mouse.move(tile.x + 8, tile.y + 8); await page.mouse.down();
    await page.mouse.move(area.x - 100, area.y + area.height + 100, { steps: 5 }); await page.mouse.up();
    await expect(page.locator(".overlay-position-readout")).toContainText("0% horizontal, 100% vertical");
    await page.getByRole("button", { name: "Cancel", exact: true }).click();
    expect(await page.evaluate(() => window.__overlaySaved)).toBeUndefined();
    await page.evaluate(() => window.__noxa.openSettings("overlay"));
    await expect(page.locator('.ovl-anchor[data-anchor="center-left"]')).toHaveAttribute("aria-checked", "true");
});

test("unsupported native overlays disable controls with a clear explanation", async ({ page }) => {
    await page.evaluate(() => { window.__overlayAvailable = false; window.__noxa.openSettings("overlay"); });
    await expect(page.getByLabel("Voice overlay", { exact: true })).toBeDisabled();
    await expect(page.getByRole("button", { name: "Preview in game", exact: true })).toBeDisabled();
    await expect(page.getByText("The native voice overlay is available on Windows.", { exact: true })).toBeVisible();
});

test("breathing light halo replaces old styles and hugs every name", async ({ page }, testInfo) => {
    await page.evaluate(() => { window.__noxa.state.settings.gaming_overlay_style = "bars"; window.__noxa.openSettings("overlay"); });
    await expect(page.getByLabel("Talk animation", { exact: true })).toHaveValue("halo");
    await expect(page.locator('#ovl-talk option[value="halo"]')).toHaveText("Light halo + comb (noXa)");
    await expect(page.locator(".overlay-sample-wave, option[value='bars'], option[value='mist-aurora']")).toHaveCount(0);
    const canvases = page.locator(".overlay-animation-canvas");
    await expect(canvases).toHaveCount(2);
    await expect.poll(() => canvases.first().evaluate(canvas => canvas.width)).toBeGreaterThan(0);
    const pixels = await page.locator(".overlay-position-sample").evaluate(sample => {
        const people = [...sample.querySelectorAll(".overlay-sample-person:not([hidden])")];
        const bounds = people.map(person => person.getBoundingClientRect());
        const results = people.map(person => {
            const name = person.querySelector(".overlay-sample-name"), a = name.getBoundingClientRect();
            const composite = document.createElement("canvas"); composite.width = Math.ceil(a.width); composite.height = Math.ceil(a.height);
            const ctx = composite.getContext("2d");
            for (const canvas of sample.querySelectorAll("canvas")) {
                const b = canvas.getBoundingClientRect();
                ctx.drawImage(canvas, b.x-a.x, b.y-a.y, b.width, b.height);
            }
            let max = 0, visible = 0;
            const data = ctx.getImageData(0,0,composite.width,composite.height).data;
            for (let i = 3; i < data.length; i += 4) { max = Math.max(max, data[i]); if (data[i]) visible++; }
            return { max, visible, background: getComputedStyle(name).backgroundColor };
        });
        return { results, gap: (bounds[1].y-bounds[0].bottom)/Number(sample.style.getPropertyValue("--overlay-scale")) };
    });
    for (const name of pixels.results) {
        expect(name.visible).toBeGreaterThan(0);
        expect(name.background).toBe("rgba(0, 0, 0, 0)");
    }
    expect(pixels.gap).toBeCloseTo(32, 1);
    await page.locator(".overlay-position-preview").screenshot({ path: testInfo.outputPath("light-halo-settings.png") });
    expect(await page.evaluate(() => window.__overlaySaved)).toBeUndefined();
    await page.getByRole("button", { name: "Preview in game", exact: true }).click();
    expect(await page.evaluate(() => window.__overlayPreview.gaming_overlay_style)).toBe("default");
    await page.getByRole("button", { name: "Apply", exact: true }).click();
    expect(await page.evaluate(() => window.__overlaySaved.gaming_overlay_style)).toBe("default");
    await page.getByRole("button", { name: "Cancel", exact: true }).click();
    await page.evaluate(() => window.__noxa.openSettings("overlay"));
    await expect(page.getByLabel("Talk animation", { exact: true })).toHaveValue("halo");
});

test("light halo and comb follow long names, freeze when disabled and stay bounded on narrow screens", async ({ page }) => {
    await page.evaluate(() => window.__noxa.openSettings("overlay"));
    const canvas = page.locator(".overlay-animation-canvas").first();
    await expect.poll(() => canvas.evaluate(el => el.width)).toBeGreaterThan(0);
    const first = await canvas.evaluate(el => ({ width: el.width, frame: el.toDataURL() }));
    await expect.poll(() => canvas.evaluate(el => el.toDataURL())).not.toBe(first.frame);
    await page.evaluate(() => { document.querySelector(".overlay-sample-name").textContent = "TheLegendaryNightwalker"; });
    await expect.poll(() => canvas.evaluate(el => el.width)).toBeGreaterThan(first.width);
    await page.getByLabel("Talk animation", { exact: true }).selectOption("still");
    await canvas.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    const frozen = await canvas.evaluate(el => el.toDataURL());
    await page.waitForTimeout(150);
    expect(await canvas.evaluate(el => el.toDataURL())).toBe(frozen);
    // The preview frame models a 960 px wide desktop, so the sample keeps
    // the overlay's proportions: avatar = 40 px × size × frame / 960.
    const expected = size => page.locator(".overlay-position-preview").evaluate((el, size) => 40 * size / 100 * el.clientWidth / 960, size);
    const avatar = () => page.locator(".overlay-sample-avatar").first().evaluate(el => el.getBoundingClientRect().width);
    await page.getByLabel("Overlay size", { exact: true }).fill("200");
    await expect.poll(async () => Math.abs(await avatar() - await expected(200))).toBeLessThan(1);
    await page.getByLabel("Overlay size", { exact: true }).fill("75");
    await expect.poll(async () => Math.abs(await avatar() - await expected(75))).toBeLessThan(1);
    await page.setViewportSize({ width: 400, height: 850 });
    await expect.poll(() => canvas.evaluate(el => {
        const area = el.closest(".overlay-position-preview").getBoundingClientRect(), bounds = el.getBoundingClientRect();
        return bounds.width <= area.width;
    })).toBe(true);
});

test("behaviour toggles drive the settings preview and save with Apply", async ({ page }, testInfo) => {
    await page.evaluate(() => window.__noxa.openSettings("overlay"));
    const quiet = page.locator(".overlay-sample-quiet"), peek = page.locator(".overlay-sample-peek");
    await expect(quiet).toBeHidden();
    await expect(peek).toBeHidden();
    await page.locator(".overlay-position-preview").screenshot({ path: testInfo.outputPath("preview-default.png") });
    await page.getByLabel("Only show who's speaking", { exact: true }).uncheck();
    await expect(quiet).toBeVisible();
    // The quiet member keeps the still avatar + name look: no halo canvas.
    await expect(page.locator(".overlay-animation-canvas")).toHaveCount(2);
    await expect(quiet.locator(".overlay-animation-canvas")).toHaveCount(0);
    await page.getByLabel("Chat peek", { exact: true }).check();
    await expect(peek).toBeVisible();
    await expect(peek).toHaveText(/Sam\s*gg, one more round\?/);
    // Same text treatment as the names, and no panel behind it.
    const style = await peek.evaluate(node => ({ background: getComputedStyle(node).backgroundColor, border: getComputedStyle(node).borderStyle,
        shadow: getComputedStyle(node).textShadow, name: getComputedStyle(document.querySelector(".overlay-sample-details")).textShadow }));
    expect(style).toMatchObject({ background: "rgba(0, 0, 0, 0)", border: "none" });
    expect(style.shadow).toBe(style.name);
    await page.getByLabel("Auto-hide when idle", { exact: true }).check();
    await page.locator(".overlay-position-preview").screenshot({ path: testInfo.outputPath("preview-all-members-peek.png") });
    await page.getByRole("button", { name: "Preview in game", exact: true }).click();
    expect(await page.evaluate(() => window.__overlayPreview)).toMatchObject({ gaming_overlay_only_speaking: false, gaming_overlay_chat_peek: true, gaming_overlay_auto_hide: true });
    expect(await page.evaluate(() => window.__overlaySaved)).toBeUndefined();
    await page.getByRole("button", { name: "Apply", exact: true }).click();
    expect(await page.evaluate(() => window.__overlaySaved)).toMatchObject({ gaming_overlay_only_speaking: false, gaming_overlay_chat_peek: true, gaming_overlay_auto_hide: true });
});

test("chat peek forwards only allowed, encryption-aware lines to the native overlay", async ({ page }) => {
    await page.evaluate(() => {
        Object.assign(window.__noxa.state.settings, { gaming_overlay_chat_peek: true, blocked_users: ["blocked-uid"], channel_notify: { "#9": { muted: true } } });
        document.hasFocus = () => false; // playing a game: noXa is in the background
        window.__chat = data => { for (const callback of window.__events.event || []) callback(JSON.stringify({ type: "chat", data })); };
        window.__peek = () => (window.__overlayUpdates || []).at(-1)?.peek || [];
    });
    await page.evaluate(() => window.__chat({ id: 1, channel_id: 7, from: "Sam", from_unique_id: "sam", text: "gg, one more round?", enc_verified: true, enc_mode: "server" }));
    await expect.poll(() => page.evaluate(() => window.__peek().map(line => [line.name, line.text]))).toEqual([["Sam", "gg, one more round?"]]);
    expect(await page.evaluate(() => window.__overlayUpdates.at(-1).active)).toBe(true);
    await page.evaluate(() => {
        window.__chat({ id: 2, channel_id: 9, from: "Muted", from_unique_id: "m", text: "muted channel text", enc_verified: true, enc_mode: "server" });
        window.__chat({ id: 3, channel_id: 7, from: "Blocked", from_unique_id: "blocked-uid", text: "blocked text", enc_verified: true, enc_mode: "server" });
        window.__chat({ id: 4, channel_id: 7, from: "Mallory", from_unique_id: "mallory", text: "SECRET sent in plaintext" });
    });
    await expect.poll(() => page.evaluate(() => window.__peek().length)).toBe(2);
    const lines = await page.evaluate(() => window.__peek());
    expect(lines[1].name).toBe("Mallory");
    expect(lines[1].text).not.toContain("SECRET");
    expect(JSON.stringify(lines)).not.toMatch(/muted channel|blocked text/);
    // Lines expire on their own; turning the setting off clears them at once.
    await page.evaluate(() => { window.__noxa.state.settings.gaming_overlay_chat_peek = false; });
    await expect.poll(() => page.evaluate(() => window.__overlayUpdates.at(-1))).toEqual({ active: false });
});
